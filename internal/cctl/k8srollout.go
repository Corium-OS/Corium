package cctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/k0s"
)

// KubernetesRollout moves a cluster to another k0s version, one node at a time.
//
// This is here and not in the daemon for the reason ADR 4 gives and ADR 10
// repeats: a node knows only about itself, and "controllers first, then
// workers" is a statement about the other nodes. The list of them exists on an
// operator's machine, so the ordering does too.
//
// The order is not a preference. k0s requires controllers to be upgraded before
// workers, and a worker may never be newer than the controllers it talks to, so
// a rollout that did workers first would produce exactly the skew k0s refuses --
// and would produce it on a running cluster rather than at a gate.
type KubernetesRollout struct {
	// Version is the k0s release every node is moved to.
	Version string

	// Mirror overrides where the extension is pulled from. Empty leaves each
	// node's own setting alone.
	Mirror string

	// Controllers are upgraded first, in order, one at a time.
	Controllers []string

	// Workers follow, once every controller is on the new version.
	Workers []string

	// Connect opens a client for an address.
	Connect func(address string) (*Client, error)

	// Timeout bounds one node's change: a download, a drain, and k0s coming
	// back. Unlike an OS upgrade there is no reboot, but a drain can still sit
	// at a pod disruption budget for a while.
	Timeout time.Duration

	// Out is where progress is reported.
	Out io.Writer

	// DryRun stops after every node has been checked.
	DryRun bool

	// Results holds one entry per listed node, in rollout order, once Run returns.
	Results []KubernetesNodeResult
}

// Outcomes a node can end a rollout with.
const (
	ResultRefused = "refused" // turned down by the preflight
	ResultChecked = "checked" // accepted, then left alone: a dry run or another node's refusal
	ResultSkipped = "skipped" // already on the target version
	ResultMoved   = "moved"
	ResultFailed  = "failed"
	ResultPending = "pending" // not reached: an earlier node failed
)

// KubernetesNodeResult is one node's part in a rollout, for automation.
type KubernetesNodeResult struct {
	Address string `json:"address"`
	Stage   string `json:"stage"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Result  string `json:"result"`
	Error   string `json:"error,omitempty"`
}

// KubernetesReport is the machine-readable summary of a rollout.
type KubernetesReport struct {
	Version string                 `json:"version"`
	DryRun  bool                   `json:"dryRun"`
	Nodes   []KubernetesNodeResult `json:"nodes"`
}

// Report summarises the rollout. It is complete once Run has returned.
func (r *KubernetesRollout) Report() KubernetesReport {
	nodes := r.Results
	if nodes == nil {
		nodes = []KubernetesNodeResult{}
	}

	return KubernetesReport{Version: r.Version, DryRun: r.DryRun, Nodes: nodes}
}

// rolloutStage is one group of nodes moved together, in order.
type rolloutStage struct {
	name  string
	nodes []string
}

// DefaultKubernetesTimeout is what one node is allowed. The download is the
// slow part -- a quarter of a gigabyte, possibly over a slow link -- and the
// drain after it is bounded by the node's own drain timeout.
const DefaultKubernetesTimeout = 30 * time.Minute

// Run moves every node in turn, stopping at the first that fails.
//
// Stopping matters more here than it does for an OS rollout. A cluster halted
// halfway through a Kubernetes version change is in a state k0s supports --
// controllers ahead of workers, by one minor -- whereas one that carried on
// past a controller which did not come back would be evicting workloads onto a
// control plane that is already short a member.
func (r *KubernetesRollout) Run(ctx context.Context) error {
	if r.Timeout == 0 {
		r.Timeout = DefaultKubernetesTimeout
	}

	stages := []rolloutStage{
		{"controller", r.Controllers},
		{"worker", r.Workers},
	}

	total := len(r.Controllers) + len(r.Workers)
	if total == 0 {
		return errors.New("no nodes to move")
	}

	clients, err := r.preflight(ctx, stages, total)
	if err != nil {
		return err
	}

	if r.DryRun {
		r.say("\nDry run: every node accepts k0s %s. Nothing was changed.\n", r.Version)

		return nil
	}

	r.say("\n")

	for i := range r.Results {
		result := &r.Results[i]

		r.say("[%d/%d] %s (%s)\n", i+1, total, result.Address, result.Stage)

		if result.Result == ResultSkipped {
			r.say("        already on k0s %s, skipped\n", r.Version)

			continue
		}

		if err := r.moveOne(ctx, clients[result.Address]); err != nil {
			r.say("        failed: %v\n", err)

			result.Result, result.Error = ResultFailed, err.Error()
			for j := i + 1; j < len(r.Results); j++ {
				if r.Results[j].Result == ResultChecked {
					r.Results[j].Result = ResultPending
				}
			}

			return fmt.Errorf(
				"%s: %w. %d of %d nodes are on %s; the rest are untouched. "+
					"A cluster part-way through this is a state k0s supports, "+
					"so there is no hurry to finish it",
				result.Address, err, i, total, r.Version)
		}

		result.Result = ResultMoved
	}

	r.say("\nAll %d nodes are running k0s %s.\n", total, r.Version)

	return nil
}

// preflight asks every node whether it can be moved before the first one is,
// so a bad address or a refused version cannot leave the cluster half-way.
func (r *KubernetesRollout) preflight(
	ctx context.Context, stages []rolloutStage, total int,
) (map[string]*Client, error) {
	r.say("Checking %d nodes can move to k0s %s\n", total, r.Version)

	clients := make(map[string]*Client, total)
	cluster := ""
	refused := 0
	r.Results = make([]KubernetesNodeResult, 0, total)

	for _, stage := range stages {
		for _, address := range stage.nodes {
			result := KubernetesNodeResult{
				Address: address, Stage: stage.name, To: r.Version, Result: ResultChecked,
			}

			if _, seen := clients[address]; seen {
				r.say("  %s (%s): listed twice\n", address, stage.name)
				refused++

				result.Result, result.Error = ResultRefused, "listed twice"
				r.Results = append(r.Results, result)

				continue
			}

			client, running, err := r.checkOne(ctx, address, stage.name, &cluster)
			clients[address] = client
			result.From = running

			switch {
			case errors.Is(err, ErrUnknownRoute):
				r.say("  %s (%s): on k0s %s; predates the check, it will check when moved\n",
					address, stage.name, displayVersion(running))

				if running == r.Version {
					result.Result = ResultSkipped
				}
			case err != nil:
				r.say("  %s (%s): %v\n", address, stage.name, err)
				refused++

				result.Result, result.Error = ResultRefused, err.Error()
			case running == r.Version:
				r.say("  %s (%s): already on k0s %s\n", address, stage.name, running)

				result.Result = ResultSkipped
			default:
				r.say("  %s (%s): on k0s %s\n", address, stage.name, running)
			}

			r.Results = append(r.Results, result)
		}
	}

	mixed := mixedMinors(r.Results)
	if mixed != "" {
		r.say("  %s\n", mixed)
	}

	if refused > 0 {
		return nil, fmt.Errorf("%d of %d nodes cannot move to %s; nothing was changed",
			refused, total, r.Version)
	}

	if mixed != "" {
		return nil, fmt.Errorf("%s; bring them to one minor before moving to %s. Nothing was changed",
			mixed, r.Version)
	}

	return clients, nil
}

// mixedMinors names the nodes still to move when they span several minors,
// which a resumed or overlapping rollout can leave behind. Each node accepting
// the target alone does not make the order safe across them. Empty when they
// agree; an unreported or unparseable version is left to the node's own check.
func mixedMinors(results []KubernetesNodeResult) string {
	var lines []string

	byLine := map[string][]string{}

	for _, result := range results {
		if result.Result != ResultChecked {
			continue
		}

		version, err := k0s.ParseVersion(result.From)
		if err != nil {
			continue
		}

		line := version.MinorLine()
		if _, seen := byLine[line]; !seen {
			lines = append(lines, line)
		}

		byLine[line] = append(byLine[line], result.Address)
	}

	if len(lines) < 2 {
		return ""
	}

	groups := make([]string, 0, len(lines))
	for _, line := range lines {
		groups = append(groups, fmt.Sprintf("%s on %s", strings.Join(byLine[line], ", "), line))
	}

	return "the nodes still to move are on different minors: " + strings.Join(groups, "; ")
}

// checkOne reads a node and asks it to validate the version. cluster carries
// the first cluster name seen, which every other node must share.
func (r *KubernetesRollout) checkOne(
	ctx context.Context, address, stage string, cluster *string,
) (*Client, string, error) {
	client, err := r.Connect(address)
	if err != nil {
		return nil, "", err
	}

	node, err := client.Node(ctx)
	if err != nil {
		return client, "", err
	}

	if err := healthy(node); err != nil {
		return client, "", err
	}

	if err := roleFits(node.Role, stage); err != nil {
		return client, "", err
	}

	if node.Cluster != "" {
		if *cluster == "" {
			*cluster = node.Cluster
		} else if node.Cluster != *cluster {
			return client, "", fmt.Errorf("in cluster %q, the others in %q", node.Cluster, *cluster)
		}
	}

	running, err := client.KubernetesCheck(ctx, r.Version, r.Mirror)
	if errors.Is(err, ErrUnknownRoute) {
		return client, node.Kubernetes.Version, err
	}

	return client, running, err
}

// roleFits refuses a node listed in the wrong stage, which would break the
// controllers-first order. An unreported role is left to the node.
func roleFits(role, stage string) error {
	if role == "" {
		return nil
	}

	isWorker := role == "worker"
	if isWorker != (stage == "worker") {
		return fmt.Errorf("is a %s, listed as a %s", role, stage)
	}

	return nil
}

func (r *KubernetesRollout) moveOne(ctx context.Context, client *Client) error {
	// Before anything: is this node in a state where taking it out of service
	// is safe? A node that is already unhealthy is one whose workloads have
	// nowhere good to go, and whose control plane can least afford to lose a
	// member.
	before, err := client.Node(ctx)
	if err != nil {
		return err
	}

	if err := healthy(before); err != nil {
		return err
	}

	r.say("        on k0s %s, moving to %s\n",
		displayVersion(before.Kubernetes.Version), r.Version)

	// One node's deadline, not the whole rollout's: a slow first node should
	// not eat the budget of the ones behind it.
	attempt, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	running, err := client.KubernetesVersion(attempt, r.Version, r.Mirror)
	if err != nil {
		return err
	}

	// The node already refuses to report success on a version it is not
	// running, so this is a second pair of eyes rather than the only check.
	// It costs nothing and it is the assertion a rollout exists to make.
	if running != r.Version {
		return fmt.Errorf("reports %s after being moved to %s", running, r.Version)
	}

	// Health again, because the point of going one at a time is to find out
	// that a node did not come back *before* starting the next one.
	after, err := client.Node(ctx)
	if err != nil {
		return err
	}

	if err := healthy(after); err != nil {
		return err
	}

	r.say("        up on k0s %s\n", running)

	return nil
}

// displayVersion is a node's reported version for a progress line. A node too
// old to report it is not an error worth stopping for: the change is about to
// say what it moved from anyway.
func displayVersion(version string) string {
	if version == "" {
		return "an unreported version"
	}

	return version
}

func (r *KubernetesRollout) say(format string, args ...any) {
	if r.Out == nil {
		return
	}

	// Progress on a terminal: a write that fails has nowhere better to be
	// reported, and losing a progress line must not fail a rollout.
	_, _ = fmt.Fprintf(r.Out, format, args...)
}
