package config

import (
	"strings"
	"testing"
)

func TestValidateZFS(t *testing.T) {
	tests := []struct {
		name    string
		zfs     []ZFSPool
		raid    []RAIDArray
		wantErr string
	}{
		{
			name:    "missing name",
			zfs:     []ZFSPool{{Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}}}},
			wantErr: "zfs[0].name: required",
		},
		{
			name:    "invalid name",
			zfs:     []ZFSPool{{Name: "1tank", Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}}}},
			wantErr: "must start with a letter",
		},
		{
			name: "duplicate pool names",
			zfs: []ZFSPool{
				{Name: "tank", Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}}},
				{Name: "tank", Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdc"}}}},
			},
			wantErr: "used by more than one pool",
		},
		{
			name:    "no vdevs",
			zfs:     []ZFSPool{{Name: "tank"}},
			wantErr: "at least one vdev is required",
		},
		{
			name:    "unknown vdev type",
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Type: "raid5", Devices: []string{"/dev/sdb", "/dev/sdc"}}}}},
			wantErr: "unknown value",
		},
		{
			name:    "mirror with one device",
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Type: ZFSVdevMirror, Devices: []string{"/dev/sdb"}}}}},
			wantErr: "a mirror vdev needs at least 2 devices",
		},
		{
			name:    "raidz2 with two devices",
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Type: ZFSVdevRAIDZ2, Devices: []string{"/dev/sdb", "/dev/sdc"}}}}},
			wantErr: "a raidz2 vdev needs at least 3 devices",
		},
		{
			name:    "device not under /dev",
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Devices: []string{"sdb"}}}}},
			wantErr: "must be an absolute device path under /dev",
		},
		{
			name: "device claimed by two vdevs",
			zfs: []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{
				{Type: ZFSVdevMirror, Devices: []string{"/dev/sdb", "/dev/sdc"}},
				{Type: ZFSVdevMirror, Devices: []string{"/dev/sdc", "/dev/sdd"}},
			}}},
			wantErr: "is already claimed by",
		},
		{
			// A device in both a pool and an array would be fought over at first
			// boot, where whichever runs first wins and corrupts the other.
			name:    "device claimed by a raid array and a pool",
			raid:    []RAIDArray{{Name: "arr", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}}},
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Type: ZFSVdevMirror, Devices: []string{"/dev/sdb", "/dev/sdd"}}}}},
			wantErr: "already claimed by raid[0]",
		},
		{
			name: "pool mount point not absolute",
			zfs: []ZFSPool{{Name: "tank", MountPoint: "data",
				Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}}}},
			wantErr: "must be an absolute path",
		},
		{
			name: "dataset without a name",
			zfs: []ZFSPool{{Name: "tank",
				Vdevs:    []ZFSVdev{{Devices: []string{"/dev/sdb"}}},
				Datasets: []ZFSDataset{{MountPoint: "/var/lib/corium/data"}}}},
			wantErr: "datasets[0].name: required",
		},
		{
			name: "duplicate dataset names",
			zfs: []ZFSPool{{Name: "tank",
				Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}},
				Datasets: []ZFSDataset{
					{Name: "data"},
					{Name: "data"},
				}}},
			wantErr: "is declared more than once",
		},
		{
			name: "dataset mount point not absolute",
			zfs: []ZFSPool{{Name: "tank",
				Vdevs:    []ZFSVdev{{Devices: []string{"/dev/sdb"}}},
				Datasets: []ZFSDataset{{Name: "data", MountPoint: "relative"}}}},
			wantErr: "must be an absolute path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Role: RoleSingle, RAID: tc.raid, ZFS: tc.zfs}
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want one containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateZFSAcceptsAGoodPool(t *testing.T) {
	cfg := &Config{
		Role: RoleSingle,
		ZFS: []ZFSPool{{
			Name:              "tank",
			MountPoint:        "/var/lib/corium/data",
			Options:           map[string]string{"ashift": "12"},
			FilesystemOptions: map[string]string{"compression": "lz4"},
			Vdevs: []ZFSVdev{{
				Type:    ZFSVdevMirror,
				Devices: []string{"/dev/disk/by-id/wwn-0x1", "/dev/disk/by-id/wwn-0x2"},
			}},
			Datasets: []ZFSDataset{{
				Name:       "containerd",
				MountPoint: "/var/lib/corium/data/containerd",
				Properties: map[string]string{"recordsize": "1M"},
			}},
		}},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateZFSAcceptsSpecialMountPoints(t *testing.T) {
	for _, mountPoint := range []string{"none", "legacy"} {
		cfg := &Config{
			Role: RoleSingle,
			ZFS: []ZFSPool{{
				Name:       "tank",
				MountPoint: mountPoint,
				Vdevs:      []ZFSVdev{{Devices: []string{"/dev/sdb"}}},
				Datasets:   []ZFSDataset{{Name: "data", MountPoint: mountPoint}},
			}},
		}
		cfg.ApplyDefaults()

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with mountPoint %q = %v, want nil", mountPoint, err)
		}
	}
}
