package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Corium-OS/Corium/internal/config"
)

const (
	// backupTimer fires the snapshot. It ships disabled; corium-agent enables
	// it at first boot only when corium.backup asks for it.
	backupTimer = "corium-backup.timer"

	// backupService is what the timer starts: `corium-agent backup`.
	backupService = "corium-backup.service"
)

// applyBackupPolicy puts the control plane snapshot on a timer, if one was
// asked for. Doing nothing is the default and the common case.
//
// Validation has already refused a backup block on a node with no control
// plane, so reaching here means `k0s backup` has something to read.
func applyBackupPolicy(ctx context.Context, cfg *config.Config) error {
	if !cfg.Backup.Enabled {
		return nil
	}

	if err := writeBackupSettings(cfg.Backup); err != nil {
		return err
	}

	if err := writeSchedule(backupTimer, cfg.Backup.Schedule, config.DefaultBackupSchedule); err != nil {
		return err
	}

	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	// --now starts the timer, not the service: the first snapshot happens on
	// the schedule. Taking one here would back up a cluster that is seconds old
	// and has nothing in it yet.
	if err := run(ctx, "systemctl", "enable", "--now", backupTimer); err != nil {
		return err
	}

	slog.Info("scheduled control plane backups",
		"schedule", cfg.Backup.Schedule, "path", cfg.Backup.Path, "keep", cfg.Backup.Keep)

	return nil
}

// writeBackupSettings hands the service its target and its retention.
func writeBackupSettings(backup config.Backup) error {
	dir := filepath.Join(dropInDir, backupService+".d")

	// 0755, as writeSchedule uses and for the same reason: a path and a count
	// are configuration rather than a credential, and `systemctl cat` should
	// work without root when somebody is working out where the archives went.
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	path := filepath.Join(dir, "10-corium-backup.conf")
	if err := os.WriteFile(path, []byte(backupDropIn(backup)), 0o644); err != nil { // #nosec G306
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// backupDropIn renders the service drop-in carrying the operator's target and
// retention.
//
// Both values are quoted: systemd splits an unquoted Environment= on
// whitespace, so a target path with a space in it would otherwise arrive
// truncated and the archives would land somewhere nobody looks. Validation
// rejects a path carrying a quote, a backslash or a newline, which is what
// makes quoting here sufficient rather than merely hopeful.
func backupDropIn(backup config.Backup) string {
	return "# Written by corium-agent from the corium: block.\n" +
		"[Service]\n" +
		"Environment=\"CORIUM_BACKUP_PATH=" + backup.Path + "\"\n" +
		"Environment=\"CORIUM_BACKUP_KEEP=" + strconv.Itoa(backup.Keep) + "\"\n"
}
