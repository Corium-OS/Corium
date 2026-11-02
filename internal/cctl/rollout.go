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

	// DryRun stops after every node has been checked.
	DryRun bool
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

	if len(r.Nodes) == 0 {
		return errors.New("no nodes to upgrade")
	}

	clients, err := r.preflight(ctx)
	if err != nil {
		return err
	}

	if r.DryRun {
		r.say("\nDry run: every node accepts %s. Nothing was changed.\n", r.Image)

		return nil
	}

	r.say("\n")

	for i, address := range r.Nodes {
		r.say("[%d/%d] %s\n", i+1, len(r.Nodes), address)

		if err := r.upgradeOne(ctx, clients[address], address); err != nil {
			r.say("        failed: %v\n", err)

			return fmt.Errorf("%s: %w. %d of %d nodes were upgraded; "+
				"the rest are untouched", address, err, i, len(r.Nodes))
		}
	}

	r.say("\nAll %d nodes are running %s.\n", len(r.Nodes), r.Image)

	return nil
}

// preflight asks every node whether it would take the image before the first
// one is rebooted, so a bad address or a refused image cannot leave the cluster
// half-way.
func (r *Rollout) preflight(ctx context.Context) (map[string]*Client, error) {
	r.say("Checking %d nodes can move to %s\n", len(r.Nodes), r.Image)

	clients := make(map[string]*Client, len(r.Nodes))
	cluster := ""
	refused := 0

	for _, address := range r.Nodes {
		if _, seen := clients[address]; seen {
			r.say("  %s: listed twice\n", address)
			refused++

			continue
		}

		client, node, err := inspect(ctx, r.Connect, address, &cluster)
		clients[address] = client

		if err == nil {
			err = client.UpgradeCheck(ctx, r.Image)
		}

		switch {
		case errors.Is(err, ErrUnknownRoute):
			r.say("  %s: on %s; predates the check, it will check when staged\n",
				address, bootedImageOf(node))
		case err != nil:
			r.say("  %s: %v\n", address, err)
			refused++
		default:
			r.say("  %s: on %s\n", address, bootedImageOf(node))
		}
	}

	if refused > 0 {
		return nil, fmt.Errorf("%d of %d nodes cannot move to %s; nothing was changed",
			refused, len(r.Nodes), r.Image)
	}

	return clients, nil
}

// inspect reads a node and refuses one that is unhealthy or in another cluster
// than the first seen, which cluster carries. Shared by both rollouts.
func inspect(
	ctx context.Context, connect func(string) (*Client, error), address string, cluster *string,
) (*Client, *nodeinfo.Node, error) {
	client, err := connect(address)
	if err != nil {
		return nil, nil, err
	}

	node, err := client.Node(ctx)
	if err != nil {
		return client, nil, err
	}

	if err := healthy(node); err != nil {
		return client, node, err
	}

	if node.Cluster != "" {
		if *cluster == "" {
			*cluster = node.Cluster
		} else if node.Cluster != *cluster {
			return client, node, fmt.Errorf("in cluster %q, the others in %q", node.Cluster, *cluster)
		}
	}

	return client, node, nil
}

// bootedImageOf names what a node runs, for the preflight line.
func bootedImageOf(node *nodeinfo.Node) string {
	switch {
	case node == nil || node.OS.Booted == nil:
		return "an unreported image"
	case node.OS.Booted.Image != "":
		return node.OS.Booted.Image
	case node.OS.Booted.Digest != "":
		return node.OS.Booted.Digest
	default:
		return "an unreported image"
	}
}

func (r *Rollout) upgradeOne(ctx context.Context, client *Client, address string) error {
	// Before anything: is this node in a state where taking it down is safe?
	// A node that is already unhealthy is one whose workloads have nowhere
	// good to go. Asked again after the preflight: an earlier node's reboot may
	// have changed the answer.
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

	// The uptime before the reboot is what tells a real return from a node that
	// merely answered: Apply starts the drain-and-reboot asynchronously, so the
	// node keeps replying for a while, and reading its still-running state as
	// "came back" is how an upgrade that never rebooted gets called a failure on
	// the wrong image. waitForReturn holds out for an uptime lower than this.
	after, err := r.waitForReturn(ctx, address, before.Health.UptimeSeconds)
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

// waitForReturn polls until the node answers again on a fresh boot.
//
// The connection is expected to fail for a while: the machine is rebooting.
// Failures are the normal state here. But a success is not enough on its own --
// the drain runs asynchronously and the node answers throughout it, so a reply
// is accepted only once its uptime has dropped below what it was before Apply,
// which is the one thing that cannot be true without a reboot in between. Only
// the deadline ends the wait.
func (r *Rollout) waitForReturn(ctx context.Context, address string, beforeUptime int64) (*nodeinfo.Node, error) {
	deadline := time.Now().Add(r.Settle)

	ticker := time.NewTicker(r.Poll)
	defer ticker.Stop()

	// Set once the node has answered without having rebooted, so the timeout can
	// say which of the two failures this was: a node that never came back at
	// all, or one that stayed up because its drain never let it reboot.
	answeredWithoutReboot := false

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}

		r.say(".")

		if node, err := r.probe(ctx, address); err == nil {
			if rebooted(beforeUptime, node) {
				r.say("\n")

				return node, nil
			}

			answeredWithoutReboot = true
		}

		if time.Now().After(deadline) {
			r.say("\n")

			if answeredWithoutReboot {
				return nil, fmt.Errorf(
					"stayed up and did not reboot within %s, so the staged image was "+
						"never applied -- its drain may not have finished", r.Settle)
			}

			return nil, fmt.Errorf("did not come back within %s", r.Settle)
		}
	}
}

// rebooted reports whether the node has restarted since it was last seen.
//
// A reboot resets uptime, so an uptime below the one recorded before Apply is
// the proof a reply came from a fresh boot rather than from a node still on its
// way down. When the earlier uptime is unknown -- a node old enough not to
// report it -- there is nothing to compare against, so the reply is accepted
// rather than waited on forever: the weaker guarantee this had before uptime
// was reported, and no worse than it.
func rebooted(beforeUptime int64, node *nodeinfo.Node) bool {
	if beforeUptime <= 0 {
		return true
	}

	return node.Health.UptimeSeconds < beforeUptime
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
