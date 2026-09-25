package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestLuksFormatArgs(t *testing.T) {
	t.Parallel()

	got := strings.Join(luksFormatArgs("/dev/sdb", "/run/corium/data.key"), " ")

	// --key-file, never the passphrase itself: a command-line argument is
	// readable in /proc by every process on the machine while it runs.
	want := "luksFormat --type luks2 --batch-mode --key-file /run/corium/data.key /dev/sdb"
	if got != want {
		t.Errorf("luksFormatArgs()\n got: %s\nwant: %s", got, want)
	}
}

func TestCryptenrollArgs(t *testing.T) {
	t.Parallel()

	got := strings.Join(cryptenrollArgs("/dev/sdb", "/run/corium/data.key"), " ")

	// The TPM slot is added and the temporary password slot removed in one
	// command, so an interrupted enrolment leaves a volume that still opens.
	want := "--tpm2-device=auto --unlock-key-file=/run/corium/data.key --wipe-slot=password /dev/sdb"
	if got != want {
		t.Errorf("cryptenrollArgs()\n got: %s\nwant: %s", got, want)
	}
}

func TestCrypttabEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		volume  config.LUKSVolume
		keyFile string
		want    string
	}{
		{
			name:   "tpm2 is the default and carries no key file",
			volume: config.LUKSVolume{Name: "data"},
			want:   "data UUID=abcd-1234 - tpm2-device=auto,nofail",
		},
		{
			name:   "tpm2 named explicitly",
			volume: config.LUKSVolume{Name: "data", Unlock: config.LUKSUnlockTPM2},
			want:   "data UUID=abcd-1234 - tpm2-device=auto,nofail",
		},
		{
			name:    "a passphrase volume unlocks from its key file",
			volume:  config.LUKSVolume{Name: "data", Unlock: config.LUKSUnlockPassphrase},
			keyFile: "/etc/luks-keys/data.key",
			want:    "data UUID=abcd-1234 /etc/luks-keys/data.key nofail",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := crypttabEntry(&test.volume, "abcd-1234", test.keyFile)
			if got != test.want {
				t.Errorf("crypttabEntry()\n got: %s\nwant: %s", got, test.want)
			}

			// nofail on every entry: a volume that does not unlock must leave
			// the machine booting and reachable, not at an emergency prompt.
			if !strings.Contains(got, "nofail") {
				t.Errorf("crypttabEntry() = %q, want nofail", got)
			}

			// By UUID, never by the kernel name the device happened to get.
			if !strings.Contains(got, "UUID=abcd-1234") {
				t.Errorf("crypttabEntry() = %q, want it to unlock by UUID", got)
			}
		})
	}
}

func TestUpsertCrypttabReplacesItsOwnEntry(t *testing.T) {
	// Not parallel: it swaps a package-level path.
	original := crypttabPath
	t.Cleanup(func() { crypttabPath = original })

	crypttabPath = filepath.Join(t.TempDir(), "crypttab")

	unrelated := "swap /dev/sdz /dev/urandom swap"
	if err := os.WriteFile(crypttabPath, []byte(unrelated+"\n"), 0o600); err != nil {
		t.Fatalf("seeding crypttab: %v", err)
	}

	volume := config.LUKSVolume{Name: "data"}

	// Bootstrapping twice must not leave two entries for the same volume, which
	// systemd would turn into two units fighting over one mapper name.
	for _, uuid := range []string{"uuid-one", "uuid-two"} {
		if err := upsertCrypttab(&volume, uuid, ""); err != nil {
			t.Fatalf("upsertCrypttab(%s): %v", uuid, err)
		}
	}

	written, err := os.ReadFile(crypttabPath)
	if err != nil {
		t.Fatalf("reading crypttab: %v", err)
	}

	got := string(written)

	if strings.Contains(got, "uuid-one") {
		t.Errorf("the superseded entry survived rewriting:\n%s", got)
	}

	if count := strings.Count(got, crypttabMarker+" data"); count != 1 {
		t.Errorf("want exactly one marker for the volume, got %d:\n%s", count, got)
	}

	if count := strings.Count(got, "uuid-two"); count != 1 {
		t.Errorf("want exactly one entry for the volume, got %d:\n%s", count, got)
	}

	// Eating an operator's own crypttab line is how a machine stops booting.
	if !strings.Contains(got, unrelated) {
		t.Errorf("an unrelated crypttab line was lost:\n%s", got)
	}

	// A second volume coexists rather than replacing the first.
	other := config.LUKSVolume{Name: "scratch"}
	if err := upsertCrypttab(&other, "uuid-three", ""); err != nil {
		t.Fatalf("upsertCrypttab(scratch): %v", err)
	}

	written, err = os.ReadFile(crypttabPath)
	if err != nil {
		t.Fatalf("re-reading crypttab: %v", err)
	}

	for _, want := range []string{"uuid-two", "uuid-three", unrelated} {
		if !strings.Contains(string(written), want) {
			t.Errorf("want %q to survive, got:\n%s", want, written)
		}
	}

	// The file names every key file on the machine, so it is not world-readable.
	info, err := os.Stat(crypttabPath)
	if err != nil {
		t.Fatalf("stat crypttab: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("crypttab mode = %o, want 600", mode)
	}
}

func TestUpsertCrypttabReplacesAHandWrittenEntry(t *testing.T) {
	original := crypttabPath
	t.Cleanup(func() { crypttabPath = original })

	crypttabPath = filepath.Join(t.TempDir(), "crypttab")

	// No Corium marker on this one: an operator, or an earlier tool, claimed the
	// name. Two entries for one mapper name is not a state worth half-keeping.
	if err := os.WriteFile(crypttabPath, []byte("data /dev/sdz none\n"), 0o600); err != nil {
		t.Fatalf("seeding crypttab: %v", err)
	}

	volume := config.LUKSVolume{Name: "data"}
	if err := upsertCrypttab(&volume, "uuid-one", ""); err != nil {
		t.Fatalf("upsertCrypttab: %v", err)
	}

	written, err := os.ReadFile(crypttabPath)
	if err != nil {
		t.Fatalf("reading crypttab: %v", err)
	}

	if strings.Contains(string(written), "/dev/sdz") {
		t.Errorf("the conflicting entry survived:\n%s", written)
	}
}

func TestCrypttabVolumeName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"data UUID=abcd - tpm2-device=auto,nofail": "data",
		"  scratch /dev/sdb none":                  "scratch",
		"# corium-luks data":                       "",
		"#data UUID=abcd - nofail":                 "",
		"":                                         "",
		"   ":                                      "",
	}

	for line, want := range tests {
		if got := crypttabVolumeName(line); got != want {
			t.Errorf("crypttabVolumeName(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestLuksSignature(t *testing.T) {
	t.Parallel()

	// A device carrying a header is adopted, never reformatted: mistaking this
	// for a blank disk destroys the data behind it with no recovery.
	if !luksSignature("DEVNAME=/dev/sdb\nUUID=1234\nTYPE=crypto_LUKS\nUSAGE=crypto\n") {
		t.Error("luksSignature() = false for a LUKS container, want true")
	}

	for _, export := range []string{
		"",
		"DEVNAME=/dev/sdb\nTYPE=ext4\n",
		"DEVNAME=/dev/sdb\nTYPE=linux_raid_member\n",
		// A partition table naming a LUKS partition is not a LUKS container.
		"DEVNAME=/dev/sdb\nPTTYPE=gpt\n",
	} {
		if luksSignature(export) {
			t.Errorf("luksSignature(%q) = true, want false", export)
		}
	}
}

func TestUnexpectedMountPoints(t *testing.T) {
	t.Parallel()

	// The booted disk: lsblk reports the mounts of the device and its
	// partitions, and any of them makes it a disk this must never encrypt.
	booted := "\n/boot\n/sysroot\n/var\n"
	if got := unexpectedMountPoints(booted, ""); len(got) != 3 {
		t.Errorf("unexpectedMountPoints(booted) = %v, want three mount points", got)
	}

	// A blank data disk.
	if got := unexpectedMountPoints("\n\n", ""); len(got) != 0 {
		t.Errorf("unexpectedMountPoints(blank) = %v, want none", got)
	}

	// A second bootstrap finds the disk in use -- by this volume. That has to
	// stay a no-op rather than becoming a refusal.
	if got := unexpectedMountPoints("\n/var/lib/corium/data\n", "/var/lib/corium/data"); len(got) != 0 {
		t.Errorf("unexpectedMountPoints(own mount) = %v, want none", got)
	}

	// Somebody else's mount on the same disk is still a refusal.
	if got := unexpectedMountPoints("\n/var/lib/corium/data\n/srv\n", "/var/lib/corium/data"); len(got) != 1 {
		t.Errorf("unexpectedMountPoints(foreign mount) = %v, want one", got)
	}
}

func TestWriteKeyFileStoresThePassphraseVerbatim(t *testing.T) {
	original := keyDir
	t.Cleanup(func() { keyDir = original })

	keyDir = filepath.Join(t.TempDir(), "luks-keys")

	volume := config.LUKSVolume{
		Name:       "data",
		Unlock:     config.LUKSUnlockPassphrase,
		Passphrase: "correct horse battery staple\n",
	}

	path, err := writeKeyFile(t.Context(), &volume)
	if err != nil {
		t.Fatalf("writeKeyFile: %v", err)
	}

	written, err := os.ReadFile(path) //nolint:gosec // the path is this test's temp dir
	if err != nil {
		t.Fatalf("reading the key file: %v", err)
	}

	// No trailing newline: cryptsetup uses the file's bytes as the key, so a
	// newline here becomes part of the key and the passphrase an operator types
	// at a rescue prompt no longer opens the volume.
	if string(written) != "correct horse battery staple" {
		t.Errorf("key file = %q, want the passphrase with no surrounding whitespace", written)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("key file mode = %o, want 600", mode)
	}
}

func TestWriteKeyFileRefusesAnEmptyPassphrase(t *testing.T) {
	original := keyDir
	t.Cleanup(func() { keyDir = original })

	keyDir = filepath.Join(t.TempDir(), "luks-keys")

	// A secret that resolved to whitespace would otherwise become a volume
	// anybody can open by pressing enter.
	volume := config.LUKSVolume{Name: "data", Unlock: config.LUKSUnlockPassphrase, Passphrase: "   "}

	if _, err := writeKeyFile(t.Context(), &volume); err == nil {
		t.Error("writeKeyFile() = nil, want an error for an empty passphrase")
	}
}

func TestTemporaryKeyIsRemovedAndRandom(t *testing.T) {
	original := runtimeKeyDir
	t.Cleanup(func() { runtimeKeyDir = original })

	runtimeKeyDir = filepath.Join(t.TempDir(), "run")

	first, remove, err := temporaryKey("data")
	if err != nil {
		t.Fatalf("temporaryKey: %v", err)
	}

	key, err := os.ReadFile(first) //nolint:gosec // the path is this test's temp dir
	if err != nil {
		t.Fatalf("reading the temporary key: %v", err)
	}

	if len(key) != 32 {
		t.Errorf("temporary key is %d bytes, want 32", len(key))
	}

	remove()

	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("the temporary key survived cleanup: %v", err)
	}

	// Two volumes must not be handed the same key, which would make one's
	// header openable with the other's.
	second, removeSecond, err := temporaryKey("scratch")
	if err != nil {
		t.Fatalf("temporaryKey: %v", err)
	}

	defer removeSecond()

	other, err := os.ReadFile(second) //nolint:gosec // the path is this test's temp dir
	if err != nil {
		t.Fatalf("reading the second temporary key: %v", err)
	}

	if string(key) == string(other) {
		t.Error("two volumes were given the same temporary key")
	}
}

func TestLUKSMountPoints(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{LUKS: []config.LUKSVolume{
		{Name: "data", MountPoint: "/var/lib/corium/data"},
		{Name: "raw", Filesystem: config.RAIDFilesystemNone},
	}}

	got := luksMountPoints(cfg)

	if len(got) != 1 || got[0] != "/var/lib/corium/data" {
		t.Errorf("luksMountPoints() = %v, want only the mounted volume", got)
	}
}

// TestWriteKeyFileReusesWhatIsAlreadyOnDisk is the regression test for a second
// bootstrap failing on a volume that was already unlocked.
//
// It was found on a real node: the passphrase came from a cloud-init
// write_files entry under /run, the node rebooted, /run was empty, and the next
// `corium-agent bootstrap` stopped at "LUKS passphrase: not available yet" --
// on a volume whose key file was sitting in /etc/luks-keys the whole time.
func TestWriteKeyFileReusesWhatIsAlreadyOnDisk(t *testing.T) {
	original := keyDir
	t.Cleanup(func() { keyDir = original })
	keyDir = t.TempDir()

	volume := &config.LUKSVolume{
		Name:           "passdata",
		Device:         "/dev/sdc",
		Unlock:         config.LUKSUnlockPassphrase,
		PassphraseFrom: &config.SecretSource{File: "/run/gone-after-a-reboot"},
	}

	// What the first boot left behind.
	existing := keyFilePath(volume.Name)
	if err := os.WriteFile(existing, []byte("the-original-passphrase"), 0o600); err != nil {
		t.Fatalf("seeding the key file: %v", err)
	}

	// The source is gone. Resolving it would fail, so reaching the resolver at
	// all is the bug.
	path, err := writeKeyFile(t.Context(), volume)
	if err != nil {
		t.Fatalf("writeKeyFile() = %v, want it to reuse the key file on disk", err)
	}

	if path != existing {
		t.Errorf("key file = %q, want %q", path, existing)
	}

	content, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading the key file: %v", err)
	}

	if string(content) != "the-original-passphrase" {
		t.Errorf("key file = %q, want the original left untouched", content)
	}
}
