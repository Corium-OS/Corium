package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// testWindow is the window an image would ship: a floor, and the three minors
// around it.
const testWindow = `K0S_FLOOR=v1.36.4+k0s.0

v1.34.11+k0s.1
v1.35.8+k0s.1
v1.36.4+k0s.0
v1.36.4+k0s.1
`

// serverWithWindow is a bootstrapped node whose supported versions a test owns.
//
// The extension-release path is left pointing at the real /usr, where no test
// machine has one, so the node reads as running its floor -- which is what a
// node with no extension merged genuinely is.
func serverWithWindow(t *testing.T, ca *authority) *Server {
	t.Helper()

	server, _, _, _ := configurableServer(t, ca, true)

	path := filepath.Join(t.TempDir(), "k0s.window")
	if err := os.WriteFile(path, []byte(testWindow), 0o600); err != nil {
		t.Fatalf("writing the window: %v", err)
	}

	server.k0sWindowPath = path

	return server
}

// TestSwapRefusesBeforeTouchingTheNode covers the refusals that have to happen
// before anything is downloaded, drained or stopped.
//
// That ordering is the point of the test rather than a detail of it: each of
// these reaches its decision without a registry, a cluster or systemd, which is
// why they can be asserted at all on a machine that has none of the three. A
// refusal that needed any of them would be one a node discovers with its
// workloads already evicted.
func TestSwapRefusesBeforeTouchingTheNode(t *testing.T) {
	t.Parallel()

	ca := newAuthority(t)

	tests := []struct {
		name    string
		version string
		wantErr error
	}{
		{
			name: "outside the window",
			// Reaching this one is an OS upgrade, and the node says so rather
			// than trying and getting a 404.
			version: "v1.33.13+k0s.1",
			wantErr: k0s.ErrOutsideWindow,
		},
		{
			name: "a minor downgrade",
			// In the window, and still refused: k0s does not support moving
			// back a minor, so the floor at 1.36 cannot go to 1.34.
			version: "v1.34.11+k0s.1",
			wantErr: k0s.ErrSkew,
		},
		{
			name:    "not a k0s release tag",
			version: "v1.36.4",
			wantErr: k0s.ErrMalformedVersion,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := serverWithWindow(t, ca)

			_, err := server.swapKubernetesVersion(context.Background(),
				&config.Config{
					Role:       config.RoleSingle,
					Kubernetes: config.Kubernetes{Version: test.version},
				})

			if err == nil {
				t.Fatal("the swap was allowed")
			}

			if !errors.Is(err, test.wantErr) {
				t.Errorf("error is %v, want it to wrap %v", err, test.wantErr)
			}
		})
	}
}

// TestSwapToTheRunningVersionDoesNothing guards the property that makes a
// repeated apply safe: asking for the version a node already runs must not
// drain it, stop k0s, or touch the extension.
//
// It is asserted by the fact that this test passes at all. Nothing here can
// reach a registry or systemd, so a swap that went past the early return would
// fail rather than return the version.
func TestSwapToTheRunningVersionDoesNothing(t *testing.T) {
	t.Parallel()

	server := serverWithWindow(t, newAuthority(t))

	got, err := server.swapKubernetesVersion(context.Background(),
		&config.Config{
			Role: config.RoleSingle,
			// The floor, which is what a node with no extension merged runs.
			Kubernetes: config.Kubernetes{Version: "v1.36.4+k0s.0"},
		})
	if err != nil {
		t.Fatalf("swapKubernetesVersion() error = %v", err)
	}

	if got != "v1.36.4+k0s.0" {
		t.Errorf("got %q, want the floor v1.36.4+k0s.0", got)
	}
}
