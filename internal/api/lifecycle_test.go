package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/lifecycle"
)

// lifecycleNode starts a claimed node whose machine commands are stubbed.
func lifecycleNode(t *testing.T, run lifecycle.Runner) (*authority, string, *Store, string) {
	t.Helper()

	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if err := store.RecordClaim(ClaimedWithPairingCode); err != nil {
		t.Fatalf("RecordClaim() error = %v", err)
	}

	if _, err := store.Identity(); err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	dir := t.TempDir()
	server.Lifecycle(&lifecycle.Manager{Run: run, Dir: dir, Hostname: "worker-01"})

	return ca, serveOn(t, server), store, dir
}

// machine records what the node was asked to do to itself.
//
// It is mutex-guarded because the handler keeps working after the response has
// been flushed -- reboot and shutdown answer first and act second, on purpose
// -- so a test reading this while the handler is still writing to it is the
// normal case rather than a rare one.
type machine struct {
	mu        sync.Mutex
	calls     []string
	reachable bool
}

func (m *machine) runner() lifecycle.Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")

		m.mu.Lock()
		m.calls = append(m.calls, line)
		m.mu.Unlock()

		if strings.Contains(line, "kubectl get node") && !m.reachable {
			return nil, errors.New("no access")
		}

		return nil, nil
	}
}

func (m *machine) ran(fragment string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, call := range m.calls {
		if strings.Contains(call, fragment) {
			return true
		}
	}

	return false
}

// waitFor gives the handler the moment it needs to finish what it said it
// would do after answering.
func (m *machine) waitFor(t *testing.T, fragment string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if m.ran(fragment) {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Errorf("the node never ran %q", fragment)
}

// quiet asserts something was not run, after giving it a chance to be.
func (m *machine) quiet(t *testing.T, fragment string) {
	t.Helper()

	time.Sleep(50 * time.Millisecond)

	if m.ran(fragment) {
		t.Errorf("the node ran %q", fragment)
	}
}

func TestCordonAndDrainNeedOperatorAndTheDestructiveOnesNeedAdmin(t *testing.T) {
	m := &machine{reachable: true}
	ca, address, store, _ := lifecycleNode(t, m.runner())

	readonly := client(t, store, ca.issue(t, RoleReadOnly))
	operator := client(t, store, ca.issue(t, RoleOperator))

	for path, c := range map[string]*http.Client{
		"/v1/lifecycle/cordon":   readonly,
		"/v1/lifecycle/drain":    readonly,
		"/v1/lifecycle/reboot":   operator,
		"/v1/lifecycle/shutdown": operator,
		"/v1/lifecycle/reset":    operator,
	} {
		if status, _ := postRaw(t, c, "https://"+address+path, "{}"); status != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", path, status)
		}
	}

	m.quiet(t, "systemctl reboot")
	m.quiet(t, "k0s reset")
}

func TestAWorkerSaysItCannotDrainRatherThanFailing(t *testing.T) {
	// 409 rather than 403: nothing is wrong with the caller, this node simply
	// holds no credentials that can evict a pod.
	m := &machine{reachable: false}
	ca, address, store, _ := lifecycleNode(t, m.runner())

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleOperator)),
		"https://"+address+"/v1/lifecycle/drain", "{}")

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "controller") {
		t.Errorf("error = %q, want it to say which nodes can", message)
	}
}

func TestResetRefusesWithoutTheNodeName(t *testing.T) {
	// The one call no other call can undo, and an address in a shell's history
	// is a poor guard against it landing on the wrong machine.
	m := &machine{reachable: true}
	ca, address, store, _ := lifecycleNode(t, m.runner())

	admin := client(t, store, ca.issue(t, RoleAdmin))

	for _, body := range []string{`{}`, `{"confirm":""}`, `{"confirm":"some-other-node"}`} {
		status, reply := postRaw(t, admin, "https://"+address+"/v1/lifecycle/reset", body)
		if status != http.StatusBadRequest {
			t.Errorf("reset with %s = %d, want 400 (%v)", body, status, reply)
		}
	}

	m.quiet(t, "k0s reset")

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if !enrolled {
		t.Error("an unconfirmed reset dropped the node's enrolment")
	}
}

func TestResetLeavesTheClusterBeforeItForgetsItsOwner(t *testing.T) {
	// The order is the whole property. A node that dropped its enrolment and
	// then failed to leave would be a cluster member nobody owns -- the one
	// state this design exists to make unreachable.
	m := &machine{reachable: true}
	ca, address, store, dir := lifecycleNode(t, m.runner())

	for _, name := range []string{"bootstrapped", "node.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/lifecycle/reset", `{"confirm":"worker-01"}`)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%v)", status, body)
	}

	// The reboot comes after the response, so it is waited for rather than
	// assumed to have happened by the time the body arrived.
	m.waitFor(t, "systemctl reboot")

	if !m.ran("k0s reset") {
		t.Error("the node did not leave its cluster")
	}

	// Drained first, best effort.
	if !m.ran("kubectl drain") {
		t.Error("the node was not drained before being erased")
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("the node still believes it has an owner")
	}

	// The serving identity goes too: a machine handed on with the certificate
	// its previous owner pinned is one that owner's tooling still accepts.
	for _, name := range []string{operatorCAFile, claimFile, serverCertFile, serverKeyFile} {
		if _, err := os.Stat(store.path(name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the reset", name)
		}
	}

	for _, name := range []string{"bootstrapped", "node.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the reset", name)
		}
	}
}

func TestResetStillWorksWhenTheClusterIsAlreadyGone(t *testing.T) {
	// A node whose cluster has gone is exactly the node somebody wants to
	// reset, so a drain that cannot run must not stop it.
	m := &machine{reachable: false}
	ca, address, store, _ := lifecycleNode(t, m.runner())

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/lifecycle/reset", `{"confirm":"worker-01"}`)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%v)", status, body)
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("the node was not reset")
	}
}

func TestRebootAnswersBeforeItGoes(t *testing.T) {
	// A client must be able to tell "the node refused" from "the node obeyed";
	// without a reply, both look like a connection that died.
	m := &machine{reachable: true}
	ca, address, store, _ := lifecycleNode(t, m.runner())

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/lifecycle/reboot", "{}")

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}

	if body["status"] != "rebooting" {
		t.Errorf("body = %v, want it to say what is happening", body)
	}
}
