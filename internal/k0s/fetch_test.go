package k0s

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReference(t *testing.T) {
	t.Parallel()

	version, err := ParseVersion("v1.36.4+k0s.1")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	tests := []struct {
		name, mirror, want string
	}{
		{
			name: "the default mirror",
			// A registry tag may not contain '+', so the mapping to '_' is
			// mechanical in both directions rather than a lookup. The
			// architecture is in the tag so a mismatch is a 404 when asking,
			// rather than an extension that fails to merge after downloading.
			want: DefaultMirror + ":v1.36.4_k0s.1-" + runtime.GOARCH,
		},
		{
			name:   "an internal mirror",
			mirror: "registry.internal.example/corium/k0s",
			want: "registry.internal.example/corium/k0s:v1.36.4_k0s.1-" +
				runtime.GOARCH,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := Reference(test.mirror, version); got != test.want {
				t.Errorf("Reference = %q, want %q", got, test.want)
			}
		})
	}
}

// writeManifest lays out a dir: copy the way skopeo does: a manifest, and each
// blob written verbatim under the hex of its digest.
func writeManifest(t *testing.T, layers []string, writeBlobs bool) string {
	t.Helper()

	dir := t.TempDir()

	type layer struct {
		Digest string `json:"digest"`
	}

	body := struct {
		Layers []layer `json:"layers"`
	}{}

	for _, hex := range layers {
		body.Layers = append(body.Layers, layer{Digest: "sha256:" + hex})

		if writeBlobs {
			if err := os.WriteFile(filepath.Join(dir, hex), []byte("image"), 0o600); err != nil {
				t.Fatalf("writing blob: %v", err)
			}
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling manifest: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	return dir
}

func TestExtensionBlob(t *testing.T) {
	t.Parallel()

	t.Run("one blob is the extension", func(t *testing.T) {
		t.Parallel()

		dir := writeManifest(t, []string{"abc123"}, true)

		got, err := extensionBlob(dir)
		if err != nil {
			t.Fatalf("extensionBlob: %v", err)
		}

		if want := filepath.Join(dir, "abc123"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("several blobs are refused", func(t *testing.T) {
		t.Parallel()

		// Picking one would be a guess that works most of the time, which is
		// the worst available behaviour: the time it guesses wrong, a node
		// installs the wrong file and reports success.
		if _, err := extensionBlob(writeManifest(t, []string{"a1", "b2"}, true)); err == nil {
			t.Fatal("a multi-layer artefact was accepted")
		}
	})

	t.Run("no blob at all is refused", func(t *testing.T) {
		t.Parallel()

		_, err := extensionBlob(writeManifest(t, nil, false))
		if !errors.Is(err, ErrNoLayer) {
			t.Fatalf("error is %v, want it to wrap ErrNoLayer", err)
		}
	})

	t.Run("a manifest naming an uncopied blob is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := extensionBlob(writeManifest(t, []string{"missing"}, false)); err == nil {
			t.Fatal("a manifest naming a blob that was not copied was accepted")
		}
	})

	t.Run("a malformed digest is refused", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
			[]byte(`{"layers":[{"digest":"nocolon"}]}`), 0o600); err != nil {
			t.Fatalf("writing manifest: %v", err)
		}

		if _, err := extensionBlob(dir); !errors.Is(err, ErrNoLayer) {
			t.Fatalf("error is %v, want it to wrap ErrNoLayer", err)
		}
	})

	t.Run("a missing manifest is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := extensionBlob(t.TempDir()); err == nil {
			t.Fatal("a directory with no manifest was accepted")
		}
	})
}
