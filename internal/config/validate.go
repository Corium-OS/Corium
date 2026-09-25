package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Validate checks the configuration and returns every problem it finds, joined
// into a single error.
//
// All problems are reported at once rather than one per run: an operator
// iterating on a node's configuration through a reboot cycle should not have to
// discover their mistakes one boot at a time.
//
// Validation performs no network access and no filesystem access, so it is safe
// to run in CI against a configuration for a node that does not exist yet.
func (c *Config) Validate() error {
	var problems []error

	problems = append(problems, c.validateRole()...)
	problems = append(problems, c.validateNetwork()...)
	problems = append(problems, c.validateStorage()...)
	problems = append(problems, c.validateJoin()...)
	problems = append(problems, c.validateNode()...)
	problems = append(problems, c.validateAddons()...)
	problems = append(problems, c.validateHA()...)
	problems = append(problems, c.validateUpgrades()...)
	problems = append(problems, c.validateBackup()...)
	problems = append(problems, c.validateRAID()...)
	problems = append(problems, c.validateZFS()...)
	problems = append(problems, c.validateLUKS()...)
	problems = append(problems, c.validateWireGuard()...)
	problems = append(problems, c.validateAPI()...)

	return errors.Join(problems...)
}

func (c *Config) validateRole() []error {
	switch c.Role {
	case RoleSingle, RoleController, RoleControllerWorker, RoleWorker:
		return nil
	case "":
		// A document that names no role is not an incomplete document: it is a
		// node saying "I will be told what I am". Demanding a placeholder role
		// would mean writing down something untrue, which takes effect the day
		// somebody turns the API off.
		//
		// The one thing that has to be true is that somebody can still answer.
		// With the API running they can, over `cctl apply` or `cctl enroll
		// --config`; with it off the question could never reach the node, and a
		// machine waiting for an answer nobody can give is worse than one that
		// refused to boot and said why. The document that arrives later must
		// name a role, and the bootstrap checks that before it builds anything.
		if c.API.Mode() != APIModeDisabled {
			return nil
		}

		return []error{errors.New(
			"role: required (single, controller, controller+worker or worker); " +
				"a document may omit it only when the API is on, which is how a " +
				"node says it is waiting to be told what it is")}
	default:
		return []error{fmt.Errorf("role: unknown value %q", c.Role)}
	}
}

func (c *Config) validateNetwork() []error {
	var problems []error

	for _, cidr := range []struct{ field, value string }{
		{"network.podCIDR", c.Network.PodCIDR},
		{"network.serviceCIDR", c.Network.ServiceCIDR},
	} {
		if cidr.value == "" {
			continue
		}

		if _, err := netip.ParsePrefix(cidr.value); err != nil {
			problems = append(problems,
				fmt.Errorf("%s: %q is not a valid CIDR", cidr.field, cidr.value))
		}
	}

	// Overlapping pod and service ranges produce a cluster that comes up and
	// then misroutes traffic in ways that are miserable to diagnose. Catching
	// it here costs nothing.
	pod, podErr := netip.ParsePrefix(c.Network.PodCIDR)
	svc, svcErr := netip.ParsePrefix(c.Network.ServiceCIDR)

	if podErr == nil && svcErr == nil {
		if pod.Overlaps(svc) {
			problems = append(problems, fmt.Errorf(
				"network: podCIDR %s overlaps serviceCIDR %s", pod, svc))
		}
	}

	switch c.Network.CNI {
	case CNIKubeRouter, CNICalico, CNICustom, "":
	default:
		problems = append(problems,
			fmt.Errorf("network.cni: unknown value %q", c.Network.CNI))
	}

	return problems
}

func (c *Config) validateStorage() []error {
	var problems []error

	switch c.Storage.Type {
	case StorageEtcd, StorageSQLite, "":
	default:
		problems = append(problems,
			fmt.Errorf("storage.type: unknown value %q", c.Storage.Type))
	}

	// SQLite cannot be shared between controllers. Accepting this combination
	// would produce a cluster that works until the second controller joins and
	// then fails in a way that looks like a networking problem.
	if c.Storage.Type == StorageSQLite && c.Role == RoleController {
		problems = append(problems, errors.New(
			"storage.type: sqlite cannot back a multi-controller cluster; use etcd, or role: single"))
	}

	return problems
}

func (c *Config) validateJoin() []error {
	var problems []error

	hasToken := c.Join.Token != ""
	hasSource := c.Join.TokenFrom != nil

	if hasToken && hasSource {
		problems = append(problems, errors.New(
			"join: set either token or tokenFrom, not both"))
	}

	// A worker with no way to authenticate cannot join anything. Failing here
	// is far kinder than a node that boots, looks healthy, and never appears in
	// the cluster.
	if c.Role == RoleWorker && !hasToken && !hasSource {
		problems = append(problems, errors.New(
			"join: required for role worker (set join.token or join.tokenFrom)"))
	}

	if c.Role == RoleSingle && (hasToken || hasSource) {
		problems = append(problems, errors.New(
			"join: meaningless for role single, which bootstraps its own cluster"))
	}

	if hasSource {
		problems = append(problems, c.Join.TokenFrom.validate("join.tokenFrom")...)
	}

	return problems
}

// validate checks a secret source, reporting problems against the field it was
// reached through. The field is passed in rather than assumed: the same type
// now backs join.tokenFrom, ha.authPassFrom and api.operatorCAFrom, and an
// error that names the wrong key sends an operator to the wrong line.
func (t *SecretSource) validate(field string) []error {
	var problems []error

	hasURL := t.URL != ""
	hasFile := t.File != ""

	switch {
	case hasURL && hasFile:
		problems = append(problems, fmt.Errorf(
			"%s: set either url or file, not both", field))
	case !hasURL && !hasFile:
		problems = append(problems, fmt.Errorf(
			"%s: set either url or file", field))
	}

	if hasURL {
		parsed, err := url.Parse(t.URL)
		switch {
		case err != nil:
			problems = append(problems,
				fmt.Errorf("%s.url: %q is not a valid URL", field, t.URL))
		case parsed.Scheme != "https":
			// A secret fetched over plain HTTP is a secret handed to anyone on
			// the path. There is no opt-out for this.
			problems = append(problems, fmt.Errorf(
				"%s.url: scheme must be https, got %q", field, parsed.Scheme))
		}
	}

	if hasFile && !strings.HasPrefix(t.File, "/") {
		problems = append(problems, fmt.Errorf(
			"%s.file: must be an absolute path, got %q", field, t.File))
	}

	if t.WaitFor != "" {
		switch d, err := time.ParseDuration(t.WaitFor); {
		case err != nil:
			problems = append(problems, fmt.Errorf(
				"%s.waitFor: %q is not a duration; use a form like 15m or 1h",
				field, t.WaitFor))
		case d < 0:
			problems = append(problems, fmt.Errorf(
				"%s.waitFor: %q is negative", field, t.WaitFor))
		case d > maxWaitFor:
			// A node stuck waiting is a node nobody is looking at. An hour is
			// already generous for "the first controller is still coming up".
			problems = append(problems, fmt.Errorf(
				"%s.waitFor: %q is longer than the %s maximum", field, t.WaitFor, maxWaitFor))
		}
	}

	return problems
}

// hostnamePattern is the RFC 1123 subset Kubernetes accepts for a node name.
var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (c *Config) validateNode() []error {
	var problems []error

	if name := c.Node.Name; name != "" {
		// Kubernetes lowercases node names, so an uppercase one here would
		// register as something other than what was written.
		switch {
		case len(name) > 63:
			problems = append(problems, fmt.Errorf(
				"node.name: %q is %d characters, the limit is 63", name, len(name)))
		case !hostnamePattern.MatchString(name):
			problems = append(problems, fmt.Errorf(
				"node.name: %q must be lowercase letters, digits and hyphens, "+
					"starting and ending with a letter or digit", name))
		}
	}

	for i, taint := range c.Node.Taints {
		if taint.Key == "" {
			problems = append(problems,
				fmt.Errorf("node.taints[%d].key: required", i))
		}

		switch taint.Effect {
		case "NoSchedule", "PreferNoSchedule", "NoExecute":
		case "":
			problems = append(problems,
				fmt.Errorf("node.taints[%d].effect: required", i))
		default:
			problems = append(problems, fmt.Errorf(
				"node.taints[%d].effect: must be NoSchedule, PreferNoSchedule or NoExecute, got %q",
				i, taint.Effect))
		}
	}

	return problems
}

func (c *Config) validateAddons() []error {
	var problems []error

	// Add-ons are installed by the control plane. A worker declaring them is
	// expressing an intent that will never be carried out.
	if len(c.Addons) > 0 && !c.Role.IsController() {
		problems = append(problems, fmt.Errorf(
			"addons: only a controller installs add-ons, but role is %q", c.Role))
	}

	repositories := make(map[string]bool)

	for _, addon := range c.Addons {
		if addon.Repository != nil {
			repositories[addon.Repository.Name] = true
		}
	}

	for i, addon := range c.Addons {
		if addon.Name == "" {
			problems = append(problems, fmt.Errorf("addons[%d].name: required", i))
		}

		if addon.Chart == "" {
			problems = append(problems, fmt.Errorf("addons[%d].chart: required", i))
			continue
		}

		repo, _, qualified := strings.Cut(addon.Chart, "/")
		if !qualified {
			problems = append(problems, fmt.Errorf(
				"addons[%d].chart: expected repository/chart, got %q", i, addon.Chart))
			continue
		}

		if !repositories[repo] {
			problems = append(problems, fmt.Errorf(
				"addons[%d].chart: repository %q is not declared by any addon", i, repo))
		}
	}

	return problems
}

// keepalivedAuthPassLimit is how many characters of a VRRP password keepalived
// actually uses. Longer values are silently truncated, which is how two
// controllers end up disagreeing about a password they both believe they set.
const keepalivedAuthPassLimit = 8

// maxWaitFor bounds how long a node will wait for a secret to appear.
const maxWaitFor = time.Hour

func (c *Config) validateHA() []error {
	if !c.HA.Enabled {
		// Configuring HA without enabling it is almost always a mistake worth
		// reporting: the operator wrote settings that do nothing.
		if c.HA.VirtualIP != "" || c.HA.AuthPass != "" || c.HA.AuthPassFrom != nil {
			return []error{errors.New(
				"ha: settings given but ha.enabled is false, so none of them take effect")}
		}

		return nil
	}

	var problems []error

	// Control plane load balancing elects a leader among controllers. With one
	// controller there is no election, and with a worker there is no control
	// plane to balance.
	switch c.Role {
	case RoleController, RoleControllerWorker:
	case RoleSingle:
		problems = append(problems, errors.New(
			"ha: role single cannot be highly available; it is one node by definition"))
	default:
		problems = append(problems, fmt.Errorf(
			"ha: only a controller can run control plane load balancing, but role is %q", c.Role))
	}

	if c.HA.VirtualIP == "" {
		problems = append(problems, errors.New("ha.virtualIP: required when ha is enabled"))
	} else if _, err := netip.ParsePrefix(c.HA.VirtualIP); err != nil {
		// The prefix length is not decoration: keepalived needs it to add the
		// address to the interface.
		problems = append(problems, fmt.Errorf(
			"ha.virtualIP: %q must be an address with a prefix length, such as 192.168.0.200/24",
			c.HA.VirtualIP))
	}

	// Zero means "unset": Corium omits the field and k0s assigns an ID starting
	// at 51. Saying "must be 1-255" would be wrong, since leaving it out is
	// both valid and the common case.
	if c.HA.VirtualRouterID < 0 || c.HA.VirtualRouterID > 255 {
		problems = append(problems, fmt.Errorf(
			"ha.virtualRouterID: %d is out of range; use 1-255, or omit it and "+
				"k0s assigns one starting at 51", c.HA.VirtualRouterID))
	}

	problems = append(problems, c.validateAuthPass()...)

	for i, peer := range c.HA.UnicastPeers {
		if _, err := netip.ParseAddr(peer); err != nil {
			problems = append(problems, fmt.Errorf(
				"ha.unicastPeers[%d]: %q is not an IP address", i, peer))
		}
	}

	// The virtual IP only helps if clients are told to use it and the API
	// server certificate covers it.
	if c.Cluster.Endpoint == "" {
		problems = append(problems, errors.New(
			"cluster.endpoint: required when ha is enabled; set it to the virtual IP so "+
				"clients and joining nodes use the address that survives a controller failing"))
	}

	return problems
}

func (c *Config) validateAuthPass() []error {
	hasInline := c.HA.AuthPass != ""
	hasSource := c.HA.AuthPassFrom != nil

	switch {
	case hasInline && hasSource:
		return []error{errors.New("ha: set either authPass or authPassFrom, not both")}
	case !hasInline && !hasSource:
		return []error{errors.New(
			"ha.authPass: required when ha is enabled; it must be identical on every controller")}
	}

	if hasSource {
		return c.HA.AuthPassFrom.validate("ha.authPassFrom")
	}

	if len(c.HA.AuthPass) > keepalivedAuthPassLimit {
		return []error{fmt.Errorf(
			"ha.authPass: keepalived uses only the first %d characters, and yours is %d long; "+
				"shorten it so every controller agrees on the same value",
			keepalivedAuthPassLimit, len(c.HA.AuthPass))}
	}

	return nil
}

func (c *Config) validateUpgrades() []error {
	switch c.Upgrades.Automatic {
	case UpgradeNone, UpgradeDownload, UpgradeApply, "":
	default:
		return []error{fmt.Errorf(
			"upgrades.automatic: unknown value %q; use none, download or apply",
			c.Upgrades.Automatic)}
	}

	// A schedule with nothing to schedule is a setting that silently does
	// nothing, which is worth saying rather than ignoring.
	if c.Upgrades.Automatic == UpgradeNone &&
		c.Upgrades.Schedule != "" && c.Upgrades.Schedule != DefaultUpgradeSchedule {
		return []error{errors.New(
			"upgrades.schedule: set but upgrades.automatic is none, so nothing is scheduled")}
	}

	return nil
}

// onCalendarShorthands are the named schedules systemd.time(7) accepts in place
// of a full calendar expression.
var onCalendarShorthands = map[string]bool{
	"minutely":     true,
	"hourly":       true,
	"daily":        true,
	"weekly":       true,
	"monthly":      true,
	"quarterly":    true,
	"semiannually": true,
	"yearly":       true,
	"annually":     true,
}

// onCalendarPattern is the character set a systemd calendar expression is drawn
// from: weekday names, digits, and the separators between them.
var onCalendarPattern = regexp.MustCompile(`^[A-Za-z0-9*/,:.~+ -]+$`)

// validOnCalendar reports whether a schedule could plausibly be a systemd
// OnCalendar expression.
//
// It is a syntactic check and nothing more. Validation performs no network and
// no filesystem access and does not shell out, so `systemd-analyze calendar`,
// which is the only authority on this grammar, is not available to it — and a
// reimplementation of that grammar here would be a second parser to keep in
// step with systemd's.
//
// What it does catch is the mistake that actually happens: writing cron. A cron
// expression has no date separator and no time separator, so "0 3 * * *" lands
// here rather than in a timer that never fires. Weekday-only expressions such
// as "Mon,Fri" carry neither separator either, and are allowed through because
// they carry no digits.
func validOnCalendar(schedule string) bool {
	if onCalendarShorthands[strings.ToLower(schedule)] {
		return true
	}

	if !onCalendarPattern.MatchString(schedule) {
		return false
	}

	if strings.ContainsAny(schedule, "-:") {
		return true
	}

	return !strings.ContainsAny(schedule, "0123456789")
}

// validateBackup checks the scheduled backup block.
//
// The role check is the one worth being loud about. `k0s backup` reads the
// control plane's datastore and PKI, and refuses outright to run anywhere else,
// so a backup block on a plain worker describes a timer that would fail on
// every tick. Ignoring it would leave somebody believing a node is backed up.
func (c *Config) validateBackup() []error {
	// Nothing written, nothing to check. ApplyDefaults only fills this block in
	// once it is enabled, so an untouched configuration reaches here zero.
	if c.Backup == (Backup{}) {
		return nil
	}

	var problems []error

	// An empty role is a node waiting to be told what it is; the document that
	// answers is validated in its turn, and this check belongs to that one.
	if c.Role != "" && !c.Role.IsController() {
		problems = append(problems, fmt.Errorf(
			"backup: role %q runs no control plane, so there is nothing for "+
				"`k0s backup` to snapshot; backups belong on a controller",
			c.Role))
	}

	// A configured backup that is switched off is a setting that silently does
	// nothing, which is worth saying rather than ignoring -- the same reading
	// upgrades.schedule gets.
	if !c.Backup.Enabled {
		problems = append(problems, errors.New(
			"backup: configured but backup.enabled is false, so nothing is scheduled"))
	}

	switch {
	case c.Backup.Path == "":
		problems = append(problems, errors.New("backup.path: required"))
	case !strings.HasPrefix(c.Backup.Path, "/"):
		problems = append(problems, fmt.Errorf(
			"backup.path: %q must be an absolute path", c.Backup.Path))
	case strings.ContainsAny(c.Backup.Path, "\"\\\n"):
		// The path is written verbatim into a quoted systemd Environment=
		// line. Rejecting the three characters that could break out of it is
		// cheaper than escaping them, and no real backup target needs one.
		problems = append(problems, fmt.Errorf(
			"backup.path: %q must not contain a quote, a backslash or a newline",
			c.Backup.Path))
	}

	if c.Backup.Schedule != "" && !validOnCalendar(c.Backup.Schedule) {
		problems = append(problems, fmt.Errorf(
			"backup.schedule: %q is not a systemd OnCalendar expression; "+
				"use daily, weekly, or a calendar spec such as \"*-*-* 03:00:00\"",
			c.Backup.Schedule))
	}

	if c.Backup.Keep < 0 {
		problems = append(problems, fmt.Errorf(
			"backup.keep: %d must not be negative", c.Backup.Keep))
	}

	return problems
}

// raidNamePattern keeps an array name usable as a device node under /dev/md/.
var raidNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// raidMinimumDevices is the smallest number of members each RAID level can be
// built from. mdadm will refuse anything below these, but it refuses at first
// boot on a machine nobody is watching, so catch it here instead.
var raidMinimumDevices = map[int]int{
	0:  2,
	1:  2,
	5:  3,
	6:  4,
	10: 4,
}

func (c *Config) validateRAID() []error {
	var problems []error

	// Two arrays sharing a name would collide on /dev/md/<name>, and a device
	// claimed twice would be pulled into whichever array is built first and
	// silently corrupt the other.
	seenNames := make(map[string]bool, len(c.RAID))
	seenDevices := make(map[string]string, len(c.RAID))

	for i, array := range c.RAID {
		field := fmt.Sprintf("raid[%d]", i)

		if array.Name == "" {
			problems = append(problems, fmt.Errorf("%s.name: required", field))
		} else {
			if !raidNamePattern.MatchString(array.Name) {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q must be letters, digits, dashes or underscores",
					field, array.Name))
			}

			if seenNames[array.Name] {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q is used by more than one array", field, array.Name))
			}

			seenNames[array.Name] = true
		}

		problems = append(problems, validateRAIDLevel(field, array)...)
		problems = append(problems, validateRAIDDevices(field, array, seenDevices)...)
		problems = append(problems, validateFilesystem(field, array.Filesystem, array.MountPoint)...)
	}

	return problems
}

func validateRAIDLevel(field string, array RAIDArray) []error {
	minimum, ok := raidMinimumDevices[array.Level]
	if !ok {
		return []error{fmt.Errorf(
			"%s.level: unsupported level %d; use 0, 1, 5, 6 or 10", field, array.Level)}
	}

	if len(array.Devices) < minimum {
		return []error{fmt.Errorf(
			"%s.devices: RAID %d needs at least %d devices, got %d",
			field, array.Level, minimum, len(array.Devices))}
	}

	// A spare cannot be rebuilt into a stripe, so mdadm takes it but it never
	// does anything. Say so rather than letting someone believe they have a
	// safety margin they do not have.
	if array.Level == 0 && len(array.Spares) > 0 {
		return []error{fmt.Errorf(
			"%s.spares: RAID 0 has no redundancy, so a spare can never be rebuilt into it", field)}
	}

	return nil
}

func validateRAIDDevices(field string, array RAIDArray, seen map[string]string) []error {
	var problems []error

	for _, device := range append(append([]string{}, array.Devices...), array.Spares...) {
		if !strings.HasPrefix(device, "/dev/") {
			problems = append(problems, fmt.Errorf(
				"%s.devices: %q must be an absolute device path under /dev", field, device))

			continue
		}

		if owner, taken := seen[device]; taken {
			problems = append(problems, fmt.Errorf(
				"%s.devices: %q is already claimed by %s", field, device, owner))

			continue
		}

		seen[device] = field
	}

	return problems
}

// validateFilesystem checks the filesystem and mount point pair that raid[] and
// luks[] share: both lay an ordinary filesystem on a block device Corium
// produced, and both accept "none" for a workload that wants the raw device.
func validateFilesystem(field, filesystem, mountPoint string) []error {
	var problems []error

	switch filesystem {
	case "", RAIDFilesystemExt4, RAIDFilesystemXFS:
	case RAIDFilesystemNone:
		// An unformatted device cannot be mounted, and quietly ignoring the
		// mount point would leave someone waiting for a filesystem that is
		// never going to appear there.
		if mountPoint != "" {
			problems = append(problems, fmt.Errorf(
				"%s.mountPoint: set but filesystem is none, so there is nothing to mount", field))
		}
	default:
		problems = append(problems, fmt.Errorf(
			"%s.filesystem: unknown value %q; use ext4, xfs or none", field, filesystem))
	}

	if mountPoint != "" && !strings.HasPrefix(mountPoint, "/") {
		problems = append(problems, fmt.Errorf(
			"%s.mountPoint: %q must be an absolute path", field, mountPoint))
	}

	return problems
}

// zfsPoolNamePattern keeps a pool name usable: zpool wants a name that starts
// with a letter, and Corium additionally keeps it to characters that will not
// need quoting on a command line.
var zfsPoolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)

// zfsDatasetNamePattern is the same idea for a dataset name relative to its
// pool, where a slash is allowed because it builds the dataset hierarchy.
var zfsDatasetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)

// zfsVdevMinimumDevices is the smallest number of devices each vdev type can be
// built from. zpool refuses fewer, but it refuses at first boot on a machine
// nobody is watching, so catch it here instead.
var zfsVdevMinimumDevices = map[string]int{
	ZFSVdevStripe: 1,
	ZFSVdevMirror: 2,
	ZFSVdevRAIDZ1: 2,
	ZFSVdevRAIDZ2: 3,
	ZFSVdevRAIDZ3: 4,
}

func (c *Config) validateZFS() []error {
	var problems []error

	seenNames := make(map[string]bool, len(c.ZFS))

	// A device may belong to exactly one thing. Seed the map with everything
	// RAID already claimed, so a disk listed in both a pool and an array is
	// caught here rather than fought over at first boot, where whichever runs
	// first wins and corrupts the other.
	seenDevices := make(map[string]string)

	for i, array := range c.RAID {
		for _, device := range append(append([]string{}, array.Devices...), array.Spares...) {
			seenDevices[device] = fmt.Sprintf("raid[%d]", i)
		}
	}

	for i, pool := range c.ZFS {
		field := fmt.Sprintf("zfs[%d]", i)

		if pool.Name == "" {
			problems = append(problems, fmt.Errorf("%s.name: required", field))
		} else {
			if !zfsPoolNamePattern.MatchString(pool.Name) {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q must start with a letter and use only letters, digits, or _.:-",
					field, pool.Name))
			}

			if seenNames[pool.Name] {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q is used by more than one pool", field, pool.Name))
			}

			seenNames[pool.Name] = true
		}

		problems = append(problems, validateZFSVdevs(field, pool, seenDevices)...)
		problems = append(problems, validateZFSMountPoint(field+".mountPoint", pool.MountPoint)...)
		problems = append(problems, validateZFSDatasets(field, pool)...)
	}

	return problems
}

func validateZFSVdevs(field string, pool ZFSPool, seen map[string]string) []error {
	var problems []error

	if len(pool.Vdevs) == 0 {
		problems = append(problems, fmt.Errorf("%s.vdevs: at least one vdev is required", field))
	}

	for j, vdev := range pool.Vdevs {
		vfield := fmt.Sprintf("%s.vdevs[%d]", field, j)

		vdevType := vdev.Type
		if vdevType == "" {
			vdevType = ZFSVdevStripe
		}

		if minimum, ok := zfsVdevMinimumDevices[vdevType]; !ok {
			problems = append(problems, fmt.Errorf(
				"%s.type: unknown value %q; use stripe, mirror, raidz, raidz2 or raidz3",
				vfield, vdev.Type))
		} else if len(vdev.Devices) < minimum {
			problems = append(problems, fmt.Errorf(
				"%s.devices: a %s vdev needs at least %d devices, got %d",
				vfield, vdevType, minimum, len(vdev.Devices)))
		}

		for _, device := range vdev.Devices {
			if !strings.HasPrefix(device, "/dev/") {
				problems = append(problems, fmt.Errorf(
					"%s.devices: %q must be an absolute device path under /dev", vfield, device))

				continue
			}

			if owner, taken := seen[device]; taken {
				problems = append(problems, fmt.Errorf(
					"%s.devices: %q is already claimed by %s", vfield, device, owner))

				continue
			}

			seen[device] = vfield
		}
	}

	return problems
}

func validateZFSDatasets(field string, pool ZFSPool) []error {
	var problems []error

	seen := make(map[string]bool, len(pool.Datasets))

	for k, dataset := range pool.Datasets {
		dfield := fmt.Sprintf("%s.datasets[%d]", field, k)

		if dataset.Name == "" {
			problems = append(problems, fmt.Errorf("%s.name: required", dfield))
		} else {
			if !zfsDatasetNamePattern.MatchString(dataset.Name) {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q must be a dataset name relative to the pool", dfield, dataset.Name))
			}

			if seen[dataset.Name] {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q is declared more than once", dfield, dataset.Name))
			}

			seen[dataset.Name] = true
		}

		problems = append(problems, validateZFSMountPoint(dfield+".mountPoint", dataset.MountPoint)...)
	}

	return problems
}

// validateZFSMountPoint accepts an absolute path or ZFS's own special values,
// which is what distinguishes it from the RAID mount point: "none" and "legacy"
// are meaningful to ZFS and must not be rejected as non-absolute paths.
func validateZFSMountPoint(field, mountPoint string) []error {
	switch mountPoint {
	case "", "none", "legacy":
		return nil
	}

	if !strings.HasPrefix(mountPoint, "/") {
		return []error{fmt.Errorf(
			"%s: %q must be an absolute path, or \"none\" or \"legacy\"", field, mountPoint)}
	}

	return nil
}

// luksVolumeNamePattern keeps a volume name usable as a device-mapper name and
// as the first field of a crypttab line. Deliberately the same pattern raid[]
// uses: both end up as a name under /dev, and two rules for one constraint is
// one rule too many.
var luksVolumeNamePattern = raidNamePattern

// raidArrayDevicePrefix is where mdadm publishes an array Corium built. A luks[]
// volume may name one, because arrays are assembled before volumes are unlocked.
const raidArrayDevicePrefix = "/dev/md/"

func (c *Config) validateLUKS() []error {
	var problems []error

	seenNames := make(map[string]bool, len(c.LUKS))

	// One claim space across raid[], zfs[] and luks[]. A disk named by two of
	// them is a disk whichever ran first at boot has already destroyed for the
	// other, and the survivor's error message will not say so. validateZFS
	// seeds itself from raid[] for the same reason; this is the third corner of
	// the same triangle.
	seenDevices := c.claimedDiskDevices()

	arrays := make(map[string]RAIDArray, len(c.RAID))
	for _, array := range c.RAID {
		arrays[array.Name] = array
	}

	for i, volume := range c.LUKS {
		field := fmt.Sprintf("luks[%d]", i)

		if volume.Name == "" {
			problems = append(problems, fmt.Errorf("%s.name: required", field))
		} else {
			if !luksVolumeNamePattern.MatchString(volume.Name) {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q must be letters, digits, dashes or underscores",
					field, volume.Name))
			}

			if seenNames[volume.Name] {
				problems = append(problems, fmt.Errorf(
					"%s.name: %q is used by more than one volume", field, volume.Name))
			}

			seenNames[volume.Name] = true
		}

		problems = append(problems, validateLUKSDevice(field, volume, seenDevices, arrays)...)
		problems = append(problems, validateLUKSUnlock(field, volume)...)
		problems = append(problems, validateFilesystem(field, volume.Filesystem, volume.MountPoint)...)
	}

	return problems
}

// claimedDiskDevices maps every device raid[] and zfs[] have already spoken for
// to the field that claimed it.
func (c *Config) claimedDiskDevices() map[string]string {
	claimed := make(map[string]string)

	for i, array := range c.RAID {
		for _, device := range append(append([]string{}, array.Devices...), array.Spares...) {
			claimed[device] = fmt.Sprintf("raid[%d]", i)
		}
	}

	for i, pool := range c.ZFS {
		for j, vdev := range pool.Vdevs {
			for _, device := range vdev.Devices {
				claimed[device] = fmt.Sprintf("zfs[%d].vdevs[%d]", i, j)
			}
		}
	}

	return claimed
}

func validateLUKSDevice(
	field string,
	volume LUKSVolume,
	seen map[string]string,
	arrays map[string]RAIDArray,
) []error {
	if volume.Device == "" {
		return []error{fmt.Errorf("%s.device: required", field)}
	}

	if !strings.HasPrefix(volume.Device, "/dev/") {
		return []error{fmt.Errorf(
			"%s.device: %q must be an absolute device path under /dev", field, volume.Device)}
	}

	if owner, taken := seen[volume.Device]; taken {
		return []error{fmt.Errorf(
			"%s.device: %q is already claimed by %s", field, volume.Device, owner)}
	}

	seen[volume.Device] = field

	// A volume on top of one of this node's own arrays is allowed, and useful --
	// redundant storage that is also encrypted -- but only if the array was left
	// unformatted. Otherwise raid[] lays a filesystem on it and luks[] then
	// refuses to overwrite that filesystem, which is a first-boot failure the
	// operator can be told about now instead.
	name, onArray := strings.CutPrefix(volume.Device, raidArrayDevicePrefix)
	if !onArray {
		return nil
	}

	if array, declared := arrays[name]; declared && array.Filesystem != RAIDFilesystemNone {
		return []error{fmt.Errorf(
			"%s.device: raid array %q is formatted, so encrypting it would have to "+
				"overwrite that filesystem; set filesystem: none on the array and put "+
				"the filesystem on the volume instead", field, name)}
	}

	return nil
}

func validateLUKSUnlock(field string, volume LUKSVolume) []error {
	var problems []error

	hasInline := volume.Passphrase != ""
	hasSource := volume.PassphraseFrom != nil

	switch volume.Unlock {
	case "", LUKSUnlockTPM2:
		// A passphrase that would never be used is a passphrase somebody
		// believes is protecting something. Refuse it rather than ignore it.
		if hasInline || hasSource {
			problems = append(problems, fmt.Errorf(
				"%s: unlock is tpm2, so the passphrase would never be used; "+
					"remove it, or set unlock: passphrase", field))
		}
	case LUKSUnlockPassphrase:
		switch {
		case hasInline && hasSource:
			problems = append(problems, fmt.Errorf(
				"%s: set either passphrase or passphraseFrom, not both", field))
		case !hasInline && !hasSource:
			problems = append(problems, fmt.Errorf(
				"%s: unlock is passphrase, so set passphrase or passphraseFrom", field))
		}
	default:
		problems = append(problems, fmt.Errorf(
			"%s.unlock: unknown value %q; use tpm2 or passphrase", field, volume.Unlock))
	}

	if hasSource {
		problems = append(problems, volume.PassphraseFrom.validate(field+".passphraseFrom")...)
	}

	return problems
}

func (c *Config) validateAPI() []error {
	var problems []error

	hasInline := c.API.OperatorCA != ""
	hasSource := c.API.OperatorCAFrom != nil

	if hasInline && hasSource {
		problems = append(problems, errors.New(
			"api: set either operatorCA or operatorCAFrom, not both"))
	}

	// Naming the CA that owns a node and refusing to run the daemon are
	// contradictory statements. Picking a winner would mean one of them
	// silently does nothing, which is exactly the class of mistake that costs
	// an operator a reboot cycle to find.
	if c.API.Enabled != nil && !*c.API.Enabled && (hasInline || hasSource) {
		problems = append(problems, errors.New(
			"api: enabled is false but an operator CA is set; remove one of them, "+
				"since a node cannot both refuse the API and name its owner"))
	}

	// Insecure only has meaning while a node is waiting to be claimed. With a
	// CA configured the node was never unclaimed, and with the API off there
	// is nothing to open -- in both cases the key would silently do nothing,
	// which is the mistake that costs an operator a reboot cycle to find.
	if c.API.Insecure && c.API.Mode() != APIModeMaintenance {
		problems = append(problems, errors.New(
			"api.insecure: only applies to maintenance mode, and this node is not "+
				"waiting to be claimed; remove it, or remove the operator CA"))
	}

	// Waiting for a configuration that can only arrive over an API the node
	// will not run is a node that waits for ever, with nothing to tell an
	// operator why.
	if c.API.AwaitConfig && c.API.Mode() == APIModeDisabled {
		problems = append(problems, errors.New(
			"api.awaitConfig: the API is off, so the configuration this node "+
				"would be waiting for could never reach it; set api.enabled "+
				"or an operator CA"))
	}

	if hasSource {
		problems = append(problems, c.API.OperatorCAFrom.validate("api.operatorCAFrom")...)
	}

	if hasInline {
		problems = append(problems, validateOperatorCA(c.API.OperatorCA)...)
	}

	return problems
}

// validateOperatorCA checks an inline operator CA, reporting against the key
// it was written under.
//
// This is worth doing early. The value is pasted by hand or templated by a
// provisioning tool, and the failure mode of getting it wrong is a node that
// boots, serves TLS, and rejects every operator that talks to it -- which
// looks like a networking problem for as long as it takes somebody to check
// the certificate.
func validateOperatorCA(pemData string) []error {
	_, err := ParseOperatorCA([]byte(pemData))
	if err == nil {
		return nil
	}

	// The one problem worth more than a restatement: a key here has been
	// written into a document that ends up in instance metadata, and saying
	// only "wrong type" would let somebody fix the line and move on.
	if errors.Is(err, ErrPrivateKey) {
		return []error{fmt.Errorf(
			"api.operatorCA: %w -- a node is given the CA certificate and never its "+
				"key. Treat the key you just put in a configuration as compromised", err)}
	}

	return []error{fmt.Errorf("api.operatorCA: %w", err)}
}

// wgInterfaceNamePattern is what the kernel accepts for an interface name.
var wgInterfaceNamePattern = regexp.MustCompile(`^[a-z0-9][-a-z0-9]*$`)

// wgInterfaceNameMax is IFNAMSIZ minus the trailing NUL: the longest name the
// kernel will take for a network interface.
const wgInterfaceNameMax = 15

// wireGuardKeyBytes is the length a Curve25519 key decodes to. A base64 string
// that decodes to anything else is a truncated or mistyped key.
const wireGuardKeyBytes = 32

func (c *Config) validateWireGuard() []error {
	var problems []error

	// Two interfaces sharing a name would collide on the same device and the
	// same wg-quick unit instance.
	seenNames := make(map[string]bool, len(c.WireGuard))
	nodeAddresses := 0

	for i := range c.WireGuard {
		iface := &c.WireGuard[i]
		field := fmt.Sprintf("wireguard[%d]", i)

		problems = append(problems, validateWireGuardName(field, iface.Name, seenNames)...)

		if len(iface.Address) == 0 {
			problems = append(problems, fmt.Errorf("%s.address: required", field))
		} else {
			for k, addr := range iface.Address {
				if _, err := netip.ParsePrefix(addr); err != nil {
					problems = append(problems, fmt.Errorf(
						"%s.address[%d]: %q must be an address with a prefix length, such as 10.10.0.2/24",
						field, k, addr))
				}
			}
		}

		if iface.ListenPort != 0 && (iface.ListenPort < 1 || iface.ListenPort > 65535) {
			problems = append(problems, fmt.Errorf(
				"%s.listenPort: %d is out of range; use 1-65535, or omit it on a client-only node",
				field, iface.ListenPort))
		}

		if iface.MTU != 0 && (iface.MTU < 576 || iface.MTU > 65535) {
			problems = append(problems, fmt.Errorf(
				"%s.mtu: %d is out of range; use 576-65535, or omit it to let wg-quick decide",
				field, iface.MTU))
		}

		if iface.NodeAddress {
			nodeAddresses++
		}

		problems = append(problems, validateWireGuardKey(
			field, "privateKey", iface.PrivateKey, iface.PrivateKeyFrom, true)...)

		problems = append(problems, validateWireGuardPeers(field, iface)...)
	}

	// The kubelet registers exactly one address; two interfaces both claiming to
	// be it is a contradiction, not a preference to resolve.
	if nodeAddresses > 1 {
		problems = append(problems, errors.New(
			"wireguard: more than one interface sets nodeAddress, but a node registers exactly one address"))
	}

	return problems
}

func validateWireGuardName(field, name string, seen map[string]bool) []error {
	switch {
	case name == "":
		return []error{fmt.Errorf("%s.name: required", field)}
	case len(name) > wgInterfaceNameMax:
		return []error{fmt.Errorf(
			"%s.name: %q is %d characters, the limit is %d",
			field, name, len(name), wgInterfaceNameMax)}
	case !wgInterfaceNamePattern.MatchString(name):
		return []error{fmt.Errorf(
			"%s.name: %q must be lowercase letters, digits and hyphens, starting with a letter or digit",
			field, name)}
	}

	var problems []error
	if seen[name] {
		problems = append(problems, fmt.Errorf(
			"%s.name: %q is used by more than one interface", field, name))
	}

	seen[name] = true

	return problems
}

func validateWireGuardPeers(field string, iface *WireGuardInterface) []error {
	var problems []error

	seenKeys := make(map[string]bool, len(iface.Peers))
	var ranges []netip.Prefix

	for j := range iface.Peers {
		peer := &iface.Peers[j]
		pfield := fmt.Sprintf("%s.peers[%d]", field, j)

		if peer.PublicKey == "" {
			problems = append(problems, fmt.Errorf("%s.publicKey: required", pfield))
		} else if err := validWireGuardKeyMaterial(peer.PublicKey); err != nil {
			problems = append(problems, fmt.Errorf("%s.publicKey: %w", pfield, err))
		} else {
			if seenKeys[peer.PublicKey] {
				problems = append(problems, fmt.Errorf(
					"%s.publicKey: this key is used by more than one peer of the interface", pfield))
			}

			seenKeys[peer.PublicKey] = true
		}

		if peer.Endpoint != "" {
			if _, _, err := net.SplitHostPort(peer.Endpoint); err != nil {
				problems = append(problems, fmt.Errorf(
					"%s.endpoint: %q must be host:port", pfield, peer.Endpoint))
			}
		}

		if len(peer.AllowedIPs) == 0 {
			problems = append(problems, fmt.Errorf(
				"%s.allowedIPs: required; list the ranges routed to this peer", pfield))
		}

		for k, cidr := range peer.AllowedIPs {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil {
				problems = append(problems, fmt.Errorf(
					"%s.allowedIPs[%d]: %q is not a valid CIDR", pfield, k, cidr))

				continue
			}

			// WireGuard routes a packet to the peer with the longest matching
			// prefix, so two peers claiming the same range is a routing ambiguity
			// rather than a configuration two people can both have meant.
			for _, existing := range ranges {
				if existing.Overlaps(prefix) {
					problems = append(problems, fmt.Errorf(
						"%s.allowedIPs[%d]: %s overlaps another allowedIPs range on this interface",
						pfield, k, prefix))

					break
				}
			}

			ranges = append(ranges, prefix)
		}

		if peer.PersistentKeepalive != 0 && (peer.PersistentKeepalive < 1 || peer.PersistentKeepalive > 65535) {
			problems = append(problems, fmt.Errorf(
				"%s.persistentKeepalive: %d is out of range; use 1-65535 seconds",
				pfield, peer.PersistentKeepalive))
		}

		problems = append(problems, validateWireGuardKey(
			pfield, "presharedKey", peer.PresharedKey, peer.PresharedKeyFrom, false)...)
	}

	return problems
}

// validateWireGuardKey checks a base64 WireGuard key given inline or by
// reference, reporting against the field it was reached through. required says
// whether the key must be present: an interface must have a private key, a
// peer's preshared key is optional.
//
// The key value is never echoed into an error, because a private key is a secret
// and a validation message ends up in the journal.
func validateWireGuardKey(field, name, inline string, from *SecretSource, required bool) []error {
	var problems []error

	hasInline := inline != ""
	hasSource := from != nil

	switch {
	case hasInline && hasSource:
		problems = append(problems, fmt.Errorf(
			"%s: set either %s or %sFrom, not both", field, name, name))
	case !hasInline && !hasSource:
		if required {
			problems = append(problems, fmt.Errorf(
				"%s.%s: required; set %s or %sFrom", field, name, name, name))
		}
	}

	if hasSource {
		problems = append(problems, from.validate(field+"."+name+"From")...)
	}

	if hasInline {
		if err := validWireGuardKeyMaterial(inline); err != nil {
			problems = append(problems, fmt.Errorf("%s.%s: %w", field, name, err))
		}
	}

	return problems
}

// validWireGuardKeyMaterial reports whether a string is a base64-encoded 32-byte
// key, so a truncated paste fails at validation rather than at wg runtime. It
// never includes the key in its error.
func validWireGuardKeyMaterial(key string) error {
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return errors.New("must be a base64-encoded key")
	}

	if len(decoded) != wireGuardKeyBytes {
		return fmt.Errorf("must be a %d-byte key, but decodes to %d bytes",
			wireGuardKeyBytes, len(decoded))
	}

	return nil
}
