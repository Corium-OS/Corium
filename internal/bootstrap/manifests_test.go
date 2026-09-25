package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

// redirectManifests points the writer at a temporary tree. Not parallel: the
// directory is a package-level variable, the same way fstabPath is.
func redirectManifests(t *testing.T) string {
	t.Helper()

	original := manifestsDir
	t.Cleanup(func() { manifestsDir = original })

	manifestsDir = filepath.Join(t.TempDir(), "k0s", "manifests")

	return manifestsDir
}

func TestApplyManifestsWritesEachStack(t *testing.T) {
	root := redirectManifests(t)

	cfg := &config.Config{
		Role: config.RoleControllerWorker,
		Manifests: []config.ManifestStack{
			{
				Name: "metallb-config",
				Files: []config.ManifestFile{
					{Name: "pools.yaml", Content: "kind: IPAddressPool\n"},
					{Name: "l2.yaml", Content: "kind: L2Advertisement\n"},
				},
			},
			{
				Name:  "storage",
				Files: []config.ManifestFile{{Name: "class.yaml", Content: "kind: StorageClass\n"}},
			},
		},
	}

	if err := applyManifests(cfg); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	for path, want := range map[string]string{
		filepath.Join(root, "metallb-config", "pools.yaml"): "kind: IPAddressPool\n",
		filepath.Join(root, "metallb-config", "l2.yaml"):    "kind: L2Advertisement\n",
		filepath.Join(root, "storage", "class.yaml"):        "kind: StorageClass\n",
	} {
		written, err := os.ReadFile(path) //nolint:gosec // a path this test built
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		if string(written) != want {
			t.Errorf("%s = %q, want %q", path, written, want)
		}
	}
}

// The content is written byte for byte: a manifest is passed through, not
// re-serialised, so comments and document separators survive.
func TestApplyManifestsWritesContentVerbatim(t *testing.T) {
	root := redirectManifests(t)

	content := "# a comment\n" +
		"apiVersion: v1\n" +
		"kind: Namespace\n" +
		"metadata:\n  name: demo\n" +
		"---\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  namespace: demo\n"

	cfg := &config.Config{
		Role: config.RoleSingle,
		Manifests: []config.ManifestStack{{
			Name:  "demo",
			Files: []config.ManifestFile{{Name: "demo.yaml", Content: content}},
		}},
	}

	if err := applyManifests(cfg); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	written, err := os.ReadFile(filepath.Join(root, "demo", "demo.yaml"))
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}

	if string(written) != content {
		t.Errorf("manifest = %q, want %q", written, content)
	}
}

// Bootstrapping the same node twice must produce the same bytes, not a second
// copy of the same objects.
func TestApplyManifestsIsIdempotent(t *testing.T) {
	root := redirectManifests(t)

	cfg := &config.Config{
		Role: config.RoleSingle,
		Manifests: []config.ManifestStack{{
			Name:  "demo",
			Files: []config.ManifestFile{{Name: "demo.yaml", Content: "kind: ConfigMap\n"}},
		}},
	}

	for run := range 2 {
		if err := applyManifests(cfg); err != nil {
			t.Fatalf("applyManifests() run %d = %v, want nil", run+1, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("reading the stack directory: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("stack holds %d files after two runs, want 1", len(entries))
	}

	written, err := os.ReadFile(filepath.Join(root, "demo", "demo.yaml"))
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}

	if string(written) != "kind: ConfigMap\n" {
		t.Errorf("manifest = %q, want it rewritten rather than appended to", written)
	}
}

// A shorter second version must truncate the file rather than leave the tail of
// the first behind, which would be a document the deployer still applies.
func TestApplyManifestsTruncatesAShorterRewrite(t *testing.T) {
	root := redirectManifests(t)

	long := "kind: ConfigMap\n---\nkind: Secret\n"

	cfg := &config.Config{
		Role: config.RoleSingle,
		Manifests: []config.ManifestStack{{
			Name:  "demo",
			Files: []config.ManifestFile{{Name: "demo.yaml", Content: long}},
		}},
	}

	if err := applyManifests(cfg); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	cfg.Manifests[0].Files[0].Content = "kind: ConfigMap\n"

	if err := applyManifests(cfg); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	written, err := os.ReadFile(filepath.Join(root, "demo", "demo.yaml"))
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}

	if string(written) != "kind: ConfigMap\n" {
		t.Errorf("manifest = %q, want the first version gone entirely", written)
	}
}

func TestApplyManifestsModes(t *testing.T) {
	root := redirectManifests(t)

	cfg := &config.Config{
		Role: config.RoleSingle,
		Manifests: []config.ManifestStack{{
			Name:  "demo",
			Files: []config.ManifestFile{{Name: "demo.yaml", Content: "kind: ConfigMap\n"}},
		}},
	}

	if err := applyManifests(cfg); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	dir, err := os.Stat(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("stat on the stack directory: %v", err)
	}

	if mode := dir.Mode().Perm(); mode&0o002 != 0 {
		t.Errorf("stack directory mode = %o, want it not world-writable", mode)
	}

	file, err := os.Stat(filepath.Join(root, "demo", "demo.yaml"))
	if err != nil {
		t.Fatalf("stat on the manifest: %v", err)
	}

	if mode := file.Mode().Perm(); mode != manifestFileMode {
		t.Errorf("manifest mode = %o, want %o", mode, manifestFileMode)
	}
}

// Nothing declared, nothing created: a node with no manifests must not grow an
// empty /var/lib/k0s/manifests it did not ask for.
func TestApplyManifestsDoesNothingWhenNoneAreDeclared(t *testing.T) {
	root := redirectManifests(t)

	if err := applyManifests(&config.Config{Role: config.RoleSingle}); err != nil {
		t.Fatalf("applyManifests() = %v, want nil", err)
	}

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("stat(%s) = %v, want the directory not to exist", root, err)
	}
}
