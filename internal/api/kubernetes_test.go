package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	if got.running != "v1.36.4+k0s.0" {
		t.Errorf("got %q, want the floor v1.36.4+k0s.0", got.running)
	}

	if got.backup != "" {
		t.Errorf("a no-op swap took a snapshot: %s", got.backup)
	}
}

// fakeK0s stands in for k0s backup, running script.
func fakeK0s(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "k0s")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}

	return path
}

// TestSnapshotBeforeSwap covers which roles hold a datastore worth saving, and
// that a failed snapshot is an error rather than a swap without one.
func TestSnapshotBeforeSwap(t *testing.T) {
	t.Parallel()

	ca := newAuthority(t)

	tests := []struct {
		name       string
		role       config.Role
		script     string
		wantBackup bool
		wantErr    bool
	}{
		{name: "single, on kine/SQLite", role: config.RoleSingle,
			script: `printf '\037\213'`, wantBackup: true},
		{name: "controller, on etcd", role: config.RoleController,
			script: `printf '\037\213'`, wantBackup: true},
		{name: "controller+worker", role: config.RoleControllerWorker,
			script: `printf '\037\213'`, wantBackup: true},
		{name: "a worker has no datastore", role: config.RoleWorker,
			script: `exit 1`},
		{name: "k0s refuses", role: config.RoleController,
			script: `echo 'unable to detect cluster status' >&2; exit 1`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := serverWithWindow(t, ca)
			server.k0sBinary = fakeK0s(t, test.script)
			dir := filepath.Join(t.TempDir(), "backups")

			path, err := server.snapshotBeforeSwap(context.Background(), &config.Config{
				Role:   test.role,
				Backup: config.Backup{Path: dir},
			})

			if (err != nil) != test.wantErr {
				t.Fatalf("snapshotBeforeSwap() error = %v, want error %v", err, test.wantErr)
			}

			if test.wantBackup != (path != "") {
				t.Fatalf("snapshotBeforeSwap() = %q, want a snapshot %v", path, test.wantBackup)
			}

			if test.wantBackup && filepath.Dir(path) != dir {
				t.Errorf("snapshot written to %s, want it under backup.path %s", path, dir)
			}
		})
	}
}

// TestCancelSwapSaysNothingChanged pins the message an operator acts on when a
// snapshot fails on a node that could not drain.
func TestCancelSwapSaysNothingChanged(t *testing.T) {
	t.Parallel()

	server := serverWithWindow(t, newAuthority(t))
	cause := errors.New("snapshot failed")

	err := server.cancelSwap(context.Background(), false, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("cancelSwap() = %v, want it to wrap the cause and say nothing changed", err)
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

// TestSwapRefusesWhileOneIsRunning covers a client retrying while the swap it
// abandoned is still running on the node.
func TestSwapRefusesWhileOneIsRunning(t *testing.T) {
	t.Parallel()

	server := serverWithWindow(t, newAuthority(t))

	server.swapping.Lock()
	defer server.swapping.Unlock()

	_, err := server.swapKubernetesVersion(context.Background(),
		&config.Config{
			Role:       config.RoleSingle,
			Kubernetes: config.Kubernetes{Version: "v1.36.4+k0s.0"},
		})
	if !errors.Is(err, ErrSwapInProgress) {
		t.Fatalf("error is %v, want %v", err, ErrSwapInProgress)
	}
}

// TestSwapOutlivesTheClient covers a client that leaves once the node has
// started draining: a ^C, a laptop going to sleep, `cctl k8s upgrade --timeout`.
// The swap must still reach systemd rather than leave k0s stopped on a
// cordoned node.
func TestSwapOutlivesTheClient(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")

	// systemctl is the first step after the drain that the Runner does not
	// stub, so a stand-in on PATH records whether the swap got that far.
	script := "#!/bin/sh\necho \"$@\" >> " + calls + "\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o700); err != nil {
		t.Fatalf("writing the systemctl stand-in: %v", err)
	}

	t.Setenv("PATH", bin)

	clientCtx, leave := context.WithCancel(context.Background())
	defer leave()

	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "drain" {
			leave()
		}

		return nil, nil
	}

	server := serverWithWindow(t, newAuthority(t))
	server.Lifecycle(&lifecycle.Manager{Run: run, Dir: t.TempDir(), Hostname: "node-01"})

	// The snapshot runs after the client left too; a canceled one would
	// cancel the swap before the stop.
	server.k0sBinary = fakeK0s(t, `printf '\037\213'`)
	backups := t.TempDir()

	floor, err := k0s.ParseVersion("v1.36.4+k0s.0")
	if err != nil {
		t.Fatal(err)
	}

	next, err := k0s.ParseVersion("v1.36.4+k0s.1")
	if err != nil {
		t.Fatal(err)
	}

	// Fails at Activate, which finds nothing downloaded; what matters is that
	// it got there with a live context.
	_, err = server.commitSwap(clientCtx,
		kubernetesPlan{active: floor, requested: next},
		&config.Config{Role: config.RoleSingle, Backup: config.Backup{Path: backups}})
	if errors.Is(err, context.Canceled) {
		t.Fatalf("the swap stopped with its client: %v", err)
	}

	if err == nil || !strings.Contains(err.Error(), "snapshot taken before the stop is "+backups) {
		t.Errorf("swap error = %v, want it to name the snapshot under %s", err, backups)
	}

	recorded, readErr := os.ReadFile(calls)
	if readErr != nil || !strings.HasPrefix(string(recorded), "stop ") {
		t.Fatalf("k0s was never stopped after the client left (swap error = %v)", err)
	}
}
