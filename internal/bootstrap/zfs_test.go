package bootstrap

import (
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestZpoolCreateArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pool config.ZFSPool
		want []string
	}{
		{
			name: "single disk is a bare device with no vdev keyword",
			pool: config.ZFSPool{
				Name:  "tank",
				Vdevs: []config.ZFSVdev{{Devices: []string{"/dev/sdb"}}},
			},
			want: []string{
				"create", "-f", "-o", "cachefile=none",
				"tank",
				"/dev/sdb",
			},
		},
		{
			name: "mirror carries its type keyword",
			pool: config.ZFSPool{
				Name: "tank",
				Vdevs: []config.ZFSVdev{{
					Type:    config.ZFSVdevMirror,
					Devices: []string{"/dev/sdb", "/dev/sdc"},
				}},
			},
			want: []string{
				"create", "-f", "-o", "cachefile=none",
				"tank",
				"mirror", "/dev/sdb", "/dev/sdc",
			},
		},
		{
			// ashift is fixed for the life of the pool, and compression belongs
			// on the root dataset so every dataset inherits it: -o for the pool,
			// -O for the filesystem, both in sorted order so the command is
			// deterministic.
			name: "options and filesystem options and a mount point",
			pool: config.ZFSPool{
				Name:              "tank",
				MountPoint:        "/var/lib/corium/data",
				Options:           map[string]string{"ashift": "12"},
				FilesystemOptions: map[string]string{"compression": "lz4", "atime": "off"},
				Vdevs: []config.ZFSVdev{{
					Type:    config.ZFSVdevRAIDZ2,
					Devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd"},
				}},
			},
			want: []string{
				"create", "-f", "-o", "cachefile=none",
				"-m", "/var/lib/corium/data",
				"-o", "ashift=12",
				"-O", "atime=off",
				"-O", "compression=lz4",
				"tank",
				"raidz2", "/dev/sdb", "/dev/sdc", "/dev/sdd",
			},
		},
		{
			name: "two mirror vdevs stripe across both",
			pool: config.ZFSPool{
				Name: "tank",
				Vdevs: []config.ZFSVdev{
					{Type: config.ZFSVdevMirror, Devices: []string{"/dev/sdb", "/dev/sdc"}},
					{Type: config.ZFSVdevMirror, Devices: []string{"/dev/sdd", "/dev/sde"}},
				},
			},
			want: []string{
				"create", "-f", "-o", "cachefile=none",
				"tank",
				"mirror", "/dev/sdb", "/dev/sdc",
				"mirror", "/dev/sdd", "/dev/sde",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := zpoolCreateArgs(&test.pool)

			if strings.Join(got, " ") != strings.Join(test.want, " ") {
				t.Errorf("zpoolCreateArgs()\n got: %v\nwant: %v", got, test.want)
			}
		})
	}
}

func TestZfsCreateArgs(t *testing.T) {
	t.Parallel()

	dataset := config.ZFSDataset{
		Name:       "containerd",
		MountPoint: "/var/lib/corium/data",
		Properties: map[string]string{"recordsize": "1M", "compression": "zstd"},
	}

	got := zfsCreateArgs("tank", dataset)

	want := []string{
		"create",
		"-o", "mountpoint=/var/lib/corium/data",
		"-o", "compression=zstd",
		"-o", "recordsize=1M",
		"tank/containerd",
	}

	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("zfsCreateArgs()\n got: %v\nwant: %v", got, want)
	}
}

func TestVdevArgsOmitsTheStripeKeyword(t *testing.T) {
	t.Parallel()

	// A bare list of devices is what a stripe means to zpool, so the keyword
	// must not appear -- including for the default, empty type.
	for _, vdevType := range []string{"", config.ZFSVdevStripe} {
		got := vdevArgs(config.ZFSVdev{Type: vdevType, Devices: []string{"/dev/sdb"}})

		if strings.Join(got, " ") != "/dev/sdb" {
			t.Errorf("vdevArgs(type=%q) = %v, want just the device", vdevType, got)
		}
	}

	got := vdevArgs(config.ZFSVdev{Type: config.ZFSVdevRAIDZ1, Devices: []string{"/dev/sdb", "/dev/sdc"}})
	if got[0] != "raidz" {
		t.Errorf("vdevArgs() = %v, want it to lead with the raidz keyword", got)
	}
}

func TestPoolMountPoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pool config.ZFSPool
		want string
	}{
		{
			name: "default is ZFS's own /<name>",
			pool: config.ZFSPool{Name: "tank"},
			want: "/tank",
		},
		{
			name: "an explicit mount point is used as given",
			pool: config.ZFSPool{Name: "tank", MountPoint: "/var/lib/corium/data"},
			want: "/var/lib/corium/data",
		},
		{
			name: "none leaves the pool unmounted",
			pool: config.ZFSPool{Name: "tank", MountPoint: "none"},
			want: "",
		},
		{
			name: "legacy leaves the pool unmounted",
			pool: config.ZFSPool{Name: "tank", MountPoint: "legacy"},
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := poolMountPoint(test.pool); got != test.want {
				t.Errorf("poolMountPoint(%+v) = %q, want %q", test.pool, got, test.want)
			}
		})
	}
}

func TestIsRealMountPoint(t *testing.T) {
	t.Parallel()

	real := []string{"/var/lib/corium/data", "/tank"}
	notReal := []string{"", "none", "legacy", "relative/path"}

	for _, mountPoint := range real {
		if !isRealMountPoint(mountPoint) {
			t.Errorf("isRealMountPoint(%q) = false, want true", mountPoint)
		}
	}

	for _, mountPoint := range notReal {
		if isRealMountPoint(mountPoint) {
			t.Errorf("isRealMountPoint(%q) = true, want false", mountPoint)
		}
	}
}
