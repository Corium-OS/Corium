// Package access manages the SSH public keys the management API trusts for a
// user that already exists on the node.
//
// It is deliberately narrow. It never creates a user, sets a password, chooses
// a shell, or grants sudo -- those stay cloud-init's, so cloud-init remains the
// only authority over an account (decision 7). What this package owns is key
// material, and it owns it in its own file, /var/lib/corium/ssh/<user>, which
// sshd reads alongside the user's own ~/.ssh/authorized_keys. The two are never
// the same file, so listing and revoking here never touch a key the operator
// placed by hand. See docs/adr/0005-ssh-access-over-the-api.md.
package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// KeysDir is where Corium keeps the keys it manages, one file per user.
const KeysDir = "/var/lib/corium/ssh"

// sshKeyContext is the SELinux type sshd will read an authorized-keys file
// under. A file outside a home directory carries the wrong type by default, and
// sshd_t is not permitted to read it; ssh_home_t is what ~/.ssh already has.
const sshKeyContext = "ssh_home_t"

// The modes sshd needs to read these files, which are wider than they look
// like they should be. See writeLines for why, and for why that costs nothing.
const (
	// keyFileMode is 0644 because sshd reads the file as the user logging in.
	keyFileMode = 0o644

	// keyDirMode is 0711: every component of the path must be traversable by
	// that user, but nobody needs to list which users have keys.
	keyDirMode = 0o711
)

// commandTimeout bounds the SELinux relabel, which either answers at once or is
// a tool that is not installed.
const commandTimeout = 30 * time.Second

var (
	// ErrUnknownUser reports a key aimed at an account that does not exist. The
	// message names the way round it: the account is cloud-init's to create,
	// not this API's.
	ErrUnknownUser = errors.New(
		"no such user on this node; create it with cloud-init before adding a key for it")

	// ErrNoSuchKey reports a fingerprint that is not installed.
	ErrNoSuchKey = errors.New("no key with that fingerprint is trusted on this node")

	// ErrEmptyKey reports a request that carried no key at all.
	ErrEmptyKey = errors.New("no key was given")
)

// Runner executes a command. It exists so this package can be tested without a
// machine whose files it would relabel.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Key is one trusted public key, named by its SHA-256 fingerprint.
//
// It carries no private material and the fingerprint is not a secret: a public
// key is meant to be seen, and being able to read what a node trusts is the
// point of List.
type Key struct {
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
}

// Manager reads and writes the keys the API trusts.
type Manager struct {
	// Dir overrides KeysDir. Empty means the real one.
	Dir string

	// LookupUser reports whether a user exists, returning ErrUnknownUser if
	// not. Nil means ask the operating system.
	LookupUser func(name string) error

	// Run executes commands, for the SELinux relabel. Nil means execute them.
	Run Runner

	// mu serialises writes: List, Add and Revoke each read-modify-write a
	// per-user file, and two Adds racing would otherwise lose one of the keys.
	mu sync.Mutex
}

func (m *Manager) dir() string {
	if m.Dir != "" {
		return m.Dir
	}

	return KeysDir
}

// Add trusts a public key for a user, and returns it by fingerprint.
//
// Adding a key that is already trusted for that user is a no-op that still
// reports success: the same key twice is one key, and idempotence is what makes
// a retried request safe.
func (m *Manager) Add(ctx context.Context, username, authorizedKey string) (Key, error) {
	if strings.TrimSpace(authorizedKey) == "" {
		return Key{}, ErrEmptyKey
	}

	if err := validName(username); err != nil {
		return Key{}, err
	}

	if err := m.userExists(username); err != nil {
		return Key{}, err
	}

	public, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return Key{}, fmt.Errorf("that is not a usable SSH public key: %w", err)
	}

	key := Key{
		User:        username,
		Fingerprint: ssh.FingerprintSHA256(public),
		Type:        public.Type(),
		Comment:     comment,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	lines, err := m.readLines(username)
	if err != nil {
		return Key{}, err
	}

	for _, line := range lines {
		if fingerprintOf(line) == key.Fingerprint {
			return key, nil
		}
	}

	if err := m.writeLines(ctx, username, append(lines, canonical(public, comment))); err != nil {
		return Key{}, err
	}

	return key, nil
}

// List reports every trusted key, across every user, sorted so the output is
// stable.
func (m *Manager) List(_ context.Context) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	users, err := m.users()
	if err != nil {
		return nil, err
	}

	var keys []Key

	for _, username := range users {
		lines, err := m.readLines(username)
		if err != nil {
			return nil, err
		}

		for _, line := range lines {
			public, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
			if err != nil {
				// A line this daemon did not write, or wrote in an older shape.
				// Skipped rather than fatal: one bad line must not make the whole
				// list unreadable.
				slog.Warn("skipping an unreadable ssh key line", "user", username, "error", err)

				continue
			}

			keys = append(keys, Key{
				User:        username,
				Fingerprint: ssh.FingerprintSHA256(public),
				Type:        public.Type(),
				Comment:     comment,
			})
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].User != keys[j].User {
			return keys[i].User < keys[j].User
		}

		return keys[i].Fingerprint < keys[j].Fingerprint
	})

	return keys, nil
}

// Revoke stops the node trusting the key with the given fingerprint, wherever
// it is trusted, and returns what it removed.
//
// A fingerprint is a hash of the key, so it is the same for every user the key
// was added to: revoking is "this key is no longer trusted here", not "for this
// one account". A fingerprint that is trusted nowhere is ErrNoSuchKey.
func (m *Manager) Revoke(ctx context.Context, fingerprint string) ([]Key, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return nil, ErrNoSuchKey
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	users, err := m.users()
	if err != nil {
		return nil, err
	}

	var removed []Key

	for _, username := range users {
		lines, err := m.readLines(username)
		if err != nil {
			return nil, err
		}

		kept := make([]string, 0, len(lines))

		for _, line := range lines {
			public, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
			if err == nil && ssh.FingerprintSHA256(public) == fingerprint {
				removed = append(removed, Key{
					User:        username,
					Fingerprint: fingerprint,
					Type:        public.Type(),
					Comment:     comment,
				})

				continue
			}

			kept = append(kept, line)
		}

		if len(kept) == len(lines) {
			continue
		}

		if err := m.replace(ctx, username, kept); err != nil {
			return nil, err
		}
	}

	if len(removed) == 0 {
		return nil, ErrNoSuchKey
	}

	return removed, nil
}

// Purge removes every key the node was trusting. It is what a reset calls: the
// keys the API added leave with the enrolment that authorised them.
func (m *Manager) Purge() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.RemoveAll(m.dir()); err != nil {
		return fmt.Errorf("removing %s: %w", m.dir(), err)
	}

	return nil
}

// replace rewrites a user's file with kept, removing the file entirely when
// nothing is left rather than leaving an empty one behind.
func (m *Manager) replace(ctx context.Context, username string, kept []string) error {
	if len(kept) == 0 {
		path := filepath.Join(m.dir(), username)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}

		return nil
	}

	return m.writeLines(ctx, username, kept)
}

// Reconcile repairs what the rest of the system undoes, and is meant to be
// called once when the daemon starts.
//
// Two things drift, and both end the same way: sshd stops reading a key it was
// told to trust, and says nothing about why.
//
// The SELinux label is the one that actually moves. `restorecon -R /var`, a
// policy update, or a filesystem relabel resets these files to the default type
// for /var, which sshd_t may not read -- measured on a node, where a relabel
// turned a working login into "Permission denied" with no message anywhere. The
// label is reapplied here so that a machine which has been relabelled repairs
// itself at the next boot.
//
// The modes matter on a node upgraded into this feature rather than installed
// with it: /var/lib/corium was 0700 before, and a directory sshd cannot walk
// through makes every key under it invisible. tmpfiles corrects the directories
// at boot; the files are corrected here.
//
// None of it is fatal. A node whose keys are mislabelled still has a working
// API to fix it with, and refusing to start would take that away too.
func (m *Manager) Reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	users, err := m.users()
	if err != nil || len(users) == 0 {
		// Nothing written yet, or nothing readable. Either way there is no key
		// on this node for sshd to fail to read.
		return
	}

	directory := m.dir()

	if err := m.allowTraversal(filepath.Dir(directory)); err != nil {
		slog.Warn("could not make the path to the ssh keys traversable", "error", err)
	}

	m.relabel(ctx, directory)

	for _, username := range users {
		path := filepath.Join(directory, username)

		if err := os.Chmod(path, keyFileMode); err != nil {
			slog.Warn("could not set the mode on an ssh key file; "+
				"sshd reads it as the user logging in and may not be able to",
				"path", path, "error", err)
		}

		m.relabel(ctx, path)
	}

	slog.Info("reapplied the mode and SELinux label on the trusted ssh keys",
		"users", len(users))
}

// users lists the accounts that have a key file, or nothing on a node that has
// never been given one.
func (m *Manager) users() ([]string, error) {
	entries, err := os.ReadDir(m.dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.dir(), err)
	}

	var users []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		users = append(users, entry.Name())
	}

	return users, nil
}

// readLines returns a user's keys, one per line, with blanks and comments
// dropped. A user with no file yet is not an error: it is a user with no keys.
func (m *Manager) readLines(username string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(m.dir(), username)) //nolint:gosec // G304: username is validated and the directory is the package's own
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading the keys for %q: %w", username, err)
	}

	var lines []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		lines = append(lines, line)
	}

	return lines, nil
}

// writeLines replaces a user's file atomically and relabels it for sshd.
//
// Root-owned, and readable by everyone. That last part is not a relaxation to
// be apologised for, it is what makes the feature work at all: sshd opens
// AuthorizedKeysFile **as the user logging in**, not as root, so a file the
// user cannot read is a file sshd silently skips. Shipping it 0600 in a 0700
// directory produced exactly that -- the key was written, `list` showed it, the
// journal said it was trusted, and every login was refused.
//
// StrictModes is the rule that governs the mode here, and it refuses a path
// others can *write*, not one others can read. 0644 satisfies it. Nothing is
// given away: a public key is not a secret, which is the same reason this API
// will hand the list of them to a readonly client.
//
// A crash mid-write leaves the old file or the new one, never a half-written
// one.
func (m *Manager) writeLines(ctx context.Context, username string, lines []string) error {
	directory := m.dir()

	if err := os.MkdirAll(directory, keyDirMode); err != nil {
		return fmt.Errorf("creating %s: %w", directory, err)
	}

	// MkdirAll leaves an existing directory's mode alone, so a directory created
	// by an earlier release -- or by the tmpfiles line that predates this fix --
	// keeps a mode sshd cannot traverse. Setting it here is what repairs a node
	// upgraded into this rather than installed with it.
	if err := os.Chmod(directory, keyDirMode); err != nil { //nolint:gosec // G302: see keyDirMode
		return fmt.Errorf("setting the mode on %s: %w", directory, err)
	}

	// And the parent, for the same reason: every component of the path has to be
	// traversable by the user, or the one below it might as well not exist.
	// Only the execute bit is added, so /var/lib/corium stays unlistable and
	// everything under it keeps its own mode -- the API's own state directory,
	// which holds this node's serving key, is 0700 and stays shut.
	if err := m.allowTraversal(filepath.Dir(directory)); err != nil {
		return err
	}

	path := filepath.Join(directory, username)
	content := strings.Join(lines, "\n") + "\n"

	temporary, err := os.CreateTemp(directory, "."+username+".*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", directory, err)
	}

	defer func() { _ = os.Remove(temporary.Name()) }()

	if err := temporary.Chmod(keyFileMode); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("setting mode on the keys for %q: %w", username, err)
	}

	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("writing the keys for %q: %w", username, err)
	}

	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("flushing the keys for %q: %w", username, err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing the keys for %q: %w", username, err)
	}

	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("installing the keys for %q: %w", username, err)
	}

	m.relabel(ctx, directory)
	m.relabel(ctx, path)

	return nil
}

// allowTraversal adds the execute bit for others to a directory, so that sshd
// -- running as the user logging in -- can walk through it to the keys below.
//
// Only that bit, and only on this one directory. /var/lib/corium is 0700 by
// design and holds things that must stay closed; 0711 opens the door without
// opening the drawers, since traversal is not listing and every child keeps its
// own mode. Read access to anything inside still has to be granted by that
// thing itself.
func (m *Manager) allowTraversal(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Nothing to traverse yet. The directory below was just created, so
			// this cannot happen on the real path; a test pointing somewhere
			// shallow can reach it, and there it is not a problem.
			return nil
		}

		return fmt.Errorf("reading the mode of %s: %w", directory, err)
	}

	mode := info.Mode().Perm()
	if mode&0o001 != 0 {
		return nil
	}

	if err := os.Chmod(directory, mode|0o001); err != nil {
		return fmt.Errorf("making %s traversable: %w", directory, err)
	}

	return nil
}

// relabel gives a path the SELinux type sshd will read.
//
// Best effort, and loud when it fails: on a node with SELinux disabled, or in a
// test, chcon is a no-op or absent, and losing the key that was just written
// would be a worse answer than a warning. On an enforcing node a failure here
// is why a key that was accepted will not let anybody in, so it is logged where
// somebody debugging that will find it.
func (m *Manager) relabel(ctx context.Context, path string) {
	if _, err := m.run(ctx, "chcon", "-t", sshKeyContext, path); err != nil {
		slog.Warn("could not label an ssh key file for SELinux; "+
			"sshd may refuse to read it on an enforcing node",
			"path", path, "context", sshKeyContext, "error", err)
	}
}

func (m *Manager) userExists(name string) error {
	if m.LookupUser != nil {
		return m.LookupUser(name)
	}

	if _, err := user.Lookup(name); err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return fmt.Errorf("%q: %w", name, ErrUnknownUser)
		}

		return fmt.Errorf("looking up %q: %w", name, err)
	}

	return nil
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}

// canonical renders a public key the way it will be stored: the marshalled key,
// with the comment kept because that is what makes a list legible to a person.
func canonical(public ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public)))
	if comment != "" {
		line += " " + comment
	}

	return line
}

// fingerprintOf is the SHA-256 fingerprint of an authorized_keys line, or the
// empty string for a line that does not parse -- which never matches a real
// fingerprint, so an unreadable line is simply not a duplicate of anything.
func fingerprintOf(line string) string {
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return ""
	}

	return ssh.FingerprintSHA256(public)
}

// validName rejects anything that would not be a single path element, so the
// user name -- which becomes a file name -- can never escape the keys
// directory. A name that fails here is reported as unknown, which it is.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`+"\x00") {
		return fmt.Errorf("%q: %w", name, ErrUnknownUser)
	}

	return nil
}
