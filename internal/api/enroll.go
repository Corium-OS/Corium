package api

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// maxAttempts is how many wrong pairing codes a node accepts before it stops
// offering to be claimed until the next boot.
//
// Five is chosen from both ends. An operator who has mistyped a code five
// times will not mind rebooting; a script working through the keyspace gets
// five guesses out of 2^40 per boot, and each boot costs it whatever a reboot
// costs -- which, on somebody else's machine, is a great deal.
const maxAttempts = 5

var (
	// ErrWrongCode reports a pairing code that did not match. It deliberately
	// says nothing about how close it was.
	ErrWrongCode = errors.New("pairing code is not correct")

	// ErrLockedOut reports that too many were wrong.
	ErrLockedOut = fmt.Errorf("too many incorrect pairing codes; reboot the node to try again")
)

// Enrolment says what a node asks of somebody claiming it.
type Enrolment int

const (
	// RequirePairingCode is the default: the code printed on the console must
	// be presented. See docs/adr/0004-management-api.md for why this is the
	// default rather than the alternative below.
	RequirePairingCode Enrolment = iota

	// OpenToAnyone asks for nothing. The first client to reach the node claims
	// it, and owns it for the rest of its life.
	//
	// What keeps this from being reckless where it is used is the rule the
	// rest of the design enforces anyway: an unclaimed node is in no cluster,
	// so winning the race gets a bare machine. What it does get is that
	// machine's future, since the CA it pins is the CA it will obey.
	OpenToAnyone
)

// Enroller is the one thing an unclaimed node will do for a stranger.
//
// It is safe for concurrent use, which is not a formality: the whole point of
// the attempt limit is that it holds when somebody is trying codes in
// parallel.
type Enroller struct {
	store *Store
	how   Enrolment

	mu           sync.Mutex
	code         string
	attemptsLeft int
	claimed      bool
}

// NewEnroller mints a pairing code and prepares to accept exactly one
// successful enrolment.
//
// It refuses to exist on a node that is already enrolled. That refusal is the
// stickiness the design depends on: a claimed node must not offer itself again
// after a reboot, or the pairing code would be guarding a door that reopens on
// its own.
func NewEnroller(store *Store, how Enrolment) (*Enroller, error) {
	enrolled, err := store.Enrolled()
	if err != nil {
		return nil, err
	}

	if enrolled {
		return nil, ErrAlreadyEnrolled
	}

	code, err := newCode()
	if err != nil {
		return nil, fmt.Errorf("minting pairing code: %w", err)
	}

	return &Enroller{store: store, how: how, code: code, attemptsLeft: maxAttempts}, nil
}

// OpenToAnyone reports whether this node asks nothing of a claimant.
func (e *Enroller) OpenToAnyone() bool { return e.how == OpenToAnyone }

// Code is the pairing code, grouped as it is printed on the console. It is
// empty on a node that asks for nothing, because there is nothing to print.
func (e *Enroller) Code() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.how == OpenToAnyone {
		return ""
	}

	return formatCode(e.code)
}

// Open reports whether the node can still be claimed.
func (e *Enroller) Open() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return !e.claimed && e.attemptsLeft > 0
}

// Enroll claims the node for the operator holding the CA.
//
// The code is checked before the certificate is looked at, because the code is
// the authentication and everything after it is work done on behalf of someone
// who has proved they are at the console.
//
// A wrong code costs an attempt. A correct code with an unusable certificate
// does not: the caller is the legitimate operator with a bad file, and burning
// their remaining attempts over a typo in a path would turn a small mistake
// into a trip to the console.
func (e *Enroller) Enroll(typedCode string, operatorCA []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch {
	case e.claimed:
		return ErrAlreadyEnrolled
	case e.attemptsLeft <= 0:
		return ErrLockedOut
	}

	if e.how == OpenToAnyone {
		// Nothing was asked and nothing was proved. The claim is recorded as
		// such so that afterwards anybody looking at this node can see that
		// its ownership was established by whoever got there first.
		slog.Warn("accepting an unauthenticated enrolment",
			"reason", "api.insecure is set on this node")
	} else if !codeMatches(e.code, typedCode) {
		e.attemptsLeft--

		// Logged without the code that was tried. A journal is read by more
		// people than the operator, and a failed attempt is often a correct
		// code sent to the wrong node.
		slog.Warn("rejected an enrolment attempt",
			"reason", "pairing code did not match",
			"attemptsLeft", e.attemptsLeft)

		if e.attemptsLeft <= 0 {
			slog.Error("enrolment is closed until the next boot",
				"attempts", maxAttempts)

			return ErrLockedOut
		}

		return ErrWrongCode
	}

	if err := e.store.Adopt(operatorCA); err != nil {
		slog.Warn("enrolment presented an unusable operator CA", "error", err)

		return err
	}

	e.claimed = true

	if err := e.store.RecordClaim(e.method()); err != nil {
		return err
	}

	certificate, err := e.store.OperatorCA()
	if err != nil {
		return err
	}

	slog.Info("node enrolled",
		"operatorCA", certificate.Subject.CommonName,
		"fingerprint", Fingerprint(certificate.Raw))

	return nil
}

func (e *Enroller) method() ClaimMethod {
	if e.how == OpenToAnyone {
		return ClaimedOpenly
	}

	return ClaimedWithPairingCode
}

// Banner is what an unclaimed node prints on the console, the serial port and
// the journal.
//
// Both halves of the trust are in it: the code authenticates the operator to
// the node, and the fingerprint authenticates the node to the operator. The
// console is the channel an attacker on the network does not have, which is
// the only reason either value can be published this way.
func (e *Enroller) Banner(address, fingerprint string) string {
	var out strings.Builder

	out.WriteString("\nCorium node is unenrolled and is not in a cluster.\n\n")
	fmt.Fprintf(&out, "  address       %s\n", address)

	if e.OpenToAnyone() {
		fmt.Fprintf(&out, "  fingerprint   %s\n\n", fingerprint)
		fmt.Fprintf(&out, "  cctl enroll %s\n\n", address)

		// Said plainly, because somebody may be reading this console without
		// having written the configuration that opened it.
		out.WriteString("  !! api.insecure is set: no pairing code is required, so the\n" +
			"  !! first client to reach this port claims this node for good.\n\n")
	} else {
		fmt.Fprintf(&out, "  pairing code  %s\n", e.Code())
		fmt.Fprintf(&out, "  fingerprint   %s\n\n", fingerprint)
		fmt.Fprintf(&out, "  cctl enroll %s --code %s\n\n", address, e.Code())
	}

	out.WriteString("The node joins no cluster until an operator claims it.\n")

	return out.String()
}
