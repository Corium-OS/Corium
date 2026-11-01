package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
	"github.com/Corium-OS/Corium/internal/lifecycle"
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

// TestCheckAnswersWithoutChangingAnything covers the route a rollout asks every
// node before moving the first.
func TestCheckAnswersWithoutChangingAnything(t *testing.T) {
	t.Parallel()

	ca := newAuthority(t)

	tests := []struct {
		name       string
		version    string
		wantStatus int
		wantResult string
	}{
		{name: "the running version", version: "v1.36.4+k0s.0",
			wantStatus: http.StatusOK, wantResult: "unchanged"},
		{name: "a patch in the window", version: "v1.36.4+k0s.1",
			wantStatus: http.StatusOK, wantResult: "would-change"},
		{name: "a minor downgrade", version: "v1.34.11+k0s.1",
			wantStatus: http.StatusConflict},
		{name: "outside the window", version: "v1.33.13+k0s.1",
			wantStatus: http.StatusConflict},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := serverWithWindow(t, ca)

			if err := os.WriteFile(server.configPath, []byte(aDocument), 0o600); err != nil {
				t.Fatalf("seeding the configuration: %v", err)
			}

			address := serveOn(t, server)
			admin := client(t, server.store, ca.issue(t, RoleAdmin))

			status, body := postRaw(t, admin, "https://"+address+"/v1/kubernetes/check",
				`{"version":"`+test.version+`"}`)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d (%v)", status, test.wantStatus, body)
			}

			if result, _ := body["status"].(string); result != test.wantResult {
				t.Errorf("status field = %q, want %q", result, test.wantResult)
			}

			written, err := os.ReadFile(server.configPath)
			if err != nil {
				t.Fatalf("reading the configuration: %v", err)
			}

			if string(written) != aDocument {
				t.Error("a check rewrote the node's configuration")
			}
		})
	}
}

// minorWindow puts the floor one minor below the top of the window, so a
// minor move is reachable on a node with no extension merged.
const minorWindow = `K0S_FLOOR=v1.35.8+k0s.1

v1.34.11+k0s.1
v1.35.8+k0s.1
v1.36.4+k0s.0
`

const (
	strandedWorker = `{"items":[
		{"metadata":{"name":"c1"},"status":{"nodeInfo":{"kubeletVersion":"v1.35.8+k0s"}}},
		{"metadata":{"name":"forgotten"},"status":{"nodeInfo":{"kubeletVersion":"v1.34.11+k0s"}}}]}`
	laggingWorker = `{"items":[
		{"metadata":{"name":"c1"},"status":{"nodeInfo":{"kubeletVersion":"v1.35.8+k0s"}}}]}`
)

// serverWithKubelets is a node on 1.35 whose cluster reports the kubelets given.
func serverWithKubelets(t *testing.T, ca *authority, kubelets string, asked *atomic.Bool) *Server {
	t.Helper()

	server := serverWithWindow(t, ca)

	if err := os.WriteFile(server.k0sWindowPath, []byte(minorWindow), 0o600); err != nil {
		t.Fatalf("writing the window: %v", err)
	}

	server.Lifecycle(&lifecycle.Manager{
		Dir: t.TempDir(),
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") != "kubectl get nodes -o json" {
				return nil, errors.New("unexpected command")
			}

			asked.Store(true)

			return []byte(kubelets), nil
		},
	})

	return server
}

// TestAControllerRefusesToStrandAKubelet is ADR 12: a minor move that would
// leave a worker two minors behind is refused before anything is fetched.
func TestAControllerRefusesToStrandAKubelet(t *testing.T) {
	t.Parallel()

	var asked atomic.Bool

	server := serverWithKubelets(t, newAuthority(t), strandedWorker, &asked)

	_, err := server.swapKubernetesVersion(context.Background(), &config.Config{
		Role:       config.RoleController,
		Kubernetes: config.Kubernetes{Version: "v1.36.4+k0s.0"},
	})

	if !errors.Is(err, k0s.ErrSkew) || !strings.Contains(err.Error(), "forgotten (v1.34.11+k0s)") {
		t.Fatalf("error = %v, want a skew refusal naming the forgotten worker", err)
	}
}

// TestAWorkerDoesNotAskForKubelets: it holds no credentials to list them.
func TestAWorkerDoesNotAskForKubelets(t *testing.T) {
	t.Parallel()

	var asked atomic.Bool

	server := serverWithKubelets(t, newAuthority(t), strandedWorker, &asked)

	plan, err := server.checkKubernetesVersion(&config.Config{
		Role:       config.RoleWorker,
		Kubernetes: config.Kubernetes{Version: "v1.36.4+k0s.0"},
	})
	if err != nil {
		t.Fatalf("checkKubernetesVersion() error = %v", err)
	}

	if _, err := server.checkKubelets(context.Background(), config.RoleWorker, plan); err != nil {
		t.Errorf("checkKubelets() error = %v", err)
	}

	if asked.Load() {
		t.Error("a worker listed the cluster's nodes")
	}
}

// TestCheckReportsLaggingKubelets is what cctl warns from.
func TestCheckReportsLaggingKubelets(t *testing.T) {
	t.Parallel()

	ca := newAuthority(t)

	var asked atomic.Bool

	server := serverWithKubelets(t, ca, laggingWorker, &asked)

	if err := os.WriteFile(server.configPath, []byte(aDocument), 0o600); err != nil {
		t.Fatalf("seeding the configuration: %v", err)
	}

	address := serveOn(t, server)
	admin := client(t, server.store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/kubernetes/check",
		`{"version":"v1.36.4+k0s.0"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}

	lagging, _ := body["lagging"].([]any)
	if len(lagging) != 1 {
		t.Fatalf("lagging = %v, want c1", body["lagging"])
	}

	if entry, _ := lagging[0].(map[string]any); entry["name"] != "c1" {
		t.Errorf("lagging = %v, want c1", lagging)
	}
}
