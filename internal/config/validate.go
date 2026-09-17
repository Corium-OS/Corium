package config

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
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
	problems = append(problems, c.validateRAID()...)
	problems = append(problems, c.validateAPI()...)

	return errors.Join(problems...)
}

func (c *Config) validateRole() []error {
	switch c.Role {
	case RoleSingle, RoleController, RoleControllerWorker, RoleWorker:
		return nil
	case "":
		return []error{errors.New("role: required (single, controller, controller+worker or worker)")}
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
		problems = append(problems, validateRAIDFilesystem(field, array)...)
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

func validateRAIDFilesystem(field string, array RAIDArray) []error {
	var problems []error

	switch array.Filesystem {
	case "", RAIDFilesystemExt4, RAIDFilesystemXFS:
	case RAIDFilesystemNone:
		// An unformatted array cannot be mounted, and quietly ignoring the
		// mount point would leave someone waiting for a filesystem that is
		// never going to appear there.
		if array.MountPoint != "" {
			problems = append(problems, fmt.Errorf(
				"%s.mountPoint: set but filesystem is none, so there is nothing to mount", field))
		}
	default:
		problems = append(problems, fmt.Errorf(
			"%s.filesystem: unknown value %q; use ext4, xfs or none", field, array.Filesystem))
	}

	if array.MountPoint != "" && !strings.HasPrefix(array.MountPoint, "/") {
		problems = append(problems, fmt.Errorf(
			"%s.mountPoint: %q must be an absolute path", field, array.MountPoint))
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

	if hasSource {
		problems = append(problems, c.API.OperatorCAFrom.validate("api.operatorCAFrom")...)
	}

	if hasInline {
		problems = append(problems, validateOperatorCA(c.API.OperatorCA)...)
	}

	return problems
}

// validateOperatorCA checks that an inline operator CA is what it claims to be.
//
// This is worth doing early. The value is pasted by hand or templated by a
// provisioning tool, and the failure mode of getting it wrong is a node that
// boots, serves TLS, and rejects every operator that talks to it -- which
// looks like a networking problem for as long as it takes somebody to check
// the certificate.
func validateOperatorCA(pemData string) []error {
	block, rest := pem.Decode([]byte(pemData))

	switch {
	case block == nil:
		return []error{errors.New(
			"api.operatorCA: not PEM data; expected a -----BEGIN CERTIFICATE----- block")}
	case block.Type != "CERTIFICATE":
		// A private key here is the mistake worth catching by name: it would
		// mean the operator has published the key that owns their whole fleet.
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return []error{fmt.Errorf(
				"api.operatorCA: this is a %s, not a certificate -- a node is given "+
					"the CA certificate and never its key. Treat the key you just "+
					"put in a configuration as compromised", block.Type)}
		}

		return []error{fmt.Errorf(
			"api.operatorCA: expected a CERTIFICATE block, got %q", block.Type)}
	case len(bytes.TrimSpace(rest)) > 0:
		// A chain would leave it ambiguous which certificate is the anchor,
		// and the answer decides who can manage the node.
		return []error{errors.New(
			"api.operatorCA: expected exactly one certificate, got more than one")}
	}

	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return []error{fmt.Errorf("api.operatorCA: %w", err)}
	}

	var problems []error

	if !certificate.IsCA {
		problems = append(problems, errors.New(
			"api.operatorCA: certificate is not a CA (basic constraints say CA:FALSE), "+
				"so it cannot sign the client certificates it is here to vouch for"))
	}

	if certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		problems = append(problems, errors.New(
			"api.operatorCA: certificate does not carry the certSign key usage"))
	}

	// Expiry is checked even though it makes validation depend on the clock.
	// An expired anchor produces a node nobody can manage, and finding that
	// out at first boot is the whole point of validating before mutating.
	if now := time.Now(); now.After(certificate.NotAfter) {
		problems = append(problems, fmt.Errorf(
			"api.operatorCA: certificate expired on %s", certificate.NotAfter.Format(time.RFC3339)))
	}

	return problems
}
