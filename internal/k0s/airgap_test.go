package k0s

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundleReference(t *testing.T) {
	t.Parallel()

	version, err := ParseVersion("v1.36.4+k0s.1")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	tests := []struct {
		name, mirror, want string
	}{
		{
			name: "the default mirror's sibling",
			// The bundle lives in the repository beside the extension's, so the
			// two artefacts of one version are always found together.
			want: DefaultBundleMirror + ":v1.36.4_k0s.1-" + runtime.GOARCH,
		},
		{
			name:   "an internal mirror's sibling",
			mirror: "registry.internal.example/corium/k0s",
			want: "registry.internal.example/corium/k0s-airgap:v1.36.4_k0s.1-" +
				runtime.GOARCH,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := BundleReference(test.mirror, version); got != test.want {
				t.Errorf("BundleReference = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBundleName(t *testing.T) {
	t.Parallel()

	version, err := ParseVersion("v1.36.6+k0s.0")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	want := "k0s-airgap-bundle-v1.36.6+k0s.0-linux-" + runtime.GOARCH + ".tar"
	if got := BundleName(version); got != want {
		t.Errorf("BundleName = %q, want %q", got, want)
	}

	if got := StoredBundle(version); got != filepath.Join(ImageDir, want) {
		t.Errorf("StoredBundle = %q, want %q", got, filepath.Join(ImageDir, want))
	}
}

func TestClearBundles(t *testing.T) {
	t.Parallel()

	keep, err := ParseVersion("v1.36.6+k0s.0")
	if err != nil {
		t.Fatal(err)
	}

	old, err := ParseVersion("v1.36.5+k0s.0")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("an absent directory is not an error", func(t *testing.T) {
		t.Parallel()

		if err := ClearBundles(filepath.Join(t.TempDir(), "absent"), keep); err != nil {
			t.Fatalf("ClearBundles: %v", err)
		}
	})

	t.Run("only other versions' bundles are removed", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		// kept says whether each file must still be there afterwards.
		kept := map[string]bool{
			BundleName(keep): true,  // the running version's bundle stays
			BundleName(old):  false, // the previous version's goes
			"k0s-airgap-bundle-v1.35.9+k0s.0-linux-" + runtime.GOARCH + ".tar": false,
			"operator-bundle.tar":       true, // not our naming: untouched
			"k0s-airgap-bundle-notatar": true, // wrong suffix: untouched
		}

		for name := range kept {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		// A directory with a bundle's name is not a bundle: left alone.
		if err := os.Mkdir(filepath.Join(dir, bundlePrefix+"a-directory.tar"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := ClearBundles(dir, keep); err != nil {
			t.Fatalf("ClearBundles: %v", err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading the directory after ClearBundles: %v", err)
		}

		for _, entry := range entries {
			want, known := kept[entry.Name()]
			if !known {
				want = true // the directory created above
			}

			if !want {
				t.Errorf("%s was not removed", entry.Name())
			}
		}

		for name, want := range kept {
			if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
				t.Errorf("%s present = %v, want %v", name, err == nil, want)
			}
		}
	})
}
