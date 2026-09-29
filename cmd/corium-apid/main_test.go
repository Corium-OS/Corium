package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHasBootstrappedReadsTheMarker covers the gate ADR 11 rule 3 rests on:
// enrolment is served only while this file is absent.
func TestHasBootstrappedReadsTheMarker(t *testing.T) {
	dir := t.TempDir()

	got, err := hasBootstrapped(dir)
	if err != nil {
		t.Fatalf("hasBootstrapped() = %v, want nil", err)
	}

	if got {
		t.Error("hasBootstrapped() = true on an empty state directory, want false")
	}

	if err := os.WriteFile(filepath.Join(dir, "bootstrapped"), nil, 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}

	got, err = hasBootstrapped(dir)
	if err != nil {
		t.Fatalf("hasBootstrapped() = %v, want nil", err)
	}

	if !got {
		t.Error("hasBootstrapped() = false with the marker present, want true")
	}
}

// TestHasBootstrappedRefusesToGuess is the half that matters for security. A
// /var it cannot read must not be reported as "never bootstrapped", because
// that answer reopens enrolment on a node holding cluster credentials.
func TestHasBootstrappedRefusesToGuess(t *testing.T) {
	dir := t.TempDir()

	// A marker that is a directory rather than a file still stats cleanly, so
	// make the parent unreadable instead: that is the failure a damaged or
	// mislabelled /var actually produces.
	blocked := filepath.Join(dir, "state")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the permission bits this test needs")
	}

	if _, err := hasBootstrapped(blocked); err == nil {
		t.Error("hasBootstrapped() = nil error on an unreadable state directory, want a failure")
	}
}
