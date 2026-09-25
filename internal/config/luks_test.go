package config

import (
	"strings"
	"testing"
)

func TestValidateLUKS(t *testing.T) {
	tests := []struct {
		name    string
		luks    []LUKSVolume
		raid    []RAIDArray
		zfs     []ZFSPool
		wantErr string
	}{
		{
			name:    "missing name",
			luks:    []LUKSVolume{{Device: "/dev/sdb"}},
			wantErr: "luks[0].name: required",
		},
		{
			name:    "invalid name",
			luks:    []LUKSVolume{{Name: "data/one", Device: "/dev/sdb"}},
			wantErr: "must be letters, digits, dashes or underscores",
		},
		{
			name: "duplicate volume names",
			luks: []LUKSVolume{
				{Name: "data", Device: "/dev/sdb"},
				{Name: "data", Device: "/dev/sdc"},
			},
			wantErr: "used by more than one volume",
		},
		{
			name:    "missing device",
			luks:    []LUKSVolume{{Name: "data"}},
			wantErr: "luks[0].device: required",
		},
		{
			name:    "device not under /dev",
			luks:    []LUKSVolume{{Name: "data", Device: "sdb"}},
			wantErr: "must be an absolute device path under /dev",
		},
		{
			name: "device claimed by two volumes",
			luks: []LUKSVolume{
				{Name: "data", Device: "/dev/sdb"},
				{Name: "scratch", Device: "/dev/sdb"},
			},
			wantErr: "already claimed by luks[0]",
		},
		{
			// Whichever of the two ran first at boot would destroy the other's
			// disk, and its error message would not say so.
			name:    "device claimed by a raid array and a volume",
			raid:    []RAIDArray{{Name: "arr", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}}},
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb"}},
			wantErr: "already claimed by raid[0]",
		},
		{
			name:    "device claimed by a raid spare and a volume",
			raid:    []RAIDArray{{Name: "arr", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}, Spares: []string{"/dev/sdd"}}},
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdd"}},
			wantErr: "already claimed by raid[0]",
		},
		{
			name:    "device claimed by a zfs pool and a volume",
			zfs:     []ZFSPool{{Name: "tank", Vdevs: []ZFSVdev{{Devices: []string{"/dev/sdb"}}}}},
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb"}},
			wantErr: "already claimed by zfs[0].vdevs[0]",
		},
		{
			// raid[] would lay ext4 on the array and luks[] would then refuse to
			// overwrite it at first boot. Say so now instead.
			name:    "volume on a formatted raid array",
			raid:    []RAIDArray{{Name: "arr", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}, Filesystem: RAIDFilesystemExt4}},
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/md/arr"}},
			wantErr: "set filesystem: none on the array",
		},
		{
			name:    "passphrase set but unlock is tpm2",
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb", Passphrase: "hunter2"}},
			wantErr: "the passphrase would never be used",
		},
		{
			name: "passphrase source set but unlock is tpm2",
			luks: []LUKSVolume{{Name: "data", Device: "/dev/sdb",
				PassphraseFrom: &SecretSource{File: "/run/key"}}},
			wantErr: "the passphrase would never be used",
		},
		{
			name:    "passphrase unlock with neither passphrase nor source",
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb", Unlock: LUKSUnlockPassphrase}},
			wantErr: "set passphrase or passphraseFrom",
		},
		{
			name: "passphrase and passphraseFrom together",
			luks: []LUKSVolume{{Name: "data", Device: "/dev/sdb", Unlock: LUKSUnlockPassphrase,
				Passphrase: "hunter2", PassphraseFrom: &SecretSource{File: "/run/key"}}},
			wantErr: "not both",
		},
		{
			name:    "unknown unlock method",
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb", Unlock: "fido2"}},
			wantErr: "use tpm2 or passphrase",
		},
		{
			// The secret source is held to the same rules everywhere else: a
			// key fetched over plain HTTP is a key on the wire.
			name: "passphrase source over plain http",
			luks: []LUKSVolume{{Name: "data", Device: "/dev/sdb", Unlock: LUKSUnlockPassphrase,
				PassphraseFrom: &SecretSource{URL: "http://vault.example.com/key"}}},
			wantErr: "scheme must be https",
		},
		{
			name:    "unknown filesystem",
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb", Filesystem: "btrfs"}},
			wantErr: "use ext4, xfs or none",
		},
		{
			name: "mount point on an unformatted volume",
			luks: []LUKSVolume{{Name: "data", Device: "/dev/sdb",
				Filesystem: RAIDFilesystemNone, MountPoint: "/var/lib/corium/data"}},
			wantErr: "there is nothing to mount",
		},
		{
			name:    "mount point not absolute",
			luks:    []LUKSVolume{{Name: "data", Device: "/dev/sdb", MountPoint: "data"}},
			wantErr: "must be an absolute path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Role: RoleSingle, RAID: tc.raid, ZFS: tc.zfs, LUKS: tc.luks}
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

func TestValidateLUKSAcceptsGoodVolumes(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "tpm2 by default, on a whole disk",
			cfg: Config{Role: RoleSingle, LUKS: []LUKSVolume{{
				Name:       "data",
				Device:     "/dev/disk/by-id/wwn-0x1",
				MountPoint: "/var/lib/corium/data",
			}}},
		},
		{
			name: "a passphrase resolved as a secret",
			cfg: Config{Role: RoleSingle, LUKS: []LUKSVolume{{
				Name:           "data",
				Device:         "/dev/disk/by-id/wwn-0x1",
				Unlock:         LUKSUnlockPassphrase,
				PassphraseFrom: &SecretSource{URL: "https://vault.example.com/key", WaitFor: "10m"},
				Filesystem:     RAIDFilesystemXFS,
				MountPoint:     "/var/lib/corium/data",
			}}},
		},
		{
			name: "an unformatted volume with no mount point",
			cfg: Config{Role: RoleSingle, LUKS: []LUKSVolume{{
				Name:       "raw",
				Device:     "/dev/sdb",
				Filesystem: RAIDFilesystemNone,
			}}},
		},
		{
			// Encrypted redundant storage: the array carries no filesystem, so
			// the two do not format over each other.
			name: "a volume on top of an unformatted raid array",
			cfg: Config{
				Role: RoleSingle,
				RAID: []RAIDArray{{
					Name:       "mirror",
					Level:      1,
					Devices:    []string{"/dev/sdb", "/dev/sdc"},
					Filesystem: RAIDFilesystemNone,
				}},
				LUKS: []LUKSVolume{{
					Name:       "data",
					Device:     "/dev/md/mirror",
					MountPoint: "/var/lib/corium/data",
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.ApplyDefaults()

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}
