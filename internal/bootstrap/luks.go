package bootstrap

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/secret"
)

// crypttabPath gains one entry per encrypted volume, so a volume unlocked at
// first boot is unlocked again on every boot after it. A variable so that tests
// can exercise the rewriting against a real file, as fstabPath is.
var crypttabPath = "/etc/crypttab"

// crypttabMarker tags the comment line above each entry this code owns. It sits
// on its own line rather than at the end of the entry because crypttab has four
// fields and no documented inline comment; a marker in the options column would
// be an option systemd has to make sense of.
const crypttabMarker = "# corium-luks"

// keyDir holds the key files of passphrase-unlocked volumes. In /etc because
// the key has to be readable before /var is necessarily mounted, and because it
// is machine-local state the image must never ship.
//
// Variables so that tests can point them somewhere writable.
var (
	keyDir = "/etc/luks-keys"

	// runtimeKeyDir holds the throwaway key a TPM-unlocked volume is formatted
	// with. /run is a tmpfs, so that key never touches a disk.
	runtimeKeyDir = "/run/corium"
)

// mapperDevice is where device-mapper publishes an opened LUKS volume.
func mapperDevice(name string) string { return "/dev/mapper/" + name }

// applyLUKS unlocks every declared volume, formats it and mounts it.
//
// This runs before k0s is installed, for the reason applyRAID gives in full: a
// mount that lands after the kubelet has started is a mount it has already
// written past, and the data underneath it is then stranded on the root disk
// where nothing will look for it again.
//
// It runs after applyRAID, so a volume may name a /dev/md/<name> that raid[]
// has just assembled and get encrypted redundant storage out of the two.
func applyLUKS(ctx context.Context, cfg *config.Config) error {
	if len(cfg.LUKS) == 0 {
		return nil
	}

	for i := range cfg.LUKS {
		if err := applyVolume(ctx, &cfg.LUKS[i]); err != nil {
			return fmt.Errorf("luks %q: %w", cfg.LUKS[i].Name, err)
		}
	}

	return requireMountsForK0s(cfg, "luks", luksMountPoints(cfg))
}

// luksMountPoints lists the paths the declared volumes are mounted at.
func luksMountPoints(cfg *config.Config) []string {
	var mountPoints []string

	for _, volume := range cfg.LUKS {
		if volume.MountPoint != "" {
			mountPoints = append(mountPoints, volume.MountPoint)
		}
	}

	return mountPoints
}

func applyVolume(ctx context.Context, volume *config.LUKSVolume) error {
	// The key file first, on both paths: creating the volume needs it, and
	// adopting one needs it on disk anyway so that later boots unlock without
	// anybody at the console.
	keyFile := ""

	if volume.Unlock == config.LUKSUnlockPassphrase {
		var err error

		if keyFile, err = writeKeyFile(ctx, volume); err != nil {
			return err
		}
	}

	encrypted, err := luksFormatted(ctx, volume.Device)
	if err != nil {
		return err
	}

	if encrypted {
		// Adopted, never reformatted, and this is the single most important
		// behaviour in this file. luksFormat writes a new header in place, and
		// the old header held the only copy of the key that decrypts what is
		// behind it: there is no undo, no recovery, and no wipe: true that
		// should make it convenient.
		slog.Info("device already carries a LUKS header, adopting it",
			"name", volume.Name, "device", volume.Device)
	} else if err := createVolume(ctx, volume, keyFile); err != nil {
		return err
	}

	if err := openVolume(ctx, volume, keyFile); err != nil {
		return err
	}

	if volume.Filesystem == config.RAIDFilesystemNone {
		return nil
	}

	target := mountTarget{
		Owner:      "luks",
		Name:       volume.Name,
		Filesystem: volume.Filesystem,
		MountPoint: volume.MountPoint,
	}

	if err := ensureFilesystem(ctx, target, mapperDevice(volume.Name)); err != nil {
		return err
	}

	if volume.MountPoint == "" {
		return nil
	}

	return mountDevice(ctx, target, mapperDevice(volume.Name))
}

// createVolume encrypts a device that is not encrypted yet.
func createVolume(ctx context.Context, volume *config.LUKSVolume, keyFile string) error {
	// The booted disk first, because getting that wrong costs the machine and
	// not just the disk.
	if err := assertNotInUse(ctx, volume.Device, volume.MountPoint); err != nil {
		return err
	}

	// Then the same refusal RAID makes: a device already carrying a filesystem,
	// a partition table or array metadata stops the bootstrap.
	if err := assertUsable(ctx, volume.Device, volume.Wipe); err != nil {
		return err
	}

	if volume.Wipe {
		slog.Warn("wiping device before encrypting it",
			"device", volume.Device, "name", volume.Name)

		if err := run(ctx, "wipefs", "--all", volume.Device); err != nil {
			return err
		}
	}

	if keyFile == "" {
		// A TPM-unlocked volume still has to be formatted with some key. This
		// one is random, lives on a tmpfs for the length of two commands, and
		// is removed from the header by the enrolment below.
		temporary, remove, err := temporaryKey(volume.Name)
		if err != nil {
			return err
		}

		defer remove()

		keyFile = temporary
	}

	slog.Info("encrypting device",
		"name", volume.Name, "device", volume.Device, "unlock", unlockMethod(volume))

	if err := run(ctx, "cryptsetup", luksFormatArgs(volume.Device, keyFile)...); err != nil {
		return err
	}

	if volume.Unlock == config.LUKSUnlockPassphrase {
		return nil
	}

	return run(ctx, "systemd-cryptenroll", cryptenrollArgs(volume.Device, keyFile)...)
}

// openVolume records how the volume is unlocked and then unlocks it that way.
//
// Going through the crypttab entry and the unit systemd generates from it,
// rather than calling cryptsetup directly, means the line that will open this
// volume on every future boot is the line that opens it now. A crypttab entry
// that would have failed at the next reboot fails here instead, while an
// operator is still watching and before k0s has put anything on the disk.
func openVolume(ctx context.Context, volume *config.LUKSVolume, keyFile string) error {
	uuid, err := commandOutput(ctx, "blkid", "--match-tag", "UUID", "--output", "value", volume.Device)
	if err != nil {
		return fmt.Errorf("reading the LUKS UUID of %s: %w", volume.Device, err)
	}

	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return fmt.Errorf("device %s reported no LUKS UUID", volume.Device)
	}

	if err := upsertCrypttab(volume, uuid, keyFile); err != nil {
		return err
	}

	// systemd generates the unlock units from crypttab, so it has to be told
	// the file changed before the volume is asked for.
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	if _, err := os.Stat(mapperDevice(volume.Name)); err == nil {
		// Already open: a reboot unlocked it from the crypttab entry long
		// before corium-agent ran.
		return nil
	}

	slog.Info("unlocking luks volume",
		"name", volume.Name, "device", volume.Device, "unlock", unlockMethod(volume))

	// The volume name is restricted to characters that need no systemd
	// escaping, which is why the unit name can be built by concatenation.
	return run(ctx, "systemctl", "start", "systemd-cryptsetup@"+volume.Name+".service")
}

// unlockMethod is the volume's unlock method with the default filled in.
func unlockMethod(volume *config.LUKSVolume) string {
	if volume.Unlock == "" {
		return config.LUKSUnlockTPM2
	}

	return volume.Unlock
}

// luksFormatArgs builds the format command.
//
// --type luks2 explicitly: LUKS2 is what systemd-cryptenroll stores a TPM token
// in, and naming it here means the volume does not depend on what the
// distribution's cryptsetup happens to default to this year.
//
// --key-file, never a passphrase on the command line: an argument is readable
// in /proc by every process on the machine for as long as the command runs.
func luksFormatArgs(device, keyFile string) []string {
	return []string{
		"luksFormat",
		"--type", "luks2",
		"--batch-mode",
		"--key-file", keyFile,
		device,
	}
}

// cryptenrollArgs seals the volume's key to the TPM and drops the key it was
// formatted with, in one command.
//
// --wipe-slot=password is what makes the temporary key temporary: systemd
// removes the plain key slot only once the TPM slot is in place, so an
// interrupted enrolment leaves a volume that can still be opened rather than
// one that cannot be opened at all. What is left afterwards is a volume this
// machine's TPM opens and nothing else does -- which is the point of tpm2, and
// also its risk. See docs/adr/0010-luks-data-disks.md.
func cryptenrollArgs(device, keyFile string) []string {
	return []string{
		"--tpm2-device=auto",
		"--unlock-key-file=" + keyFile,
		"--wipe-slot=password",
		device,
	}
}

// luksFormatted reports whether the device already carries a LUKS header.
func luksFormatted(ctx context.Context, device string) (bool, error) {
	output, err := commandOutput(ctx, "blkid", "--probe", "--output", "export", device)
	if err != nil {
		if blkidFoundNothing(err) {
			return false, nil
		}

		// Same rule as assertUsable: a probe that failed is not permission to
		// treat the device as blank, because the next thing this code would do
		// is write a new header over the old one.
		return false, fmt.Errorf("probing %s: %w", device, err)
	}

	return luksSignature(output), nil
}

// luksSignature reports whether blkid's output describes a LUKS container.
func luksSignature(blkidExport string) bool {
	for _, line := range strings.Split(blkidExport, "\n") {
		if strings.TrimSpace(line) == "TYPE=crypto_LUKS" {
			return true
		}
	}

	return false
}

// assertNotInUse refuses to encrypt a device the running system is using.
//
// Encrypting the root filesystem is not something this code could do even if it
// tried -- root is mounted and running the process that would encrypt it -- but
// naming the booted disk here would destroy the machine rather than fail, so the
// refusal is structural instead of advisory. Checking by mount point also
// catches a data disk something else has already claimed.
//
// allowed is this volume's own mount point, tolerated so that bootstrapping the
// same node twice stays a no-op: on the second run the disk is in use, by us.
func assertNotInUse(ctx context.Context, device, allowed string) error {
	output, err := commandOutput(ctx,
		"lsblk", "--noheadings", "--list", "--output", "MOUNTPOINTS", device)
	if err != nil {
		return fmt.Errorf("checking what %s is being used for: %w", device, err)
	}

	if inUse := unexpectedMountPoints(output, allowed); len(inUse) > 0 {
		return fmt.Errorf(
			"device %s is in use: it carries the mounted filesystem(s) %s; "+
				"corium.luks covers data disks, never the disk the OS booted from",
			device, strings.Join(inUse, ", "))
	}

	return nil
}

// unexpectedMountPoints lists the mount points lsblk reported for a device and
// its partitions, minus the one this volume is allowed to own.
func unexpectedMountPoints(lsblkOutput, allowed string) []string {
	var found []string

	for _, line := range strings.Split(lsblkOutput, "\n") {
		mountPoint := strings.TrimSpace(line)
		if mountPoint == "" || mountPoint == allowed {
			continue
		}

		found = append(found, mountPoint)
	}

	return found
}

// keyFilePath is where a passphrase-unlocked volume's key is kept.
func keyFilePath(name string) string { return filepath.Join(keyDir, name+".key") }

// writeKeyFile resolves the volume's passphrase and stores it for later boots.
//
// A key file already on disk is the answer, and resolving the secret again is
// skipped. The file is what every boot after the first one unlocks with, so it
// is the authority rather than a cache of one -- and re-resolving turns a
// second bootstrap into a failure whenever the source was transient, which is
// the common shape: a cloud-init write_files entry under /run is gone after the
// first reboot, and `waitFor` would sit there for its whole window waiting for
// a secret nothing is going to write again. A volume that needs a different
// passphrase needs `cryptsetup luksChangeKey`, which this does not do and
// `luks` being immutable day-two already says.
//
// The passphrase is resolved through the same path as a join token, so it need
// not sit in instance metadata. What lands on disk afterwards is a 0600 file in
// a 0700 directory on an unencrypted root: it protects a disk that leaves the
// machine, not a machine that leaves the building. The ADR says so rather than
// letting the word "encrypted" imply more than it buys.
func writeKeyFile(ctx context.Context, volume *config.LUKSVolume) (string, error) {
	path := keyFilePath(volume.Name)

	if _, err := os.Stat(path); err == nil {
		slog.Info("reusing the key file already on disk",
			"name", volume.Name, "path", path)

		return path, nil
	} else if !os.IsNotExist(err) {
		// A stat that failed for any other reason is not permission to resolve
		// the secret and overwrite whatever is there.
		return "", fmt.Errorf("checking the key file for volume %q: %w", volume.Name, err)
	}

	passphrase := volume.Passphrase

	if volume.PassphraseFrom != nil {
		resolved, err := secret.Resolve(ctx, volume.PassphraseFrom, "LUKS passphrase")
		if err != nil {
			return "", err
		}

		passphrase = resolved
	}

	if strings.TrimSpace(passphrase) == "" {
		return "", fmt.Errorf("the passphrase for volume %q resolved to nothing", volume.Name)
	}

	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", keyDir, err)
	}

	// Written verbatim, with no trailing newline: cryptsetup uses the file's
	// bytes as the key, so a newline here would become part of the key and the
	// passphrase an operator types at a rescue prompt would no longer open the
	// volume.
	//
	// The error deliberately names the path and not the value.
	if err := os.WriteFile(path, []byte(strings.TrimSpace(passphrase)), 0o600); err != nil {
		return "", fmt.Errorf("writing the key file for volume %q: %w", volume.Name, err)
	}

	return path, nil
}

// temporaryKey mints the key a TPM-unlocked volume is formatted with, and
// returns a function that removes it.
//
// It exists for the length of two commands: LUKS has to be formatted with some
// key, and systemd-cryptenroll has to unlock with that key to add the TPM slot.
// It is random, written to a tmpfs, removed immediately, and wiped out of the
// header by the enrolment -- so nothing capable of opening the volume is ever
// written to a disk.
func temporaryKey(name string) (string, func(), error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, fmt.Errorf("generating a key for volume %q: %w", name, err)
	}

	if err := os.MkdirAll(runtimeKeyDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", runtimeKeyDir, err)
	}

	path := filepath.Join(runtimeKeyDir, name+".key")

	if err := os.WriteFile(path, key, 0o600); err != nil {
		return "", nil, fmt.Errorf("writing a key for volume %q: %w", name, err)
	}

	// Best effort: /run is a tmpfs that does not survive the boot, so a failed
	// removal leaves the key nowhere more durable than memory.
	return path, func() { _ = os.Remove(path) }, nil
}

// upsertCrypttab rewrites this volume's entry, leaving every other line alone.
func upsertCrypttab(volume *config.LUKSVolume, uuid, keyFile string) error {
	existing, err := os.ReadFile(crypttabPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", crypttabPath, err)
	}

	tag := crypttabMarker + " " + volume.Name

	var kept []string

	for _, line := range strings.Split(string(existing), "\n") {
		// The marker comment this code wrote last time, and any entry naming
		// this volume -- by whoever wrote it. The volume name is the key
		// device-mapper goes by, so two entries for one name is not a state
		// worth preserving half of.
		if strings.TrimSpace(line) == tag || crypttabVolumeName(line) == volume.Name {
			continue
		}

		kept = append(kept, line)
	}

	// Trailing blank lines accumulate otherwise, one per bootstrap.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	kept = append(kept, tag, crypttabEntry(volume, uuid, keyFile), "")

	// 0600: the file names the key file of every passphrase-unlocked volume on
	// the machine, which is a map to the keys even though it is not the keys.
	return os.WriteFile(crypttabPath, []byte(strings.Join(kept, "\n")), 0o600) //nolint:gosec // a fixed path, a variable only so tests can redirect it
}

// crypttabVolumeName is the volume an existing crypttab line describes, or
// empty for a comment or a blank line.
func crypttabVolumeName(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
		return ""
	}

	return fields[0]
}

// crypttabEntry renders the unlock line.
//
// By UUID rather than by /dev/sdb, for the reason fstabEntry gives: the kernel
// name is an enumeration order, the UUID is a property of the header.
//
// nofail, so a volume that does not unlock -- a cleared TPM, a disk that did not
// appear -- leaves the machine booting and reachable instead of sitting at an
// emergency prompt nobody can get to. What stops Kubernetes running without the
// storage is the RequiresMountsFor drop-in, the same division of labour RAID
// uses.
func crypttabEntry(volume *config.LUKSVolume, uuid, keyFile string) string {
	key, options := "-", "nofail"

	if volume.Unlock == config.LUKSUnlockPassphrase {
		key = keyFile
	} else {
		options = "tpm2-device=auto," + options
	}

	return fmt.Sprintf("%s UUID=%s %s %s", volume.Name, uuid, key, options)
}
