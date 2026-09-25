package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// mdadmConfPath is regenerated from the assembled arrays rather than appended
// to, so that a node which is bootstrapped twice does not accumulate duplicate
// ARRAY lines describing the same device.
const mdadmConfPath = "/etc/mdadm.conf"

// fstabPath gains one entry per mounted array. A variable so that tests can
// exercise the rewriting against a real file instead of reimplementing it.
var fstabPath = "/etc/fstab"

// mountTarget is everything the mount path needs to know about a block device
// it did not create: which corium: field asked for it, what to call it, what
// filesystem it should carry, and where it goes.
//
// It exists because raid[] and luks[] both arrive at the same three steps --
// format if blank, record in fstab, mount -- with one word different, and a
// second copy of the fstab rewriting is a second place for it to go wrong.
type mountTarget struct {
	// Owner is the corium: field the device came from: "raid" or "luks". It
	// tags the fstab line and names the systemd drop-in, so the two fields own
	// separate lines and separate files rather than overwriting each other.
	Owner string

	// Name identifies the device within its field.
	Name string

	// Filesystem is the filesystem to create, empty meaning the default.
	Filesystem string

	// MountPoint is where it goes.
	MountPoint string
}

// marker tags the fstab lines this code owns, so they can be rewritten without
// disturbing anything an operator or the installer put in the same file.
func (m mountTarget) marker() string { return "# corium-" + m.Owner }

// arrayDevice is where mdadm publishes an array built with --homehost=any.
func arrayDevice(name string) string { return "/dev/md/" + name }

// applyRAID brings every declared array into existence and mounts it.
//
// This runs before k0s is installed, and that ordering is the feature. An array
// created after the kubelet has started is an array the kubelet has already
// written past: containerd puts its state on the root disk under the mount
// point, the array is then mounted over the top, and the data underneath is
// still on the root disk where nothing will ever look for it again. The disk
// fills up and the cause is invisible.
func applyRAID(ctx context.Context, cfg *config.Config) error {
	if len(cfg.RAID) == 0 {
		return nil
	}

	for i := range cfg.RAID {
		if err := applyArray(ctx, &cfg.RAID[i]); err != nil {
			return fmt.Errorf("raid %q: %w", cfg.RAID[i].Name, err)
		}
	}

	if err := writeMdadmConf(ctx); err != nil {
		return err
	}

	// k0s must not start on a node whose storage did not turn up. The mount
	// itself is nofail, so the machine still boots and can be logged into; this
	// is what stops Kubernetes writing to the empty directory underneath.
	return requireMountsForK0s(cfg, "raid", raidMountPoints(cfg))
}

// raidMountPoints lists the paths the declared arrays are mounted at.
func raidMountPoints(cfg *config.Config) []string {
	var mountPoints []string

	for _, array := range cfg.RAID {
		if array.MountPoint != "" {
			mountPoints = append(mountPoints, array.MountPoint)
		}
	}

	return mountPoints
}

func applyArray(ctx context.Context, array *config.RAIDArray) error {
	device := arrayDevice(array.Name)

	existing, err := arrayAssembled(ctx, device)
	if err != nil {
		return err
	}

	if existing {
		// Adopted rather than rebuilt. Re-running bootstrap on a node whose
		// array already exists must not destroy it.
		slog.Info("raid array already exists, adopting it",
			"name", array.Name, "device", device)
	} else {
		if err := createArray(ctx, array); err != nil {
			return err
		}
	}

	if array.Filesystem == config.RAIDFilesystemNone {
		return nil
	}

	target := mountTarget{
		Owner:      "raid",
		Name:       array.Name,
		Filesystem: array.Filesystem,
		MountPoint: array.MountPoint,
	}

	if err := ensureFilesystem(ctx, target, device); err != nil {
		return err
	}

	if array.MountPoint == "" {
		return nil
	}

	return mountDevice(ctx, target, device)
}

// createArray builds a new array, refusing to overwrite anything first.
func createArray(ctx context.Context, array *config.RAIDArray) error {
	members := append(append([]string{}, array.Devices...), array.Spares...)

	for _, member := range members {
		if err := assertUsable(ctx, member, array.Wipe); err != nil {
			return err
		}
	}

	if array.Wipe {
		for _, member := range members {
			slog.Warn("wiping device before building array",
				"device", member, "array", array.Name)

			if err := run(ctx, "wipefs", "--all", member); err != nil {
				return err
			}
		}
	}

	slog.Info("creating raid array",
		"name", array.Name, "level", array.Level,
		"devices", array.Devices, "spares", array.Spares)

	if err := run(ctx, "mdadm", mdadmCreateArgs(array)...); err != nil {
		return err
	}

	// The /dev/md/<name> symlink is created by udev, which has not necessarily
	// caught up by the time mdadm returns.
	return run(ctx, "udevadm", "settle")
}

// mdadmCreateArgs builds the array creation command.
//
// --homehost=any, so the array publishes itself as /dev/md/<name> on any
// machine rather than /dev/md/<hostname>:<name>. Disks pulled from a dead node
// and put into its replacement then assemble under the name they were given,
// instead of under the name of a machine that no longer exists.
//
// --run answers mdadm's "continue creating array?" prompt, which it asks on a
// level 1 array because metadata at the start of the device can confuse some
// firmware into seeing a bootable filesystem. Nothing here is a boot device.
func mdadmCreateArgs(array *config.RAIDArray) []string {
	args := []string{
		"--create", arrayDevice(array.Name),
		"--run",
		"--homehost=any",
		"--name=" + array.Name,
		"--metadata=1.2",
		fmt.Sprintf("--level=%d", array.Level),
		fmt.Sprintf("--raid-devices=%d", len(array.Devices)),
	}

	if len(array.Spares) > 0 {
		args = append(args, fmt.Sprintf("--spare-devices=%d", len(array.Spares)))
	}

	args = append(args, array.Devices...)
	args = append(args, array.Spares...)

	return args
}

// assertUsable refuses to build an array out of a device that already holds
// something.
//
// The whole failure mode worth engineering against here is destroying data that
// cannot be recovered. A node that stops with an error is an inconvenience; a
// node that silently consumed the disk someone had just restored a backup onto
// is not recoverable by any amount of care afterwards.
func assertUsable(ctx context.Context, device string, wipe bool) error {
	info, err := os.Stat(device)
	if err != nil {
		return fmt.Errorf("device %s: %w", device, err)
	}

	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("device %s: not a block device", device)
	}

	// blkid reports what is already on the device, and says "nothing here" by
	// exiting 2 rather than by printing nothing.
	//
	// Only that one exit code means blank. Treating every failure as blank
	// would turn a missing blkid, or a permissions problem, into a silent
	// decision to overwrite every disk on the machine -- which is the exact
	// outcome this function exists to prevent.
	output, err := commandOutput(ctx, "blkid", "--probe", "--output", "export", device)
	if err != nil {
		if blkidFoundNothing(err) {
			return nil
		}

		return fmt.Errorf("probing %s: %w", device, err)
	}

	if found := describeSignature(output); found != "" {
		if !wipe {
			return fmt.Errorf(
				"device %s already holds %s; refusing to overwrite it. "+
					"Set wipe: true on this array to consent to erasing these devices",
				device, found)
		}

		slog.Warn("device holds existing data and wipe is set",
			"device", device, "found", found)
	}

	return nil
}

// blkidNothingFound is the exit status blkid uses for "nothing detected on this
// device", as opposed to the failure statuses it uses for being unable to look.
const blkidNothingFound = 2

// blkidFoundNothing distinguishes a blank device from a failed probe.
func blkidFoundNothing(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}

	return exit.ExitCode() == blkidNothingFound
}

// describeSignature summarises what blkid found, or returns empty for a device
// with nothing on it.
func describeSignature(blkidExport string) string {
	// TYPE covers filesystems and, as linux_raid_member, membership of some
	// other array -- which is the case most worth catching, since consuming it
	// would destroy a second array as well as this device.
	interesting := map[string]string{
		"TYPE":   "a filesystem or array member signature",
		"PTTYPE": "a partition table",
	}

	var found []string

	for _, line := range strings.Split(blkidExport, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || value == "" {
			continue
		}

		if description, wanted := interesting[key]; wanted {
			found = append(found, fmt.Sprintf("%s (%s=%s)", description, key, value))
		}
	}

	sort.Strings(found)

	return strings.Join(found, " and ")
}

// ensureFilesystem formats the device, unless it already carries a filesystem.
func ensureFilesystem(ctx context.Context, target mountTarget, device string) error {
	// Same rule as assertUsable, and for a sharper reason: this runs against a
	// device that may have been adopted rather than just created, so mistaking
	// a failed probe for an empty device would reformat somebody's data.
	existing, err := commandOutput(ctx, "blkid", "--probe", "--output", "export", device)

	switch {
	case err == nil && strings.Contains(existing, "TYPE="):
		slog.Info("device already carries a filesystem, leaving it alone",
			"owner", target.Owner, "name", target.Name, "device", device)

		return nil
	case err != nil && !blkidFoundNothing(err):
		return fmt.Errorf("probing %s before formatting it: %w", device, err)
	}

	filesystem := target.filesystem()

	slog.Info("formatting device",
		"owner", target.Owner, "name", target.Name, "filesystem", filesystem)

	return run(ctx, "mkfs."+filesystem, mkfsArgs(filesystem, device)...)
}

// filesystem is the filesystem to create, with the default filled in. ext4
// because it is what the root filesystem is (see ADR 2), so a node needs no
// second filesystem driver to use its data disks.
func (m mountTarget) filesystem() string {
	if m.Filesystem == "" {
		return config.RAIDFilesystemExt4
	}

	return m.Filesystem
}

// mkfsArgs forces a format without prompting, which differs per filesystem.
func mkfsArgs(filesystem, device string) []string {
	if filesystem == config.RAIDFilesystemXFS {
		return []string{"-f", device}
	}

	return []string{"-F", device}
}

// mountDevice mounts the device now and records it for later boots.
func mountDevice(ctx context.Context, target mountTarget, device string) error {
	// 0755: a mount point has to be traversable by the services that will use
	// it, and once the device is mounted the filesystem's own root permissions
	// take over from these anyway.
	if err := os.MkdirAll(target.MountPoint, 0o755); err != nil { //nolint:gosec // mount points must be traversable
		return fmt.Errorf("creating %s: %w", target.MountPoint, err)
	}

	uuid, err := commandOutput(ctx, "blkid", "--match-tag", "UUID", "--output", "value", device)
	if err != nil {
		return fmt.Errorf("reading UUID of %s: %w", device, err)
	}

	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return fmt.Errorf("device %s reported no filesystem UUID", device)
	}

	if err := upsertFstab(target, uuid); err != nil {
		return err
	}

	// systemd generates mount units from fstab, so it has to be told the file
	// changed before the mount is asked for.
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	if mounted(target.MountPoint) {
		return nil
	}

	slog.Info("mounting device",
		"owner", target.Owner, "name", target.Name,
		"mountPoint", target.MountPoint, "uuid", uuid)

	return run(ctx, "mount", target.MountPoint)
}

// upsertFstab rewrites this device's entry, leaving every other line untouched.
func upsertFstab(target mountTarget, uuid string) error {
	existing, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", fstabPath, err)
	}

	tag := fmt.Sprintf("%s %s", target.marker(), target.Name)

	var kept []string

	for _, line := range strings.Split(string(existing), "\n") {
		if strings.Contains(line, tag) {
			continue
		}

		kept = append(kept, line)
	}

	// Trailing blank lines accumulate otherwise, one per bootstrap.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	kept = append(kept, fstabEntry(target, uuid), "")

	return os.WriteFile(fstabPath, []byte(strings.Join(kept, "\n")), 0o644) //nolint:gosec // fstab is world-readable by design
}

// fstabEntry renders the mount line.
//
// By UUID rather than by /dev/md/<name> or /dev/mapper/<name>: those names are
// labels mdadm and device-mapper choose to honour, the filesystem UUID is a
// property of the data.
//
// nofail, so a missing or degraded-beyond-recovery device does not leave the
// machine sitting at an emergency prompt where nobody can reach it. The
// protection against Kubernetes running without its storage is not here, it is
// the RequiresMountsFor drop-in on the k0s unit -- which fails the service
// loudly while leaving the node reachable enough to fix.
func fstabEntry(target mountTarget, uuid string) string {
	return fmt.Sprintf("UUID=%s %s %s defaults,nofail 0 2 %s %s",
		uuid, target.MountPoint, target.filesystem(), target.marker(), target.Name)
}

// requireMountsForK0s stops k0s starting before its storage is there.
//
// owner names the corium: field that asked for the mounts and picks the
// drop-in's filename, so raid[], zfs[] and luks[] each own one file instead of
// overwriting each other's.
func requireMountsForK0s(cfg *config.Config, owner string, mountPoints []string) error {
	if len(mountPoints) == 0 {
		return nil
	}

	unit := k0s.ServiceName(cfg.Role)
	dir := filepath.Join("/etc/systemd/system", unit+".d")

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // systemd unit drop-in directories are 0755
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	content := fmt.Sprintf(
		"# Written by corium-agent from corium.%s.\n"+
			"#\n"+
			"# Without this, a node whose storage did not turn up starts Kubernetes\n"+
			"# anyway and writes to the empty directory on the root disk that the\n"+
			"# mount should have covered.\n"+
			"[Unit]\n", owner)

	for _, mountPoint := range mountPoints {
		content += "RequiresMountsFor=" + mountPoint + "\n"
	}

	path := filepath.Join(dir, "10-corium-"+owner+".conf")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // unit drop-ins are world-readable
		return fmt.Errorf("writing %s: %w", path, err)
	}

	slog.Info("k0s will wait for the mounts",
		"owner", owner, "unit", unit, "mountPoints", mountPoints)

	return nil
}

// writeMdadmConf records the assembled arrays so they come back on later boots.
//
// Regenerated wholesale from what is currently assembled rather than appended
// to: appending on every bootstrap is how this file ends up with the same ARRAY
// line three times.
func writeMdadmConf(ctx context.Context) error {
	scanned, err := commandOutput(ctx, "mdadm", "--detail", "--scan")
	if err != nil {
		return err
	}

	content := "# Written by corium-agent from corium.raid.\n" +
		"# Regenerated on each bootstrap from `mdadm --detail --scan`.\n" +
		strings.TrimSpace(scanned) + "\n"

	if err := os.WriteFile(mdadmConfPath, []byte(content), 0o644); err != nil { //nolint:gosec // mdadm.conf is world-readable
		return fmt.Errorf("writing %s: %w", mdadmConfPath, err)
	}

	return nil
}

// arrayAssembled reports whether the array is already present.
func arrayAssembled(ctx context.Context, device string) (bool, error) {
	if _, err := os.Stat(device); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("checking %s: %w", device, err)
	}

	// Present in /dev is not the same as assembled and usable: a half-assembled
	// array still has a device node.
	if err := run(ctx, "mdadm", "--detail", "--test", device); err != nil {
		return false, fmt.Errorf("%s exists but is not a healthy array: %w", device, err)
	}

	return true, nil
}

// mounted reports whether anything is mounted at path.
func mounted(path string) bool {
	return exec.Command("mountpoint", "--quiet", path).Run() == nil
}

// commandOutput executes a command and returns its combined output.
//
// The package already has run() for commands whose output only matters when
// they fail; this is for the ones whose output is the point.
func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}

		return "", fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, trimmed)
	}

	return string(out), nil
}
