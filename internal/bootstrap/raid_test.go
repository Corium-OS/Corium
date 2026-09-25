package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestMdadmCreateArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		array config.RAIDArray
		want  []string
	}{
		{
			name: "mirror of two disks",
			array: config.RAIDArray{
				Name:    "data",
				Level:   1,
				Devices: []string{"/dev/sdb", "/dev/sdc"},
			},
			want: []string{
				"--create", "/dev/md/data",
				"--run",
				"--homehost=any",
				"--name=data",
				"--metadata=1.2",
				"--level=1",
				"--raid-devices=2",
				"/dev/sdb", "/dev/sdc",
			},
		},
		{
			// The spare must be counted separately from the members, or mdadm
			// builds a three-member array and there is no spare at all.
			name: "spares are excluded from the member count",
			array: config.RAIDArray{
				Name:    "data",
				Level:   1,
				Devices: []string{"/dev/sdb", "/dev/sdc"},
				Spares:  []string{"/dev/sdd"},
			},
			want: []string{
				"--create", "/dev/md/data",
				"--run",
				"--homehost=any",
				"--name=data",
				"--metadata=1.2",
				"--level=1",
				"--raid-devices=2",
				"--spare-devices=1",
				"/dev/sdb", "/dev/sdc", "/dev/sdd",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := mdadmCreateArgs(&test.array)

			if strings.Join(got, " ") != strings.Join(test.want, " ") {
				t.Errorf("mdadmCreateArgs()\n got: %v\nwant: %v", got, test.want)
			}
		})
	}
}

func TestDescribeSignature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		blkid      string
		wantEmpty  bool
		wantSubstr string
	}{
		{
			name:      "blank device",
			blkid:     "",
			wantEmpty: true,
		},
		{
			name:       "device already carries a filesystem",
			blkid:      "DEVNAME=/dev/sdb\nUUID=1234\nTYPE=ext4\nUSAGE=filesystem\n",
			wantSubstr: "TYPE=ext4",
		},
		{
			name:       "device already carries a partition table",
			blkid:      "DEVNAME=/dev/sdb\nPTTYPE=gpt\n",
			wantSubstr: "PTTYPE=gpt",
		},
		{
			// The case worth catching most: consuming this device would also
			// destroy whatever other array it belongs to.
			name:       "device belongs to another array",
			blkid:      "DEVNAME=/dev/sdb\nTYPE=linux_raid_member\n",
			wantSubstr: "linux_raid_member",
		},
		{
			// A UUID with no TYPE is not evidence of data worth refusing over.
			name:      "identifiers alone are not a signature",
			blkid:     "DEVNAME=/dev/sdb\nUUID=1234\n",
			wantEmpty: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := describeSignature(test.blkid)

			if test.wantEmpty {
				if got != "" {
					t.Errorf("describeSignature() = %q, want empty", got)
				}

				return
			}

			if !strings.Contains(got, test.wantSubstr) {
				t.Errorf("describeSignature() = %q, want it to mention %q",
					got, test.wantSubstr)
			}
		})
	}
}

func TestFstabEntry(t *testing.T) {
	t.Parallel()

	target := mountTarget{Owner: "raid", Name: "data", MountPoint: "/var/lib/corium/data"}

	got := fstabEntry(target, "abcd-1234")

	// By UUID, not by /dev/md/data: the device name is a label mdadm honours,
	// the UUID is a property of the filesystem itself.
	if !strings.HasPrefix(got, "UUID=abcd-1234 ") {
		t.Errorf("fstabEntry() = %q, want it to mount by UUID", got)
	}

	// nofail, so a missing array does not strand the machine at an emergency
	// prompt where nobody can reach it to fix the array.
	if !strings.Contains(got, "nofail") {
		t.Errorf("fstabEntry() = %q, want nofail", got)
	}

	// An empty filesystem field must render the default, not a blank column
	// that would make fstab unparseable.
	if !strings.Contains(got, " ext4 ") {
		t.Errorf("fstabEntry() = %q, want the default filesystem filled in", got)
	}

	if !strings.Contains(got, target.marker()) {
		t.Errorf("fstabEntry() = %q, want the marker that makes it rewritable", got)
	}
}

func TestUpsertFstabReplacesItsOwnLine(t *testing.T) {
	// Not parallel: it swaps a package-level path.
	original := fstabPath
	t.Cleanup(func() { fstabPath = original })

	fstabPath = filepath.Join(t.TempDir(), "fstab")

	unrelated := "UUID=root-uuid / ext4 defaults 0 1"
	if err := os.WriteFile(fstabPath, []byte(unrelated+"\n"), 0o644); err != nil {
		t.Fatalf("seeding fstab: %v", err)
	}

	array := mountTarget{Owner: "raid", Name: "data", MountPoint: "/data"}

	// Bootstrapping twice must not leave two mounts for the same array behind.
	for _, uuid := range []string{"uuid-one", "uuid-two"} {
		if err := upsertFstab(array, uuid); err != nil {
			t.Fatalf("upsertFstab(%s): %v", uuid, err)
		}
	}

	written, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("reading fstab: %v", err)
	}

	got := string(written)

	if strings.Contains(got, "uuid-one") {
		t.Errorf("the superseded entry survived rewriting:\n%s", got)
	}

	if count := strings.Count(got, "/data"); count != 1 {
		t.Errorf("want exactly one entry for the array, got %d:\n%s", count, got)
	}

	// A disk provisioning tool that eats the root filesystem's own fstab line
	// produces a machine that does not boot.
	if !strings.Contains(got, unrelated) {
		t.Errorf("an unrelated fstab line was lost:\n%s", got)
	}

	// A second array must coexist rather than replace the first.
	other := mountTarget{Owner: "raid", Name: "scratch", MountPoint: "/scratch"}
	if err := upsertFstab(other, "uuid-three"); err != nil {
		t.Fatalf("upsertFstab(scratch): %v", err)
	}

	written, err = os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("re-reading fstab: %v", err)
	}

	for _, want := range []string{"/data", "/scratch", unrelated} {
		if !strings.Contains(string(written), want) {
			t.Errorf("want %q to survive, got:\n%s", want, written)
		}
	}
}
