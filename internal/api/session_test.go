package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// restart stands in for systemd bringing the daemon back: the same node, the
// same boot, a brand new Enroller.
func restart(t *testing.T, store *Store, dir SessionDir) *Enroller {
	t.Helper()

	enroller, err := NewEnroller(store, RequirePairingCode, dir)
	if err != nil {
		t.Fatalf("NewEnroller() after a restart error = %v", err)
	}

	return enroller
}

func TestSessionKeepsTheCodeAcrossARestart(t *testing.T) {
	// The failure this prevents: a daemon that restarts mints a second code
	// while the first is still printed on the screen above it, and the
	// operator reading the console cannot tell which of the two works.
	store := newTestStore(t)
	dir := SessionDir(t.TempDir())

	first, err := NewEnroller(store, RequirePairingCode, dir)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if got := restart(t, store, dir).Code(); got != first.Code() {
		t.Errorf("code after a restart = %q, want the one already on the console %q",
			got, first.Code())
	}
}

func TestSessionDoesNotHandBackAttempts(t *testing.T) {
	// The attempt limit is what makes a forty-bit code enough. If restarting
	// the daemon reset it, the limit would be five guesses per process rather
	// than five per boot, and the arithmetic behind the whole scheme would be
	// wrong.
	store := newTestStore(t)
	dir := SessionDir(t.TempDir())
	ca := operatorCA(t, "operators")

	enroller, err := NewEnroller(store, RequirePairingCode, dir)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	for attempt := 1; attempt < maxAttempts; attempt++ {
		if err := enroller.Enroll("00000000", ca); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("attempt %d = %v, want %v", attempt, err, ErrWrongCode)
		}
	}

	// One attempt left, and a restart in between.
	resumed := restart(t, store, dir)

	if err := resumed.Enroll("00000000", ca); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("the fifth wrong code after a restart = %v, want %v", err, ErrLockedOut)
	}

	// And the lockout itself survives the next restart, or a guesser would
	// only have to crash the daemon to get another five.
	if restart(t, store, dir).Open() {
		t.Error("Open() = true after a restart into a locked-out session, want false")
	}
}

func TestSessionIsForgottenOnceTheNodeIsClaimed(t *testing.T) {
	store := newTestStore(t)
	dir := SessionDir(t.TempDir())

	enroller, err := NewEnroller(store, RequirePairingCode, dir)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	path := filepath.Join(string(dir), sessionFile)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pairing code is still on the filesystem of a claimed node (%v)", err)
	}
}

func TestSessionStartsAfreshOnANewBoot(t *testing.T) {
	// /run is emptied by a reboot, which is the whole reason the state lives
	// there. A new directory stands in for that.
	store := newTestStore(t)

	first, err := NewEnroller(store, RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	second, err := NewEnroller(store, RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if first.Code() == second.Code() {
		t.Error("the same pairing code was offered on a second boot, want a new one")
	}
}

func TestSessionDiscardsWhatItCannotBelieve(t *testing.T) {
	// A half-written file that decodes to zero attempts left would refuse
	// enrolment for the rest of the boot. Starting again is the safe
	// direction: the code being thrown away is one nobody has used, because a
	// used one would have claimed the node.
	for name, contents := range map[string]string{
		"truncated":                  `{"code":"ABCD`,
		"not JSON":                   "AA6F-WAF3\n",
		"empty":                      "",
		"code of the wrong length":   `{"code":"ABC","attemptsLeft":5}`,
		"code outside the alphabet":  `{"code":"ABCDEFGI","attemptsLeft":5}`,
		"more attempts than allowed": `{"code":"ABCDEFGH","attemptsLeft":99}`,
		"negative attempts":          `{"code":"ABCDEFGH","attemptsLeft":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			dir := SessionDir(t.TempDir())

			if err := os.WriteFile(filepath.Join(string(dir), sessionFile),
				[]byte(contents), 0o600); err != nil {
				t.Fatalf("writing the session file: %v", err)
			}

			enroller, err := NewEnroller(store, RequirePairingCode, dir)
			if err != nil {
				t.Fatalf("NewEnroller() error = %v", err)
			}

			if !enroller.Open() {
				t.Error("Open() = false after an unbelievable session, want a fresh one")
			}

			if !plausibleCode(normaliseCode(enroller.Code())) {
				t.Errorf("Code() = %q, want a freshly minted one", enroller.Code())
			}
		})
	}
}

func TestSessionHonoursALockoutItCanBelieve(t *testing.T) {
	// The mirror of the test above, and the reason it cannot simply start
	// afresh whenever the count is inconvenient: zero attempts left in a sound
	// file is not corruption, it is a node that has been guessed at.
	store := newTestStore(t)
	dir := SessionDir(t.TempDir())

	encoded, err := json.Marshal(sessionState{Code: "ABCDEFGH", AttemptsLeft: 0})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(string(dir), sessionFile), encoded, 0o600); err != nil {
		t.Fatalf("writing the session file: %v", err)
	}

	enroller, err := NewEnroller(store, RequirePairingCode, dir)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if enroller.Open() {
		t.Error("Open() = true on a session that had run out of attempts, want false")
	}
}

func TestSessionFileIsNotReadableByEveryone(t *testing.T) {
	store := newTestStore(t)
	dir := SessionDir(filepath.Join(t.TempDir(), "run"))

	if _, err := NewEnroller(store, RequirePairingCode, dir); err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(string(dir), sessionFile))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("session file mode = %o, want 600", mode)
	}

	directory, err := os.Stat(string(dir))
	if err != nil {
		t.Fatalf("Stat() on the directory error = %v", err)
	}

	if mode := directory.Mode().Perm(); mode != 0o700 {
		t.Errorf("session directory mode = %o, want 700", mode)
	}
}

func TestSessionIsOptional(t *testing.T) {
	// An empty directory means "do not keep any of this", which is what a
	// caller with nowhere writable to put it gets. It must still work.
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode, "")
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
		t.Errorf("Enroll() error = %v", err)
	}
}

func TestSessionIsNotKeptForANodeThatAsksNothing(t *testing.T) {
	// api.insecure has no code to remember and no attempt that can be wrong,
	// so there is nothing to write down.
	store := newTestStore(t)
	dir := SessionDir(t.TempDir())

	if _, err := NewEnroller(store, OpenToAnyone, dir); err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(string(dir), sessionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a session was written for a node that asks nothing (%v)", err)
	}
}
