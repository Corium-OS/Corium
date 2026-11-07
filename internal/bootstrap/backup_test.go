package bootstrap

import (
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestBackupDropIn(t *testing.T) {
	t.Parallel()

	got := backupDropIn(config.Backup{Path: "/mnt/backups", Keep: 30})

	want := "# Written by corium-agent from the corium: block.\n" +
		"[Service]\n" +
		"Environment=\"CORIUM_BACKUP_PATH=/mnt/backups\"\n" +
		"Environment=\"CORIUM_BACKUP_KEEP=30\"\n"

	if got != want {
		t.Errorf("backupDropIn() =\n%q\nwant\n%q", got, want)
	}
}

// TestBackupDropInQuotesAPathWithASpace pins the reason the values are quoted:
// systemd splits an unquoted Environment= on whitespace, and a truncated target
// writes the cluster's secrets somewhere nobody looks.
func TestBackupDropInQuotesAPathWithASpace(t *testing.T) {
	t.Parallel()

	got := backupDropIn(config.Backup{Path: "/mnt/nfs share/backups", Keep: 7})

	if !strings.Contains(got, "Environment=\"CORIUM_BACKUP_PATH=/mnt/nfs share/backups\"\n") {
		t.Errorf("backupDropIn() = %q, want the path quoted whole", got)
	}
}
