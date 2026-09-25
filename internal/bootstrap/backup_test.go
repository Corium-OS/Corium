package bootstrap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestArchiveName(t *testing.T) {
	t.Parallel()

	// A local zone, to check the name is stamped in UTC: two controllers in
	// different time zones must produce names that sort against each other.
	zone := time.FixedZone("UTC+5", 5*60*60)

	got := archiveName(time.Date(2026, 9, 25, 3, 4, 5, 0, zone))
	if want := "corium-backup-20260924T220405Z.tar.gz"; got != want {
		t.Errorf("archiveName() = %q, want %q", got, want)
	}

	if !archivePattern.MatchString(got) {
		t.Errorf("archiveName() = %q, which retention would never recognise", got)
	}
}

// TestStaleArchivesIgnoresEverythingItDidNotWrite is the test this feature
// exists to keep passing. The target directory belongs to the operator; a
// retention pass that sweeps a glob is how a backup job deletes the thing it
// was pointed at.
func TestStaleArchivesIgnoresEverythingItDidNotWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	decoys := []string{
		// k0s's own naming, from a backup somebody took by hand.
		"k0s_backup_2026-09-20T03:00:00Z.tar.gz",
		// Somebody else's archives, in a directory shared with ours.
		"etcd-snapshot.tar.gz",
		"backup.tar.gz",
		"corium-backup.tar.gz",
		// Near misses on our own name.
		"corium-backup-20260920T030000Z.tar.gz.partial",
		"corium-backup-2026092T030000Z.tar.gz",
		"corium-backup-20260920T030000.tar.gz",
		"old-corium-backup-20260920T030000Z.tar.gz",
		"corium-backup-20260920T030000Z.tar.gz.sha256",
	}

	for _, name := range decoys {
		write(t, filepath.Join(dir, name))
	}

	// A directory named exactly like an archive, and a symlink to a real one.
	if err := os.Mkdir(filepath.Join(dir, "corium-backup-20260101T000000Z.tar.gz"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("/etc/passwd",
		filepath.Join(dir, "corium-backup-20260102T000000Z.tar.gz")); err != nil {
		t.Fatal(err)
	}

	ours := []string{
		"corium-backup-20260921T030000Z.tar.gz",
		"corium-backup-20260922T030000Z.tar.gz",
		"corium-backup-20260923T030000Z.tar.gz",
		"corium-backup-20260924T030000Z.tar.gz",
	}

	for _, name := range ours {
		write(t, filepath.Join(dir, name))
	}

	stale, err := staleArchives(dir, 2)
	if err != nil {
		t.Fatalf("staleArchives() = %v", err)
	}

	want := []string{ours[0], ours[1]}
	if !slices.Equal(stale, want) {
		t.Fatalf("staleArchives() = %v, want %v", stale, want)
	}

	// And the pass itself leaves everything else where it is.
	if err := prune(dir, 2); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	for _, name := range append(decoys, ours[2], ours[3]) {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("prune() removed %s, which it did not write", name)
		}
	}
}

func TestStaleArchives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		keep  int
		want  []string
	}{
		{
			name:  "fewer archives than the retention count",
			files: []string{"corium-backup-20260921T030000Z.tar.gz"},
			keep:  7,
		},
		{
			name: "exactly the retention count",
			files: []string{
				"corium-backup-20260921T030000Z.tar.gz",
				"corium-backup-20260922T030000Z.tar.gz",
			},
			keep: 2,
		},
		{
			name: "the oldest go first",
			files: []string{
				"corium-backup-20260923T030000Z.tar.gz",
				"corium-backup-20260921T030000Z.tar.gz",
				"corium-backup-20260922T030000Z.tar.gz",
			},
			keep: 1,
			want: []string{
				"corium-backup-20260921T030000Z.tar.gz",
				"corium-backup-20260922T030000Z.tar.gz",
			},
		},
		{
			// Configuration cannot produce this; a pass reached with a count it
			// does not understand must do nothing rather than everything.
			name:  "a retention count of zero deletes nothing",
			files: []string{"corium-backup-20260921T030000Z.tar.gz"},
			keep:  0,
		},
		{
			name:  "a negative retention count deletes nothing",
			files: []string{"corium-backup-20260921T030000Z.tar.gz"},
			keep:  -1,
		},
		{
			name: "an empty directory",
			keep: 7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for _, name := range tc.files {
				write(t, filepath.Join(dir, name))
			}

			got, err := staleArchives(dir, tc.keep)
			if err != nil {
				t.Fatalf("staleArchives() = %v", err)
			}

			if !slices.Equal(got, tc.want) {
				t.Errorf("staleArchives() = %v, want %v", got, tc.want)
			}
		})
	}
}

func write(t *testing.T, path string) {
	t.Helper()

	// The gzip magic, so a file that is meant to look like an archive does.
	if err := os.WriteFile(path, []byte{0x1f, 0x8b}, 0o600); err != nil {
		t.Fatal(err)
	}
}
