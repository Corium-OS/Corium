package k0s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// archivePrefix names an archive this agent wrote. The name is coined here
	// rather than taken from k0s so that retention has something it can
	// positively identify, instead of a glob over somebody else's directory.
	archivePrefix = "corium-backup-"

	// archiveSuffix is what `k0s backup` produces: a gzipped tarball.
	archiveSuffix = ".tar.gz"

	// archiveStamp sorts lexically in chronological order, which is the whole
	// reason retention can order by name instead of by mtime.
	archiveStamp = "20060102T150405Z"
)

// archivePattern matches exactly the names archiveName produces, and nothing
// else. Retention deletes only what this matches: the target directory belongs
// to the operator, and a pass that swept `*.tar.gz` would eventually delete the
// very thing somebody pointed it at.
var archivePattern = regexp.MustCompile(`^corium-backup-\d{8}T\d{6}Z\.tar\.gz$`)

// Backup takes one control plane snapshot into dir and applies retention.
//
// k0s must be running: `k0s backup` asks its status socket for the role first.
func Backup(ctx context.Context, dir string, keep int) error {
	if dir == "" {
		return errors.New("no backup path configured; " +
			"corium-backup.service takes one from a drop-in corium-agent writes " +
			"at first boot from corium.backup")
	}

	// 0700 and not 0755: everything in here is a copy of the cluster CA's
	// private key and of every Secret in etcd.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	path := filepath.Join(dir, archiveName(time.Now()))

	// O_EXCL, and 0600 at creation rather than a chmod afterwards: there must
	// be no moment where an archive of the cluster's secrets exists with a
	// wider mode, and no run may overwrite an archive already on disk.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a name this function coined under a validated directory
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	// `k0s backup --save-path -` streams the archive to stdout, which lets the
	// file above be the one this process created with the mode it wants,
	// instead of picking up whatever k0s left in a directory.
	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, Binary, "backup", "--save-path", "-")
	cmd.Stdout = file
	cmd.Stderr = &stderr

	err = errors.Join(cmd.Run(), file.Close())
	if err == nil {
		err = verifyArchive(path)
	}

	if err != nil {
		// A partial archive is worse than none: it looks like a backup, and
		// retention would count it as one.
		if removeErr := os.Remove(path); removeErr != nil {
			slog.Error("could not remove a failed backup", "path", path, "error", removeErr)
		}

		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("k0s backup: %w: %s", err, message)
		}

		return fmt.Errorf("k0s backup: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// The path and the size, never the contents.
	slog.Info("wrote a control plane backup", "path", path, "bytes", info.Size())

	return prune(dir, keep)
}

// verifyArchive checks that what arrived on stdout is a gzip stream.
//
// k0s logs to stderr and writes the archive to stdout, so this should never
// fire. It exists because the failure it catches is silent: a stray line of log
// output on stdout produces a file that looks like a backup, passes retention,
// and is discovered to be unusable on the day somebody needs it.
func verifyArchive(path string) error {
	file, err := os.Open(path) // #nosec G304 -- a path this process just wrote
	if err != nil {
		return fmt.Errorf("reopening %s: %w", path, err)
	}
	// Read-only, and the file is read whole below: nothing is lost by dropping
	// the close error here.
	defer func() { _ = file.Close() }()

	magic := make([]byte, 2)
	if _, err := io.ReadFull(file, magic); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if magic[0] != 0x1f || magic[1] != 0x8b {
		return errors.New("the archive is not a gzip stream")
	}

	return nil
}

// archiveName is the only name this agent gives an archive.
func archiveName(at time.Time) string {
	return archivePrefix + at.UTC().Format(archiveStamp) + archiveSuffix
}

// prune deletes the archives retention has expired.
func prune(dir string, keep int) error {
	stale, err := staleArchives(dir, keep)
	if err != nil {
		return err
	}

	for _, name := range stale {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing %s: %w", path, err)
		}

		slog.Info("removed an expired backup", "path", path, "keep", keep)
	}

	return nil
}

// staleArchives names the archives in dir beyond the newest keep of them.
//
// It is deliberately narrow. Anything that is not a regular file, and anything
// whose name this agent did not coin, is invisible to it: the target is a
// directory the operator chose, it may well hold their own archives, a mount
// point, or a symlink to something that matters, and a retention bug that
// deletes the wrong file is the failure worth engineering against here.
func staleArchives(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var archives []string

	for _, entry := range entries {
		// Type().IsRegular() is false for a directory and for a symlink, so a
		// link named like an archive is left where it is rather than followed.
		if !entry.Type().IsRegular() || !archivePattern.MatchString(entry.Name()) {
			continue
		}

		archives = append(archives, entry.Name())
	}

	// By name, not by mtime: the timestamp in the name says when the snapshot
	// was taken, while an mtime says when something last touched the file, and
	// a copy or a restore-in-progress touches files.
	sort.Strings(archives)

	// keep <= 0 deletes nothing. Configuration cannot produce it -- the default
	// is seven and a negative count is rejected -- but a retention pass reached
	// with a count it does not understand must do nothing, not everything.
	if keep <= 0 || len(archives) <= keep {
		return nil, nil
	}

	return archives[:len(archives)-keep], nil
}
