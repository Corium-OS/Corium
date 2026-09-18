package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// allow accepts any user, standing in for a machine where the accounts exist. A
// test that cares about a missing user overrides LookupUser.
func allow(string) error { return nil }

func newManager(t *testing.T) *Manager {
	t.Helper()

	return &Manager{
		Dir:        t.TempDir(),
		LookupUser: allow,
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
}

// publicKey mints a fresh ed25519 authorized_keys line.
func publicKey(t *testing.T, comment string) string {
	t.Helper()

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	wrapped, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("wrapping key: %v", err)
	}

	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(wrapped)))
	if comment != "" {
		line += " " + comment
	}

	return line
}

func TestAddThenList(t *testing.T) {
	m := newManager(t)

	added, err := m.Add(context.Background(), "alice", publicKey(t, "alice@laptop"))
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if added.User != "alice" || added.Type != "ssh-ed25519" || added.Comment != "alice@laptop" {
		t.Errorf("Add() = %+v, want alice/ssh-ed25519/alice@laptop", added)
	}

	if !strings.HasPrefix(added.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256: fingerprint", added.Fingerprint)
	}

	keys, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 1 || keys[0].Fingerprint != added.Fingerprint {
		t.Errorf("List() = %+v, want the one key just added", keys)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	m := newManager(t)
	key := publicKey(t, "")

	first, err := m.Add(context.Background(), "alice", key)
	if err != nil {
		t.Fatalf("first Add() error = %v", err)
	}

	second, err := m.Add(context.Background(), "alice", key)
	if err != nil {
		t.Fatalf("second Add() error = %v", err)
	}

	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprints differ across two adds of one key: %q vs %q",
			first.Fingerprint, second.Fingerprint)
	}

	keys, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 1 {
		t.Errorf("List() has %d keys after adding one twice, want 1", len(keys))
	}
}

func TestAddUnknownUserIsRefused(t *testing.T) {
	m := newManager(t)
	m.LookupUser = func(name string) error {
		return fmt.Errorf("%q: %w", name, ErrUnknownUser)
	}

	if _, err := m.Add(context.Background(), "ghost", publicKey(t, "")); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("Add() error = %v, want ErrUnknownUser", err)
	}

	if _, err := os.Stat(filepath.Join(m.dir(), "ghost")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a key file was written for a user that does not exist")
	}
}

func TestAddEmptyKeyIsRefused(t *testing.T) {
	m := newManager(t)

	if _, err := m.Add(context.Background(), "alice", "   \n"); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("Add() error = %v, want ErrEmptyKey", err)
	}
}

func TestAddGarbageKeyIsRefused(t *testing.T) {
	m := newManager(t)

	_, err := m.Add(context.Background(), "alice", "ssh-ed25519 this-is-not-base64!!")
	switch {
	case err == nil:
		t.Error("Add() accepted a key that does not parse")
	case errors.Is(err, ErrEmptyKey), errors.Is(err, ErrUnknownUser):
		t.Errorf("Add() error = %v, want a parse error rather than empty/unknown-user", err)
	}
}

func TestAddRejectsAUserThatWouldEscapeTheDirectory(t *testing.T) {
	m := newManager(t)

	if _, err := m.Add(context.Background(), "../evil", publicKey(t, "")); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("Add() error = %v, want ErrUnknownUser for a traversing name", err)
	}
}

func TestRevokeRemovesOneAndKeepsTheRest(t *testing.T) {
	m := newManager(t)

	one, err := m.Add(context.Background(), "alice", publicKey(t, "one"))
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	two, err := m.Add(context.Background(), "alice", publicKey(t, "two"))
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	removed, err := m.Revoke(context.Background(), one.Fingerprint)
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	if len(removed) != 1 || removed[0].Fingerprint != one.Fingerprint {
		t.Errorf("Revoke() removed %+v, want just the first key", removed)
	}

	keys, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 1 || keys[0].Fingerprint != two.Fingerprint {
		t.Errorf("List() = %+v, want only the key that was not revoked", keys)
	}
}

func TestRevokingTheLastKeyRemovesTheFile(t *testing.T) {
	m := newManager(t)

	only, err := m.Add(context.Background(), "alice", publicKey(t, ""))
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if _, err := m.Revoke(context.Background(), only.Fingerprint); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(m.dir(), "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Error("an empty key file was left behind after revoking the last key")
	}
}

func TestRevokeUnknownFingerprint(t *testing.T) {
	m := newManager(t)

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if _, err := m.Revoke(context.Background(), "SHA256:not-a-real-fingerprint"); !errors.Is(err, ErrNoSuchKey) {
		t.Errorf("Revoke() error = %v, want ErrNoSuchKey", err)
	}
}

func TestRevokeReachesEveryUserThatTrustsTheKey(t *testing.T) {
	m := newManager(t)
	key := publicKey(t, "shared")

	alice, err := m.Add(context.Background(), "alice", key)
	if err != nil {
		t.Fatalf("Add(alice) error = %v", err)
	}

	bob, err := m.Add(context.Background(), "bob", key)
	if err != nil {
		t.Fatalf("Add(bob) error = %v", err)
	}

	if alice.Fingerprint != bob.Fingerprint {
		t.Fatalf("the same key produced two fingerprints: %q vs %q", alice.Fingerprint, bob.Fingerprint)
	}

	removed, err := m.Revoke(context.Background(), alice.Fingerprint)
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	if len(removed) != 2 {
		t.Errorf("Revoke() removed %d entries, want 2 (one per user)", len(removed))
	}

	keys, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 0 {
		t.Errorf("List() = %+v, want empty after revoking the shared key everywhere", keys)
	}
}

func TestPurgeRemovesEverything(t *testing.T) {
	m := newManager(t)

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := m.Purge(); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}

	if _, err := os.Stat(m.dir()); !errors.Is(err, os.ErrNotExist) {
		t.Error("Purge() left the keys directory behind")
	}

	keys, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(keys) != 0 {
		t.Errorf("List() = %+v after Purge(), want empty", keys)
	}
}

func TestKeyFileIsReachableByTheUserLoggingIn(t *testing.T) {
	// This test exists because the obvious mode is the wrong one, and the wrong
	// one fails in a way nothing local catches.
	//
	// sshd opens AuthorizedKeysFile as the user who is logging in, not as root.
	// A 0600 file in a 0700 directory is therefore a file sshd silently skips:
	// on a real node the key was written, `list` returned it, the journal said
	// it was trusted, and every login was refused with "Permission denied".
	//
	// So these are asserted as reachable rather than as closed. What must stay
	// true is StrictModes, which refuses a path others can *write* -- and
	// nothing is being given away, because a public key is not a secret.
	m := newManager(t)

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(m.dir(), "alice"))
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}

	if mode := info.Mode().Perm(); mode&0o004 == 0 {
		t.Errorf("key file mode = %o, want it readable by the user sshd becomes", mode)
	}

	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		t.Errorf("key file mode = %o, want it unwritable by group and others "+
			"(sshd StrictModes refuses that)", mode)
	}

	dir, err := os.Stat(m.dir())
	if err != nil {
		t.Fatalf("stat keys directory: %v", err)
	}

	if mode := dir.Mode().Perm(); mode&0o001 == 0 {
		t.Errorf("keys directory mode = %o, want it traversable by that user", mode)
	}

	if mode := dir.Mode().Perm(); mode&0o022 != 0 {
		t.Errorf("keys directory mode = %o, want it unwritable by group and others", mode)
	}
}

func TestWritingAKeyOpensTheWayThroughTheParent(t *testing.T) {
	// Every component of the path has to be traversable, so the directory above
	// the keys gets the execute bit too -- which matters most on a node
	// upgraded into this feature, where /var/lib/corium already exists as 0700
	// and tmpfiles will not narrow or widen what is already there.
	m := newManager(t)

	parent := filepath.Dir(m.dir())
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("preparing the parent: %v", err)
	}

	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("closing the parent: %v", err)
	}

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}

	if mode := info.Mode().Perm(); mode&0o001 == 0 {
		t.Errorf("parent mode = %o, want the execute bit so sshd can walk through", mode)
	}

	// Traversal only. The parent holds Corium's other state, including the
	// directory with this node's serving key, and opening it to listing would
	// be a different and much larger claim.
	if mode := info.Mode().Perm(); mode&0o004 != 0 {
		t.Errorf("parent mode = %o, want it traversable but not listable", mode)
	}
}

func TestWritingAKeyRelabelsItForSELinux(t *testing.T) {
	var calls [][]string

	m := &Manager{
		Dir:        t.TempDir(),
		LookupUser: allow,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))

			return nil, nil
		},
	}

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	labelled := false

	for _, call := range calls {
		if call[0] == "chcon" && contains(call, sshKeyContext) {
			labelled = true
		}
	}

	if !labelled {
		t.Errorf("no chcon relabel to %s happened; calls = %v", sshKeyContext, calls)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}

	return false
}

func TestReconcileRepairsALabelSomethingElseReset(t *testing.T) {
	// The failure this answers was measured on a node: `restorecon -R /var`
	// reset the key file to the default type for /var, sshd stopped reading it,
	// and the login was refused with nothing said in any log. Reapplying the
	// label when the daemon starts is what lets a relabelled machine repair
	// itself at the next boot.
	var relabelled []string

	m := newManager(t)
	m.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "chcon" {
			relabelled = append(relabelled, args[len(args)-1])
		}

		return nil, nil
	}

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	relabelled = nil

	m.Reconcile(context.Background())

	path := filepath.Join(m.dir(), "alice")
	if !slices.Contains(relabelled, path) {
		t.Errorf("relabelled = %v, want it to include %s", relabelled, path)
	}
}

func TestReconcileRepairsAModeAnOlderReleaseWrote(t *testing.T) {
	// A node upgraded into this feature has files an earlier release wrote
	// 0600, and directories it created 0700. Nothing else will widen them: the
	// operator adds no new key, so the write path never runs, and every key
	// already trusted stays invisible to sshd.
	m := newManager(t)

	if _, err := m.Add(context.Background(), "alice", publicKey(t, "")); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	path := filepath.Join(m.dir(), "alice")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("narrowing the key file: %v", err)
	}

	if err := os.Chmod(filepath.Dir(m.dir()), 0o700); err != nil {
		t.Fatalf("closing the parent: %v", err)
	}

	m.Reconcile(context.Background())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}

	if mode := info.Mode().Perm(); mode&0o004 == 0 {
		t.Errorf("key file mode = %o after Reconcile, want it readable by the user", mode)
	}

	parent, err := os.Stat(filepath.Dir(m.dir()))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}

	if mode := parent.Mode().Perm(); mode&0o001 == 0 {
		t.Errorf("parent mode = %o after Reconcile, want it traversable", mode)
	}
}

func TestReconcileDoesNothingOnANodeWithNoKeys(t *testing.T) {
	// The overwhelmingly common case: no key was ever added, so there is no
	// directory, and starting the daemon must not create one.
	var ran bool

	m := newManager(t)
	m.Run = func(context.Context, string, ...string) ([]byte, error) {
		ran = true

		return nil, nil
	}

	if err := os.RemoveAll(m.dir()); err != nil {
		t.Fatalf("removing the keys directory: %v", err)
	}

	m.Reconcile(context.Background())

	if ran {
		t.Error("Reconcile() relabelled something on a node with no keys")
	}

	if _, err := os.Stat(m.dir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Reconcile() created the keys directory (%v)", err)
	}
}
