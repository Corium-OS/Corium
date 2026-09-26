package k0s

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		input                      string
		major, minor, patch, build int
		wantErr                    bool
	}{
		{name: "release", input: "v1.36.4+k0s.0", major: 1, minor: 36, patch: 4},
		{
			name: "k0s rebuild of the same kubernetes",
			// The distinction the whole axis exists to let an operator act on:
			// same Kubernetes, different k0s.
			input: "v1.36.4+k0s.1", major: 1, minor: 36, patch: 4, build: 1,
		},
		{name: "multi-digit minor", input: "v1.100.0+k0s.12", major: 1, minor: 100, build: 12},

		{name: "empty", input: "", wantErr: true},
		{name: "no v prefix", input: "1.36.4+k0s.0", wantErr: true},
		{name: "no k0s build", input: "v1.36.4", wantErr: true},
		{name: "kubernetes semver prerelease", input: "v1.37.0-alpha.1+k0s.0", wantErr: true},
		{name: "trailing junk", input: "v1.36.4+k0s.0-amd64", wantErr: true},
		{name: "not a version at all", input: "latest", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseVersion(test.input)

			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want an error", test.input, got)
				}

				if !errors.Is(err, ErrMalformedVersion) {
					t.Errorf("error is %v, want it to wrap ErrMalformedVersion", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("ParseVersion(%q): %v", test.input, err)
			}

			if got.Major != test.major || got.Minor != test.minor ||
				got.Patch != test.patch || got.Build != test.build {
				t.Errorf("got %d.%d.%d+k0s.%d, want %d.%d.%d+k0s.%d",
					got.Major, got.Minor, got.Patch, got.Build,
					test.major, test.minor, test.patch, test.build)
			}

			// The tag is carried through rather than reassembled, so that it
			// stays byte-identical to what goes on a command line.
			if got.Raw != test.input {
				t.Errorf("Raw = %q, want %q", got.Raw, test.input)
			}
		})
	}
}

func TestCheckSkew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		from, to  string
		wantAllow bool
	}{
		{
			name: "k0s rebuild of the same version",
			from: "v1.36.4+k0s.0", to: "v1.36.4+k0s.1", wantAllow: true,
		},
		{
			name: "patch within a minor",
			from: "v1.36.1+k0s.0", to: "v1.36.4+k0s.0", wantAllow: true,
		},
		{
			name: "patch backwards within a minor",
			// Allowed: k0s constrains minors, and backing out a bad patch is a
			// thing an operator legitimately does in a hurry.
			from: "v1.36.4+k0s.0", to: "v1.36.1+k0s.0", wantAllow: true,
		},
		{
			name: "one minor up",
			from: "v1.35.8+k0s.1", to: "v1.36.4+k0s.0", wantAllow: true,
		},

		{
			name: "skipping a minor",
			// The mistake an operator actually makes: it reads as a shortcut
			// rather than as a violation.
			from: "v1.34.11+k0s.1", to: "v1.36.4+k0s.0",
		},
		{
			name: "minor downgrade",
			from: "v1.36.4+k0s.0", to: "v1.35.8+k0s.1",
		},
		{
			name: "major change",
			from: "v1.36.4+k0s.0", to: "v2.0.0+k0s.0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			from, err := ParseVersion(test.from)
			if err != nil {
				t.Fatalf("parsing from: %v", err)
			}

			to, err := ParseVersion(test.to)
			if err != nil {
				t.Fatalf("parsing to: %v", err)
			}

			err = CheckSkew(from, to)

			if test.wantAllow {
				if err != nil {
					t.Fatalf("CheckSkew(%s, %s) = %v, want it allowed",
						test.from, test.to, err)
				}

				return
			}

			if err == nil {
				t.Fatalf("CheckSkew(%s, %s) = nil, want it refused",
					test.from, test.to)
			}

			if !errors.Is(err, ErrSkew) {
				t.Errorf("error is %v, want it to wrap ErrSkew", err)
			}
		})
	}
}

// TestCheckSkewNamesTheRule guards the property the refusal exists for: an
// operator told "unsupported" does not know what to do next, and one told "one
// minor at a time" does.
func TestCheckSkewNamesTheRule(t *testing.T) {
	t.Parallel()

	from, err := ParseVersion("v1.34.11+k0s.1")
	if err != nil {
		t.Fatalf("parsing from: %v", err)
	}

	to, err := ParseVersion("v1.36.4+k0s.0")
	if err != nil {
		t.Fatalf("parsing to: %v", err)
	}

	err = CheckSkew(from, to)
	if err == nil {
		t.Fatal("skipping a minor was allowed")
	}

	// The intermediate version is the actionable part: it is what the operator
	// has to do first.
	if got := err.Error(); !strings.Contains(got, "1.35") {
		t.Errorf("refusal does not name the minor to go through first: %s", got)
	}
}
