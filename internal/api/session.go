package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// SessionDir is where a node keeps the enrolment state that must last exactly
// one boot: the pairing code it is offering, and how many wrong ones it has
// been sent.
//
// It is a named type rather than a string because it travels next to a listen
// address, and two adjacent string parameters that mean entirely different
// things are a swap waiting to happen.
type SessionDir string

// DefaultSessionDir is under /run, and that choice is the whole point.
//
// The state below has to survive this daemon restarting and must not survive
// the machine rebooting, and the two obvious homes each get exactly one half
// right.
//
// Memory loses it on a restart. systemd is configured to bring this daemon
// back, so a crash would mint a second pairing code while the first is still
// on the screen above it, and an operator reading the wrong one spends attempts
// they are limited to five of. Worse, the counter itself would go back to five,
// which turns "five guesses per boot" -- the number the whole scheme leans on
// -- into "five guesses per process".
//
// /var survives the reboot, which is the failure the other way round: a pairing
// code in a disk image is a pairing code anybody holding that image can read,
// and an unclaimed node is exactly the one whose image gets passed around.
//
// /run is a tmpfs. It survives a restart, the reboot empties it, and nothing in
// it is ever written to a disk.
const DefaultSessionDir SessionDir = "/run/corium"

const sessionFile = "enrolment.json"

// session is the per-boot enrolment state, as a file.
type session struct {
	dir SessionDir
}

// sessionState is what that file holds.
//
// Only what cannot be derived from anywhere else. Whether the node is claimed
// is not here: that is durable, it lives in the store under /var, and a second
// copy in /run would be a second answer to a question that must have one.
type sessionState struct {
	Code         string `json:"code"`
	AttemptsLeft int    `json:"attemptsLeft"`
}

func (s session) path() string { return filepath.Join(string(s.dir), sessionFile) }

// load returns the state this boot already established, and whether there was
// any.
//
// Anything missing, unreadable or implausible is reported as "no session"
// rather than as an error, and the daemon starts a fresh one. The case worth
// naming is a truncated write that decodes to zero attempts left: taken at
// face value it would refuse enrolment for the rest of the boot, locking an
// operator out of a node over a half-written file. Starting again is the safe
// direction, and it costs nothing -- the code being discarded is one that
// nobody has successfully used, because a used one would have claimed the node.
//
// Zero attempts left in a file that is otherwise sound is honoured, because
// that is not a corrupt session, it is a node that has been guessed at.
func (s session) load() (sessionState, bool) {
	if s.dir == "" {
		return sessionState{}, false
	}

	raw, err := os.ReadFile(s.path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("reading the enrolment session", "error", err)
		}

		return sessionState{}, false
	}

	var state sessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Debug("the enrolment session is not readable", "error", err)

		return sessionState{}, false
	}

	if !plausibleCode(state.Code) || state.AttemptsLeft < 0 || state.AttemptsLeft > maxAttempts {
		slog.Debug("the enrolment session does not make sense; starting a new one")

		return sessionState{}, false
	}

	return state, true
}

// save records the state for the rest of this boot.
//
// Best effort, because there is a working node on the other side of a failure
// here: one that mints a new code if this daemon restarts, which is what it did
// before any of this existed. It is reported at warning level rather than debug
// because the guarantee being lost is one an operator is entitled to rely on.
func (s session) save(state sessionState) {
	if s.dir == "" {
		return
	}

	if err := s.write(state); err != nil {
		slog.Warn("could not record the enrolment session; "+
			"the pairing code will change if this daemon restarts",
			"path", s.path(), "error", err)
	}
}

func (s session) write(state sessionState) error {
	// 0700: the pairing code is in here, and a node that has not been claimed
	// is not a node whose local accounts should be able to claim it.
	if err := os.MkdirAll(string(s.dir), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", s.dir, err)
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}

	// Written to a temporary file and renamed, so that a daemon dying
	// mid-write leaves the previous session rather than a truncated one. The
	// load path above tolerates a truncated file; not producing one is better
	// than tolerating it.
	temporary, err := os.CreateTemp(string(s.dir), "."+sessionFile+".*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(temporary.Name()) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()

		return err
	}

	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()

		return err
	}

	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(temporary.Name(), s.path())
}

// clear forgets the session, once it can no longer be used.
func (s session) clear() {
	if s.dir == "" {
		return
	}

	if err := os.Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Debug("removing the enrolment session", "error", err)
	}
}

// plausibleCode reports whether a string could be a code this node minted.
//
// It checks shape and alphabet only. There is nothing to authenticate here --
// the file is root-owned in a root-only directory, and anybody who can rewrite
// it can restart the daemon holding it anyway. The point is to notice a file
// that is not a pairing code before it is offered to an operator as one.
func plausibleCode(code string) bool {
	if len(code) != codeLength {
		return false
	}

	for _, symbol := range code {
		if !strings.ContainsRune(codeAlphabet, symbol) {
			return false
		}
	}

	return true
}
