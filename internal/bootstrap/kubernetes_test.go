package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Corium-OS/Corium/internal/k0s"
)

// TestBootstrapSkew covers which versions a node may start on. A node that has
// never run k0s has no state a minor change could break, so the whole window
// is open to it; one with state keeps k0s's rules.
func TestBootstrapSkew(t *testing.T) {
	t.Parallel()

	floor := mustVersion(t, "v1.36.5+k0s.0")

	tests := []struct {
		name     string
		version  string
		hasState bool
		wantErr  error
	}{
		{name: "two minors below the floor, fresh node", version: "v1.34.12+k0s.0"},
		{name: "one minor below the floor, fresh node", version: "v1.35.9+k0s.0"},
		{
			name: "two minors below the floor, k0s state present", version: "v1.34.12+k0s.0",
			hasState: true, wantErr: k0s.ErrSkew,
		},
		{name: "a patch on the floor's minor, k0s state present", version: "v1.36.4+k0s.1", hasState: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkBootstrapSkew(floor, mustVersion(t, test.version), test.hasState)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("checkBootstrapSkew() = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestK0sHasState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if has, err := k0sHasState(filepath.Join(dir, "absent")); err != nil || has {
		t.Errorf("absent directory: has = %v, err = %v; want false", has, err)
	}

	// What Corium itself puts there before k0s first starts is not k0s state.
	for _, name := range []string{"bin", "manifests"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if has, err := k0sHasState(dir); err != nil || has {
		t.Errorf("only bin and manifests: has = %v, err = %v; want false", has, err)
	}

	if err := os.Mkdir(filepath.Join(dir, "etcd"), 0o700); err != nil {
		t.Fatal(err)
	}

	if has, err := k0sHasState(dir); err != nil || !has {
		t.Errorf("with etcd: has = %v, err = %v; want true", has, err)
	}
}

func mustVersion(t *testing.T, s string) k0s.Version {
	t.Helper()

	v, err := k0s.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}

	return v
}
