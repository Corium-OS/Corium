// Package api holds the state a Corium node keeps about who is allowed to
// manage it: the operator CA it has been given, the serving identity it minted
// for itself, and — before either exists — the pairing code that lets an
// operator claim it from the console.
//
// Nothing here listens on a socket. The transport is deliberately a separate
// concern, so that the part that decides who owns a node can be read, and
// tested, without a server in the way.
//
// See docs/adr/0004-management-api.md.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"strings"
)

// codeAlphabet is Crockford's base32: the digits and the uppercase letters,
// less I, L, O and U. The first three are dropped because they are read back
// as 1, 1 and 0 from a console at the wrong angle, and U because it turns a
// random string into an unfortunate word often enough to matter.
//
// Thirty-two symbols also divides 256 exactly, so a byte can be folded into
// one without the modulo bias that would quietly shrink the keyspace.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeLength is eight symbols, or forty bits.
//
// The threat it answers is somebody on the provisioning network trying codes
// against a node before its operator gets to it, and the attempt limit is what
// actually stops them: five tries per boot. Forty bits is chosen for the other
// end of the problem — a person reading it off a screen and typing it into a
// terminal — and it leaves the guessing odds at roughly one in 2^38 per boot.
const codeLength = 8

// newCode mints a pairing code.
//
// One is minted per boot, kept in /run for as long as that boot lasts, and
// never written to a disk -- see DefaultSessionDir. Persisting it properly
// would make it recoverable from a disk image of an unclaimed node, and there
// is nothing to gain: a node that reboots before anybody claims it simply
// offers a new one.
func newCode() (string, error) {
	raw := make([]byte, codeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	symbols := make([]byte, codeLength)
	for i, b := range raw {
		symbols[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}

	return string(symbols), nil
}

// formatCode groups a code for a human to read and retype.
func formatCode(code string) string {
	if len(code) != codeLength {
		return code
	}

	return code[:4] + "-" + code[4:]
}

// normaliseCode accepts what somebody actually types.
//
// Case and grouping are presentation, not secret: an operator who types the
// code back in lowercase, or without the dash they saw on screen, has proved
// exactly as much as one who did not. Refusing them would only teach people to
// paste more carefully, not make the node safer.
func normaliseCode(typed string) string {
	var cleaned strings.Builder

	for _, r := range strings.ToUpper(strings.TrimSpace(typed)) {
		if r == '-' || r == ' ' {
			continue
		}

		cleaned.WriteRune(r)
	}

	return cleaned.String()
}

// codeMatches compares a typed code against the expected one without leaking
// how much of it was right.
//
// The comparison is constant time in the contents. It is not constant time in
// the length -- subtle.ConstantTimeCompare returns early when lengths differ --
// which is acceptable because the length is fixed, published on the console,
// and in this file.
func codeMatches(expected, typed string) bool {
	return subtle.ConstantTimeCompare([]byte(expected), []byte(normaliseCode(typed))) == 1
}
