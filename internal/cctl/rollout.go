package cctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// Rollout upgrades a set of nodes, one at a time.
//
// This is the part ADR 4 keeps out of the daemon on purpose: a node knows only
// about itself, and deciding whether it is safe to take one out of service is
// a question about the others. It lives here, on the operator's machine, where
// the list of nodes exists.
type Rollout struct {
	// Image is what every node is moved to.
	Image string

	// Nodes are the addresses, in the order they are upgraded. A control plane
	// is safest upgraded one controller at a time, which is the default and
	// the only mode offered.
	Nodes []string

	// Connect opens a client for an address.
	Connect func(address string) (*Client, error)

	// Settle is how long to wait for a node to come back before giving up on
	// it. A reboot into a new OS image, with k0s starting and etcd rejoining,
	// is minutes rather than seconds.
	Settle time.Duration

	// Poll is how often to ask whether it is back.
	Poll time.Duration

	// Out is where progress is reported.
	Out io.Writer
}

// Defaults for a rollout, chosen from what a reboot actually costs.
const (
	DefaultSettle = 15 * time.Minute
	DefaultPoll   = 10 * time.Second
)

// ErrUnhealthy reports a node that was not in a fit state to be upgraded.
var ErrUnhealthy = errors.New("node is not healthy")

// Run upgrades every node in turn, stopping at the first one that fails.
//
// Stopping is the whole point. A rollout that carries on past a node which did
// not come back turns one broken machine into a broken cluster, and the second
// failure is always cheaper to prevent than the tenth.
func (r *Rollout) Run(ctx context.Context) error {
	r.applyDefaults()

	for i, address := range r.Nodes {
		r.say("[%d/%d] %s\n", i+1, len(r.Nodes), address)

		if err := r.upgradeOne(ctx, address); err != nil {
			r.say("        failed: %v\n", err)

			return fmt.Errorf("%s: %w. %d of %d nodes were upgraded; "+
				"the rest are untouched", address, err, i, len(r.Nodes))
		}
	}

	r.say("\nAll %d nodes are running %s.\n", len(r.Nodes), r.Image)

	return nil
}

func (r *Rollout) upgradeOne(ctx context.Context, address string) error {
	client, err := r.Connect(address)
	if err != nil {
		return err
	}

	// Before anything: is this node in a state where taking it down is safe?
	// A node that is already unhealthy is one whose workloads have nowhere
	// good to go.
	before, err := client.Node(ctx)
	if err != nil {
		return err
	}

	if err := healthy(before); err != nil {
		return err
	}

	// bootc's own progress, indented under the node it belongs to, so a pull of
	// several hundred megabytes is visibly moving rather than a silent wait.
	staged, err := client.Stage(ctx, r.Image, func(line string) {
		r.say("        %s\n", line)
	})
	if err != nil {
		return err
	}

	r.say("        staged %s\n", staged.Digest)

	if err := client.Apply(ctx); err != nil {
		return err
	}

	r.say("        draining and rebooting")

	after, err := r.waitForReturn(ctx, address)
	if err != nil {
		return err
	}

	// The check that makes the rollout worth doing one at a time: the node is
	// not merely reachable, it came back on the image it was sent to and its
	// Kubernetes is running.
	if booted := after.OS.Booted; booted == nil || booted.Digest != staged.Digest {
		got := "nothing"
		if booted != nil {
			got = booted.Digest
		}

		return fmt.Errorf("came back on %s, not the staged %s", got, staged.Digest)
	}

	if err := healthy(after); err != nil {
		return err
	}

	r.say("        up on %s\n", after.OS.Booted.Digest)

	return nil
}

// waitForReturn polls until the node answers again.
//
// The connection is expected to fail for a while: the machine is rebooting.
// Failures are the normal state here, and only the deadline ends the wait.
func (r *Rollout) waitForReturn(ctx context.Context, address string) (*nodeinfo.Node, error) {
	deadline := time.Now().Add(r.Settle)

	ticker := time.NewTicker(r.Poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}

		r.say(".")

		if node, err := r.probe(ctx, address); err == nil {
			r.say("\n")

			return node, nil
		}

		if time.Now().After(deadline) {
			r.say("\n")

			return nil, fmt.Errorf("did not come back within %s", r.Settle)
		}
	}
}

func (r *Rollout) probe(ctx context.Context, address string) (*nodeinfo.Node, error) {
	client, err := r.Connect(address)
	if err != nil {
		return nil, err
	}

	return client.Node(ctx)
}

// healthy decides whether a node is fit to be taken out of service, or has
// come back properly.
//
// It asks only what a node can answer about itself. Whether the *cluster* can
// afford to lose this node is a Kubernetes question, and this tool does not
// hold a kubeconfig -- so the honest check is that the node is bootstrapped,
// k0s is running, and the last boot was not judged bad.
func healthy(node *nodeinfo.Node) error {
	switch {
	case !node.Bootstrapped:
		return fmt.Errorf("%w: it was never bootstrapped", ErrUnhealthy)
	case node.Kubernetes.Service != "" && !node.Kubernetes.Active:
		return fmt.Errorf("%w: %s is not running", ErrUnhealthy, node.Kubernetes.Service)
	case node.Health.Greenboot == "failed":
		return fmt.Errorf("%w: greenboot judged this boot bad", ErrUnhealthy)
	default:
		return nil
	}
}

func (r *Rollout) applyDefaults() {
	if r.Settle == 0 {
		r.Settle = DefaultSettle
	}

	if r.Poll == 0 {
		r.Poll = DefaultPoll
	}

	if r.Out == nil {
		r.Out = io.Discard
	}
}

func (r *Rollout) say(format string, args ...any) {
	_, _ = fmt.Fprintf(r.Out, format, args...)
}
