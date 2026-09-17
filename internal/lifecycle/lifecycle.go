// Package lifecycle does the things to a machine that cannot be undone by
// running them again: taking it out of service, draining it, rebooting it, and
// erasing what makes it a cluster member.
//
// It holds no policy about who may ask. That lives with the API, which knows
// about certificates and roles; this package knows about a machine.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StateDir is Corium's own state, the part of it this package erases.
const StateDir = "/var/lib/corium"

// CordonMarker records that Corium cordoned this node rather than a person.
//
// The name and the path are shared with corium-upgrade-apply and
// corium-uncordon, deliberately: a node cordoned by the API and rebooted by the
// upgrade path must still uncordon itself, and it only will if both agree on
// where the marker lives.
const CordonMarker = "cordoned-by-corium"

// DefaultDrainTimeout matches what the upgrade path allows. A drain that has
// not finished in five minutes is usually a pod disruption budget saying no,
// which more time does not change.
const DefaultDrainTimeout = 5 * time.Minute

// commandTimeout bounds everything except a drain, which carries its own.
const commandTimeout = 2 * time.Minute

// ErrNoClusterAccess reports a node that cannot talk to its own cluster.
//
// Only a node running a control plane has admin credentials locally. A plain
// worker holds kubelet credentials, which cannot evict pods -- so cordon and
// drain are not available there, and saying so is better than a failure that
// reads like a broken cluster.
var ErrNoClusterAccess = errors.New(
	"this node has no cluster admin credentials; only a controller has them locally")

// Runner executes a command. It exists so this package can be tested without a
// machine to wreck.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager acts on this node.
type Manager struct {
	// Run executes commands. Nil means actually execute them.
	Run Runner

	// Dir overrides StateDir. Empty means the real one.
	Dir string

	// Hostname overrides the node's name. Empty means ask the machine.
	Hostname string
}

func (m *Manager) dir() string {
	if m.Dir != "" {
		return m.Dir
	}

	return StateDir
}

// Node is the name this machine registers under in Kubernetes.
func (m *Manager) Node() (string, error) {
	if m.Hostname != "" {
		return m.Hostname, nil
	}

	name, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("reading the hostname: %w", err)
	}

	return name, nil
}

// CanReachCluster reports whether this node can act on its own Node object.
func (m *Manager) CanReachCluster(ctx context.Context) bool {
	node, err := m.Node()
	if err != nil {
		return false
	}

	_, err = m.run(ctx, commandTimeout, "k0s", "kubectl", "get", "node", node)

	return err == nil
}

// Cordon stops new pods being scheduled here.
func (m *Manager) Cordon(ctx context.Context) error {
	node, err := m.Node()
	if err != nil {
		return err
	}

	if !m.CanReachCluster(ctx) {
		return ErrNoClusterAccess
	}

	// The marker is written before the cordon, not after: if the machine dies
	// between the two, a node that is cordoned without the marker stays
	// cordoned forever, while a marker without a cordon costs one harmless
	// uncordon at the next boot.
	if err := m.mark(); err != nil {
		return err
	}

	if _, err := m.run(ctx, commandTimeout, "k0s", "kubectl", "cordon", node); err != nil {
		return fmt.Errorf("cordoning %s: %w", node, err)
	}

	return nil
}

// Uncordon puts the node back into service.
func (m *Manager) Uncordon(ctx context.Context) error {
	node, err := m.Node()
	if err != nil {
		return err
	}

	if !m.CanReachCluster(ctx) {
		return ErrNoClusterAccess
	}

	if _, err := m.run(ctx, commandTimeout, "k0s", "kubectl", "uncordon", node); err != nil {
		return fmt.Errorf("uncordoning %s: %w", node, err)
	}

	// Only once the cluster has agreed. A marker removed before the uncordon
	// succeeded would leave nothing to retry from.
	return os.Remove(filepath.Join(m.dir(), CordonMarker))
}

// Drain evicts this node's workloads, cordoning it first.
//
// A drain that cannot finish is not forced. It usually means a pod disruption
// budget is saying this workload cannot lose a replica right now, which is
// exactly the situation where overriding it is wrong -- and the node is left
// cordoned so the operator can decide, rather than silently returned to
// service with the problem hidden.
func (m *Manager) Drain(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultDrainTimeout
	}

	node, err := m.Node()
	if err != nil {
		return err
	}

	if err := m.Cordon(ctx); err != nil {
		return err
	}

	// The same flags as corium-upgrade-apply, so a drain through the API and a
	// drain during an upgrade behave identically. DaemonSet pods cannot be
	// evicted and emptyDir data is by definition not worth keeping.
	_, err = m.run(ctx, timeout+commandTimeout, "k0s", "kubectl", "drain", node,
		"--ignore-daemonsets",
		"--delete-emptydir-data",
		"--timeout="+timeout.String())
	if err != nil {
		return fmt.Errorf("draining %s: %w -- the node stays cordoned", node, err)
	}

	return nil
}

// Reboot restarts the machine.
func (m *Manager) Reboot(ctx context.Context) error {
	if _, err := m.run(ctx, commandTimeout, "systemctl", "reboot"); err != nil {
		return fmt.Errorf("rebooting: %w", err)
	}

	return nil
}

// Shutdown powers the machine off.
//
// On anything but a machine somebody can physically reach, this is the one
// call in the API with no way back: nothing here can turn it on again.
func (m *Manager) Shutdown(ctx context.Context) error {
	if _, err := m.run(ctx, commandTimeout, "systemctl", "poweroff"); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	return nil
}

// LeaveCluster stops k0s and erases everything that made this machine a member.
//
// This is the irreversible half of a reset, and it is done first. The order is
// the point: a node that dropped its enrolment and then failed to leave would
// be a cluster member nobody owns, which is the one state the whole design
// exists to make unreachable.
func (m *Manager) LeaveCluster(ctx context.Context) error {
	// `k0s stop` on a node where k0s was never installed exits non-zero, and
	// that is not a failure to reset -- there is simply nothing running.
	_, _ = m.run(ctx, commandTimeout, "k0s", "stop")

	if _, err := m.run(ctx, commandTimeout, "k0s", "reset"); err != nil {
		return fmt.Errorf("resetting k0s: %w", err)
	}

	return nil
}

// ForgetBootstrap removes what tells this node it has already been configured.
//
// Without it the machine would come back up believing it had joined a cluster
// it has just been erased from, and corium-bootstrap would decline to run.
func (m *Manager) ForgetBootstrap() error {
	for _, name := range []string{"bootstrapped", "node.json", CordonMarker} {
		if err := os.Remove(filepath.Join(m.dir(), name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", name, err)
		}
	}

	return nil
}

func (m *Manager) mark() error {
	if err := os.MkdirAll(m.dir(), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", m.dir(), err)
	}

	// The name is a constant in this package and the directory is overridden
	// only by tests: nothing a client sends reaches either. Keep it that way.
	path := filepath.Join(m.dir(), CordonMarker)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: both parts are package constants
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return file.Close()
}

func (m *Manager) run(
	ctx context.Context, timeout time.Duration, name string, args ...string,
) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}
