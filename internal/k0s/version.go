package k0s

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// versionPattern matches a k0s release tag as upstream writes it:
// v1.36.4+k0s.1 -- a Kubernetes version, then the k0s build of it.
//
// The build number is part of the identity, not decoration. v1.36.4+k0s.0 and
// v1.36.4+k0s.1 are the same Kubernetes and different k0s, which is exactly the
// kind of change this axis exists to let an operator make on its own.
var versionPattern = regexp.MustCompile(
	`^v(\d+)\.(\d+)\.(\d+)\+k0s\.(\d+)$`)

// ErrMalformedVersion reports a string that is not a k0s release tag.
var ErrMalformedVersion = errors.New("not a k0s release tag")

// Version is a parsed k0s release tag.
type Version struct {
	Major, Minor, Patch, Build int

	// Raw is the tag as written, which is what goes on a command line or into
	// an artefact reference. Reassembling it from the fields would work today
	// and silently drift the day upstream's format gains a part.
	Raw string
}

// ParseVersion parses a k0s release tag such as v1.36.4+k0s.1.
func ParseVersion(s string) (Version, error) {
	match := versionPattern.FindStringSubmatch(s)
	if match == nil {
		return Version{}, fmt.Errorf(
			"%q: %w (expected something like v1.36.4+k0s.1)", s, ErrMalformedVersion)
	}

	// Every group is \d+ and already matched, so these cannot fail on anything
	// the pattern accepts -- except a number too large for an int, which is not
	// a version anybody released.
	numbers := make([]int, 4)

	for i := range numbers {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("%q: %w", s, ErrMalformedVersion)
		}

		numbers[i] = n
	}

	return Version{
		Major: numbers[0],
		Minor: numbers[1],
		Patch: numbers[2],
		Build: numbers[3],
		Raw:   s,
	}, nil
}

// String returns the tag as written.
func (v Version) String() string { return v.Raw }

// MinorLine reports the Kubernetes minor a version belongs to, as "1.36".
//
// It is what the supported window is counted in, and what the skew rules are
// written in terms of.
func (v Version) MinorLine() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// SameMinor reports whether two versions are on the same Kubernetes minor.
func (v Version) SameMinor(other Version) bool {
	return v.Major == other.Major && v.Minor == other.Minor
}

// ErrSkew reports a version change k0s does not support.
var ErrSkew = errors.New("unsupported version change")

// CheckSkew reports whether a node may move from one k0s version to another.
//
// k0s is stricter than upstream Kubernetes here, and the rules are not
// advisory: controllers must stay within one minor of each other, a worker may
// be one minor behind its controllers and never ahead, and an upgrade goes one
// minor at a time. Skipping a minor is the mistake an operator actually makes
// -- 1.34 to 1.36 reads like a shortcut rather than a violation -- and the
// moment to say so is before anything has moved, not when k0s refuses to start
// having already been stopped.
//
// What is allowed is a move within a minor, in either direction, and a move to
// the next minor up. What is refused is everything else, with the rule named
// rather than merely the refusal: an operator who is told "one minor at a time"
// knows what to do next, and one told "unsupported" does not.
//
// This says nothing about the other nodes in the cluster. Whether it is safe to
// move *this* node is a question about the ones it runs with, which is why
// ordering a rollout lives in cctl, where the other nodes exist.
func CheckSkew(from, to Version) error {
	switch {
	case from.Major != to.Major:
		return fmt.Errorf(
			"%w: %s to %s crosses a major version; k0s upgrades one minor at a time",
			ErrSkew, from, to)

	case from.SameMinor(to):
		// Same minor: a k0s build or patch change. This is the cheap case the
		// axis was built for -- picking up a k0s fix without touching the
		// Kubernetes version at all.
		return nil

	case to.Minor == from.Minor+1:
		return nil

	case to.Minor > from.Minor:
		return fmt.Errorf(
			"%w: %s to %s skips %d minor version(s); k0s upgrades one minor at a "+
				"time, so go through %d.%d first",
			ErrSkew, from, to, to.Minor-from.Minor-1, from.Major, from.Minor+1)

	default:
		return fmt.Errorf(
			"%w: %s to %s is a downgrade; k0s does not support moving back a "+
				"minor version, and a cluster that has run %s cannot be returned to %s",
			ErrSkew, from, to, from.MinorLine(), to.MinorLine())
	}
}
