package k0s

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile puts content at a temporary path and returns it.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return path
}

const testWindow = `# Generated from build/k0s.lock.
K0S_FLOOR=v1.36.4+k0s.0

v1.34.11+k0s.1
v1.35.8+k0s.1
v1.36.4+k0s.0
v1.36.4+k0s.1
`

func TestReadWindow(t *testing.T) {
	t.Parallel()

	window, err := ReadWindow(writeFile(t, "k0s.window", testWindow))
	if err != nil {
		t.Fatalf("ReadWindow: %v", err)
	}

	if window.Floor.Raw != "v1.36.4+k0s.0" {
		t.Errorf("floor = %s, want v1.36.4+k0s.0", window.Floor)
	}

	if len(window.Versions) != 4 {
		t.Fatalf("got %d versions, want 4", len(window.Versions))
	}
}

func TestReadWindowRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, content string
	}{
		{
			name: "no floor",
			// A window with no floor cannot say what a node runs when it is
			// told nothing, which is the state every node starts in.
			content: "v1.36.4+k0s.1\n",
		},
		{name: "malformed floor", content: "K0S_FLOOR=latest\n"},
		{name: "malformed entry", content: "K0S_FLOOR=v1.36.4+k0s.0\nnot-a-version\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := ReadWindow(writeFile(t, "k0s.window", test.content)); err == nil {
				t.Fatal("accepted a window it should have refused")
			}
		})
	}
}

func TestReadWindowMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := ReadWindow(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing window file was accepted")
	}
}

func TestWindowCheck(t *testing.T) {
	t.Parallel()

	window, err := ReadWindow(writeFile(t, "k0s.window", testWindow))
	if err != nil {
		t.Fatalf("ReadWindow: %v", err)
	}

	tests := []struct {
		name, version string
		wantSupported bool
	}{
		{name: "the floor", version: "v1.36.4+k0s.0", wantSupported: true},
		{name: "a k0s rebuild", version: "v1.36.4+k0s.1", wantSupported: true},
		{name: "the oldest minor", version: "v1.34.11+k0s.1", wantSupported: true},

		{
			name: "same minor, unpublished patch",
			// Matched exactly rather than by minor: a version nobody built an
			// extension for is not one a node can be asked to run, and saying
			// so beats a pull that fails with a 404.
			version: "v1.36.3+k0s.0",
		},
		{name: "outside the window", version: "v1.33.13+k0s.1"},
		{name: "newer than the window", version: "v1.37.0+k0s.0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			version, err := ParseVersion(test.version)
			if err != nil {
				t.Fatalf("parsing %s: %v", test.version, err)
			}

			err = window.Check(version)

			if test.wantSupported {
				if err != nil {
					t.Fatalf("Check(%s) = %v, want it supported", test.version, err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Check(%s) = nil, want it refused", test.version)
			}

			if !errors.Is(err, ErrOutsideWindow) {
				t.Errorf("error is %v, want it to wrap ErrOutsideWindow", err)
			}

			// The refusal has to say what the image *does* support, or an
			// operator's next move is a guess.
			if !strings.Contains(err.Error(), "v1.36.4+k0s.1") {
				t.Errorf("refusal does not list the supported versions: %v", err)
			}
		})
	}
}

func TestActiveVersion(t *testing.T) {
	t.Parallel()

	floor, err := ParseVersion("v1.36.4+k0s.0")
	if err != nil {
		t.Fatalf("parsing the floor: %v", err)
	}

	t.Run("no extension merged is the floor", func(t *testing.T) {
		t.Parallel()

		// An ordinary state, not an error: it is what every node does until it
		// is told otherwise, and what one falls back to if its extension stops
		// matching after an OS rebase.
		got, err := ActiveVersion(filepath.Join(t.TempDir(), "absent"), floor)
		if err != nil {
			t.Fatalf("ActiveVersion: %v", err)
		}

		if got.Raw != floor.Raw {
			t.Errorf("got %s, want the floor %s", got, floor)
		}
	})

	t.Run("reads the merged version", func(t *testing.T) {
		t.Parallel()

		path := writeFile(t, "extension-release.corium-k0s",
			"ID=fedora\nSYSEXT_LEVEL=1\nCORIUM_K0S_VERSION=v1.36.4+k0s.1\n")

		got, err := ActiveVersion(path, floor)
		if err != nil {
			t.Fatalf("ActiveVersion: %v", err)
		}

		if got.Raw != "v1.36.4+k0s.1" {
			t.Errorf("got %s, want v1.36.4+k0s.1", got)
		}
	})

	t.Run("an extension that is not ours is an error", func(t *testing.T) {
		t.Parallel()

		// Falling back to the floor here would report a version the node is
		// demonstrably not running: something *is* merged over /usr/bin/k0s.
		path := writeFile(t, "extension-release.corium-k0s", "ID=fedora\nSYSEXT_LEVEL=1\n")

		if _, err := ActiveVersion(path, floor); err == nil {
			t.Fatal("an extension carrying no version was accepted")
		}
	})

	t.Run("a malformed version is an error", func(t *testing.T) {
		t.Parallel()

		path := writeFile(t, "extension-release.corium-k0s",
			"CORIUM_K0S_VERSION=latest\n")

		if _, err := ActiveVersion(path, floor); err == nil {
			t.Fatal("a malformed version was accepted")
		}
	})
}

func TestStoredImageKeepsTheTag(t *testing.T) {
	t.Parallel()

	version, err := ParseVersion("v1.36.4+k0s.1")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	// Named after what it holds: a mangled name is one more mapping to get
	// wrong when somebody is reading the directory by hand at 3 a.m.
	if got, want := StoredImage(version), ExtensionStore+"/v1.36.4+k0s.1.raw"; got != want {
		t.Errorf("StoredImage = %s, want %s", got, want)
	}
}
