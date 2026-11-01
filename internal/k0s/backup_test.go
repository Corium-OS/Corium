package k0s

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeK0s writes an executable standing in for k0s, running script.
func fakeK0s(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "k0s")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}

	return path
}

func TestBackupReturnsAPrivateArchive(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "backups")
	binary := fakeK0s(t, `printf '\037\213archive'`)

	path, err := Backup(context.Background(), binary, dir, 7)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	if filepath.Dir(path) != dir || !archivePattern.MatchString(filepath.Base(path)) {
		t.Fatalf("Backup() = %q, want an archive retention recognises under %s", path, dir)
	}

	for _, check := range []struct {
		path string
		mode os.FileMode
	}{{path, 0o600}, {dir, 0o700}} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}

		if got := info.Mode().Perm(); got != check.mode {
			t.Errorf("%s has mode %o, want %o", check.path, got, check.mode)
		}
	}
}

// TestBackupLeavesNothingWhenItFails guards against a file that looks like a
// backup and is not one.
func TestBackupLeavesNothingWhenItFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "k0s refuses",
			script:  `printf '\037\213'; echo 'unable to detect cluster status' >&2; exit 1`,
			wantErr: "unable to detect cluster status",
		},
		{
			name:    "stdout is not an archive",
			script:  `echo 'level=info msg=starting'`,
			wantErr: "not a gzip stream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			path, err := Backup(context.Background(), fakeK0s(t, tc.script), dir, 7)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Backup() = %q, %v; want an error mentioning %q", path, err, tc.wantErr)
			}

			if path != "" {
				t.Errorf("Backup() returned %q for an archive that failed", path)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}

			if len(entries) != 0 {
				t.Errorf("a failed backup left %v behind", entries)
			}
		})
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
