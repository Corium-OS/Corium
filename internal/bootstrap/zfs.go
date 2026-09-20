package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// applyZFS brings every declared pool into existence and mounts its datasets.
//
// Like applyRAID, this runs before k0s is installed, and the ordering is the
// feature. A pool imported after the kubelet has started is a mount the kubelet
// has already written past: containerd puts its state on the root disk under
// the mount point, the dataset is then mounted over the top, and the data
// underneath is stranded on the root disk where nothing will look again.
func applyZFS(ctx context.Context, cfg *config.Config) error {
	if len(cfg.ZFS) == 0 {
		return nil
	}

	// zpool and zfs need the kernel module. modules-load.d loads it at boot, but
	// bootstrap must not depend on that ordering having already happened, so load
	// it explicitly. modprobe is a no-op when the module is already in.
	if err := run(ctx, "modprobe", "zfs"); err != nil {
		return fmt.Errorf("loading the zfs kernel module: %w", err)
	}

	for i := range cfg.ZFS {
		if err := applyPool(ctx, &cfg.ZFS[i]); err != nil {
			return fmt.Errorf("zfs %q: %w", cfg.ZFS[i].Name, err)
		}
	}

	// k0s must not start on a node whose storage did not turn up. Same guarantee
	// as RAID, by the same mechanism.
	return requireZFSMountsForK0s(cfg)
}

func applyPool(ctx context.Context, pool *config.ZFSPool) error {
	exists, err := poolPresent(ctx, pool.Name)
	if err != nil {
		return err
	}

	if exists {
		// Adopted rather than rebuilt. Re-running bootstrap on a node whose pool
		// already exists must not destroy it.
		slog.Info("zfs pool already exists, adopting it", "name", pool.Name)
	} else if err := createPool(ctx, pool); err != nil {
		return err
	}

	return ensureDatasets(ctx, pool)
}

// poolPresent reports whether the pool is usable, importing it if it is on the
// disks but not yet imported.
//
// A pool a previous boot created is brought back by the zfs-import systemd
// units, but bootstrap must not assume those have already run, so it imports by
// name itself. A name that does not exist on any attached disk simply fails to
// import, which is reported as "not present" so the caller creates it.
func poolPresent(ctx context.Context, name string) (bool, error) {
	if err := run(ctx, "zpool", "list", name); err == nil {
		return true, nil
	}

	if err := run(ctx, "zpool", "import", name); err == nil {
		slog.Info("imported an existing zfs pool", "name", name)

		return true, nil
	}

	return false, nil
}

// createPool builds a new pool, refusing to overwrite anything first.
func createPool(ctx context.Context, pool *config.ZFSPool) error {
	devices := poolDevices(pool)

	// The same guard as RAID, and it is the whole point of the feature: a device
	// that already carries a filesystem, a partition table, or another pool's
	// or array's metadata stops the bootstrap rather than being consumed.
	for _, device := range devices {
		if err := assertUsable(ctx, device, pool.Wipe); err != nil {
			return err
		}
	}

	if pool.Wipe {
		for _, device := range devices {
			slog.Warn("wiping device before building pool",
				"device", device, "pool", pool.Name)

			if err := run(ctx, "wipefs", "--all", device); err != nil {
				return err
			}
		}
	}

	slog.Info("creating zfs pool", "name", pool.Name, "vdevs", len(pool.Vdevs))

	return run(ctx, "zpool", zpoolCreateArgs(pool)...)
}

// poolDevices flattens every device across the pool's vdevs.
func poolDevices(pool *config.ZFSPool) []string {
	var devices []string

	for _, vdev := range pool.Vdevs {
		devices = append(devices, vdev.Devices...)
	}

	return devices
}

// zpoolCreateArgs builds the pool creation command.
//
// -f forces past ZFS's own refusal to use a device that looks used. Corium has
// already made that decision in assertUsable, where the default is to stop and
// wipe: true is the consent; without -f a stale label left after a wipefs would
// still abort the create.
//
// -o cachefile=none keeps the pool out of a persisted import cache that would
// pin it to the machine that built it. Later boots reimport it by scanning the
// attached disks, which is what a node with a fixed set of disks -- and one that
// might be reprovisioned onto replacement hardware -- actually wants.
func zpoolCreateArgs(pool *config.ZFSPool) []string {
	args := []string{"create", "-f", "-o", "cachefile=none"}

	if pool.MountPoint != "" {
		args = append(args, "-m", pool.MountPoint)
	}

	for _, key := range sortedKeys(pool.Options) {
		args = append(args, "-o", key+"="+pool.Options[key])
	}

	for _, key := range sortedKeys(pool.FilesystemOptions) {
		args = append(args, "-O", key+"="+pool.FilesystemOptions[key])
	}

	args = append(args, pool.Name)

	for _, vdev := range pool.Vdevs {
		args = append(args, vdevArgs(vdev)...)
	}

	return args
}

// vdevArgs renders one vdev: its type keyword, then its devices. A plain stripe
// has no keyword -- a bare list of devices is what a stripe means to zpool -- so
// it is omitted, which also keeps the common single-disk case a bare device.
func vdevArgs(vdev config.ZFSVdev) []string {
	var args []string

	if vdev.Type != "" && vdev.Type != config.ZFSVdevStripe {
		args = append(args, vdev.Type)
	}

	return append(args, vdev.Devices...)
}

// ensureDatasets creates each declared dataset, or adopts and reconciles one
// that already exists.
func ensureDatasets(ctx context.Context, pool *config.ZFSPool) error {
	for i := range pool.Datasets {
		dataset := pool.Datasets[i]
		name := pool.Name + "/" + dataset.Name

		if datasetExists(ctx, name) {
			// Adopted, and its mount point and properties reapplied, so editing
			// them and re-bootstrapping converges rather than being ignored.
			slog.Info("zfs dataset already exists, adopting it", "dataset", name)

			if err := applyDatasetProperties(ctx, name, dataset); err != nil {
				return err
			}

			continue
		}

		slog.Info("creating zfs dataset", "dataset", name)

		if err := run(ctx, "zfs", zfsCreateArgs(pool.Name, dataset)...); err != nil {
			return err
		}
	}

	return nil
}

// datasetExists reports whether the dataset is already present.
func datasetExists(ctx context.Context, name string) bool {
	return run(ctx, "zfs", "list", name) == nil
}

// zfsCreateArgs builds the dataset creation command, setting its mount point and
// properties at creation so it never mounts at the inherited location first.
func zfsCreateArgs(pool string, dataset config.ZFSDataset) []string {
	args := []string{"create"}

	if dataset.MountPoint != "" {
		args = append(args, "-o", "mountpoint="+dataset.MountPoint)
	}

	for _, key := range sortedKeys(dataset.Properties) {
		args = append(args, "-o", key+"="+dataset.Properties[key])
	}

	return append(args, pool+"/"+dataset.Name)
}

// applyDatasetProperties reconciles an adopted dataset toward the configuration,
// setting each property the create path would have set.
func applyDatasetProperties(ctx context.Context, name string, dataset config.ZFSDataset) error {
	if dataset.MountPoint != "" {
		if err := run(ctx, "zfs", "set", "mountpoint="+dataset.MountPoint, name); err != nil {
			return err
		}
	}

	for _, key := range sortedKeys(dataset.Properties) {
		if err := run(ctx, "zfs", "set", key+"="+dataset.Properties[key], name); err != nil {
			return err
		}
	}

	return nil
}

// requireZFSMountsForK0s stops k0s starting before its storage is there.
//
// The same drop-in RAID writes, for the same reason: a pool that failed to
// import must fail the k0s unit loudly rather than let Kubernetes run against
// the empty directory on the root disk that the dataset should cover.
func requireZFSMountsForK0s(cfg *config.Config) error {
	var mountPoints []string

	for _, pool := range cfg.ZFS {
		if mp := poolMountPoint(pool); mp != "" {
			mountPoints = append(mountPoints, mp)
		}

		for _, dataset := range pool.Datasets {
			if isRealMountPoint(dataset.MountPoint) {
				mountPoints = append(mountPoints, dataset.MountPoint)
			}
		}
	}

	if len(mountPoints) == 0 {
		return nil
	}

	unit := k0s.ServiceName(cfg.Role)
	dir := filepath.Join("/etc/systemd/system", unit+".d")

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // systemd unit drop-in directories are 0755
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	content := "# Written by corium-agent from corium.zfs.\n" +
		"#\n" +
		"# Without this, a node whose pool failed to import starts Kubernetes\n" +
		"# anyway and writes to the empty directory on the root disk that the\n" +
		"# dataset should have been mounted over.\n" +
		"[Unit]\n"

	for _, mountPoint := range mountPoints {
		content += "RequiresMountsFor=" + mountPoint + "\n"
	}

	path := filepath.Join(dir, "10-corium-zfs.conf")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // unit drop-ins are world-readable
		return fmt.Errorf("writing %s: %w", path, err)
	}

	slog.Info("k0s will wait for the zfs mounts",
		"unit", unit, "mountPoints", mountPoints)

	return nil
}

// poolMountPoint is the path the pool's root dataset mounts at: its configured
// mount point, ZFS's /<name> default when none is given, or empty for a pool
// left unmounted with "none" or "legacy".
func poolMountPoint(pool config.ZFSPool) string {
	switch pool.MountPoint {
	case "none", "legacy":
		return ""
	case "":
		return "/" + pool.Name
	default:
		return pool.MountPoint
	}
}

// isRealMountPoint reports whether a mount point value names an actual path, as
// opposed to being unset or one of ZFS's "do not mount" sentinels.
func isRealMountPoint(mountPoint string) bool {
	switch mountPoint {
	case "", "none", "legacy":
		return false
	default:
		return strings.HasPrefix(mountPoint, "/")
	}
}

// sortedKeys returns a map's keys in a stable order, so a command built from a
// property map is deterministic and therefore testable.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))

	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
