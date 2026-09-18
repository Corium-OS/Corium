package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// machine stands in for a node, recording what would have been done to it.
type machine struct {
	calls []string

	// reachable decides whether this node can act on its own Node object.
	reachable bool

	// fail makes a named subcommand refuse.
	fail map[string]error
}

func (m *machine) runner() Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		m.calls = append(m.calls, line)

		if strings.Contains(line, "kubectl get node") && !m.reachable {
			return nil, errors.New("the server could not find the requested resource")
		}

		for fragment, err := range m.fail {
			if strings.Contains(line, fragment) {
				return nil, err
			}
		}

		return nil, nil
	}
}

func (m *machine) ran(fragment string) bool {
	for _, call := range m.calls {
		if strings.Contains(call, fragment) {
			return true
		}
	}

	return false
}

func newManager(t *testing.T, m *machine) *Manager {
	t.Helper()

	return &Manager{Run: m.runner(), Dir: t.TempDir(), Hostname: "worker-01"}
}

func TestAWorkerCannotCordonItself(t *testing.T) {
	// Only a controller holds cluster admin credentials locally. A worker's
	// kubelet credentials cannot evict pods, and saying so is better than a
	// failure that reads like a broken cluster.
	m := &machine{reachable: false}
	manager := newManager(t, m)

	for name, err := range map[string]error{
		"Cordon":   manager.Cordon(t.Context()),
		"Uncordon": manager.Uncordon(t.Context()),
		"Drain":    manager.Drain(t.Context(), time.Second),
	} {
		if !errors.Is(err, ErrNoClusterAccess) {
			t.Errorf("%s() = %v, want %v", name, err, ErrNoClusterAccess)
		}
	}

	if m.ran("kubectl cordon") || m.ran("kubectl drain") {
		t.Error("a refused call still reached the cluster")
	}
}

func TestTheCordonMarkerIsWrittenBeforeTheCordon(t *testing.T) {
	// If the machine dies between the two, a node cordoned without a marker
	// stays cordoned forever, while a marker without a cordon costs one
	// harmless uncordon at the next boot.
	m := &machine{reachable: true, fail: map[string]error{
		"kubectl cordon": errors.New("connection refused"),
	}}
	manager := newManager(t, m)

	if err := manager.Cordon(t.Context()); err == nil {
		t.Fatal("Cordon() = nil, want the failure to surface")
	}

	if _, err := os.Stat(filepath.Join(manager.Dir, CordonMarker)); err != nil {
		t.Errorf("the marker was not written before the cordon was attempted: %v", err)
	}
}

func TestUncordonKeepsTheMarkerUntilTheClusterAgrees(t *testing.T) {
	m := &machine{reachable: true, fail: map[string]error{
		"kubectl uncordon": errors.New("connection refused"),
	}}
	manager := newManager(t, m)

	if err := manager.Cordon(t.Context()); err != nil {
		t.Fatalf("Cordon() error = %v", err)
	}

	if err := manager.Uncordon(t.Context()); err == nil {
		t.Fatal("Uncordon() = nil, want the failure to surface")
	}

	// A marker removed before the uncordon succeeded would leave nothing to
	// retry from, and the node would sit Ready and empty forever.
	if _, err := os.Stat(filepath.Join(manager.Dir, CordonMarker)); err != nil {
		t.Errorf("the marker went even though the uncordon failed: %v", err)
	}
}

func TestDrainCordonsFirstAndDoesNotForce(t *testing.T) {
	m := &machine{reachable: true}
	manager := newManager(t, m)

	if err := manager.Drain(t.Context(), 30*time.Second); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	if !m.ran("kubectl cordon") {
		t.Error("Drain() did not cordon first")
	}

	// The same flags as corium-upgrade-apply, so the two paths behave
	// identically -- and nothing that overrides a pod disruption budget.
	drain := ""

	for _, call := range m.calls {
		if strings.Contains(call, "kubectl drain") {
			drain = call
		}
	}

	for _, want := range []string{"--ignore-daemonsets", "--delete-emptydir-data", "--timeout=30s"} {
		if !strings.Contains(drain, want) {
			t.Errorf("drain ran as %q, want %q", drain, want)
		}
	}

	if strings.Contains(drain, "--force") || strings.Contains(drain, "--disable-eviction") {
		t.Errorf("drain overrides the cluster's own guard: %q", drain)
	}
}

func TestADrainThatFailsLeavesTheNodeCordoned(t *testing.T) {
	// A refused eviction is a pod disruption budget working. Returning the
	// node to service would hide that.
	m := &machine{reachable: true, fail: map[string]error{
		"kubectl drain": errors.New("cannot evict pod as it would violate the budget"),
	}}
	manager := newManager(t, m)

	err := manager.Drain(t.Context(), time.Second)
	if err == nil {
		t.Fatal("Drain() = nil, want a failure")
	}

	if !strings.Contains(err.Error(), "stays cordoned") {
		t.Errorf("error = %q, want it to say the node is still cordoned", err)
	}

	if m.ran("kubectl uncordon") {
		t.Error("a failed drain put the node back into service")
	}
}

func TestLeavingTheClusterSurvivesKzeroSNotRunning(t *testing.T) {
	// `k0s stop` exits non-zero where k0s was never installed, and that is not
	// a failure to reset -- there is nothing running.
	m := &machine{reachable: true, fail: map[string]error{
		"k0s stop": errors.New("Unit k0sworker.service not loaded"),
	}}

	if err := newManager(t, m).LeaveCluster(t.Context()); err != nil {
		t.Fatalf("LeaveCluster() error = %v", err)
	}

	if !m.ran("k0s reset") {
		t.Error("LeaveCluster() did not reset k0s")
	}
}

func TestForgetBootstrapRemovesWhatWouldStopAReprovision(t *testing.T) {
	manager := newManager(t, &machine{reachable: true})

	for _, name := range []string{"bootstrapped", "node.json", CordonMarker} {
		if err := os.WriteFile(filepath.Join(manager.Dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	if err := manager.ForgetBootstrap(); err != nil {
		t.Fatalf("ForgetBootstrap() error = %v", err)
	}

	// Left behind, the node would come back believing it had joined a cluster
	// it has just been erased from, and corium-bootstrap would decline to run.
	for _, name := range []string{"bootstrapped", "node.json", CordonMarker} {
		if _, err := os.Stat(filepath.Join(manager.Dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the reset", name)
		}
	}

	// And it is idempotent: a second reset must not fail on what is gone.
	if err := manager.ForgetBootstrap(); err != nil {
		t.Errorf("a second ForgetBootstrap() = %v, want nil", err)
	}
}
