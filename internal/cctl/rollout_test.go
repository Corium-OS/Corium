package cctl

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// fakeNode stands in for one machine in a rollout, so that a test can make it
// misbehave in ways a real one is expensive to arrange.
type fakeNode struct {
	digest  string
	staged  string
	active  bool
	applied int

	// comesBackAs overrides the digest the node reports after applying, for
	// the case that matters most: a node that rebooted onto something else.
	comesBackAs string

	// uptime is what the node reports, in seconds. A simulated reboot resets it
	// low, which is how waitForReturn tells a real return from a node that only
	// answered on its way down.
	uptime int64

	// stuckUp models a node whose drain never lets it reboot: Apply is accepted
	// but the node keeps answering on its old image at its old uptime.
	stuckUp bool

	// failStage and failApply make the node refuse.
	failStage, failApply error

	// unhealthy makes it report a stopped k0s.
	unhealthy bool
}

func (f *fakeNode) node() *nodeinfo.Node {
	return &nodeinfo.Node{
		Bootstrapped: true,
		Role:         "worker",
		OS:           nodeinfo.OS{Booted: &nodeinfo.Deployment{Digest: f.digest}},
		Kubernetes: nodeinfo.Kubernetes{
			Service: "k0sworker.service",
			Active:  !f.unhealthy,
		},
		Health: nodeinfo.Health{UptimeSeconds: f.uptime},
	}
}

// fleet serves a set of fake nodes through the Rollout's Connect hook.
type fleet struct {
	nodes map[string]*fakeNode
	t     *testing.T
}

func (f *fleet) connect(address string) (*Client, error) {
	node, ok := f.nodes[address]
	if !ok {
		return nil, errors.New("no such node")
	}

	return newTestClient(f.t, node), nil
}

func TestRolloutUpgradesEveryNodeInTurn(t *testing.T) {
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", active: true},
		"b:7443": {digest: "sha256:old", active: true},
		"c:7443": {digest: "sha256:old", active: true},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"a:7443", "b:7443", "c:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  time.Second,
		Out:     &out,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}

	for address, node := range fleet.nodes {
		if node.applied != 1 {
			t.Errorf("%s applied %d times, want 1", address, node.applied)
		}
	}
}

func TestRolloutStopsAtTheFirstNodeThatDoesNotComeBack(t *testing.T) {
	// The property that makes one-at-a-time worth doing. Carrying on past a
	// node that did not return turns one broken machine into a broken cluster.
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", active: true},
		"b:7443": {digest: "sha256:old", active: true, comesBackAs: "sha256:something-else"},
		"c:7443": {digest: "sha256:old", active: true},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"a:7443", "b:7443", "c:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  50 * time.Millisecond,
		Out:     &out,
	}

	err := rollout.Run(t.Context())
	if err == nil {
		t.Fatalf("Run() = nil, want a failure\n%s", out.String())
	}

	if fleet.nodes["c:7443"].applied != 0 {
		t.Error("the rollout carried on past a node that came back wrong")
	}

	// And it says how far it got, because the operator has to decide what to
	// do with a half-upgraded cluster.
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("error = %q, want it to say how many were upgraded", err)
	}
}

func TestRolloutWaitsForARealRebootNotJustAReply(t *testing.T) {
	// The bug this guards: Apply starts the drain-and-reboot asynchronously, so
	// the node keeps answering for a while. A node whose drain never lets it
	// reboot answers throughout -- and must not be read as "came back" on its
	// pre-reboot state. It is a timeout that names the reason, not a success.
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", active: true, uptime: 68_400, stuckUp: true},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"a:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  40 * time.Millisecond,
		Out:     &out,
	}

	err := rollout.Run(t.Context())
	if err == nil {
		t.Fatalf("Run() = nil, want a failure for a node that never rebooted\n%s", out.String())
	}

	if !strings.Contains(err.Error(), "did not reboot") {
		t.Errorf("error = %q, want it to say the node did not reboot", err)
	}

	// It was told to apply -- the staging and the reboot request happened; what
	// did not happen is the reboot itself, and the check caught that.
	if fleet.nodes["a:7443"].applied != 1 {
		t.Errorf("applied %d times, want 1", fleet.nodes["a:7443"].applied)
	}
}

func TestRolloutAcceptsAReturnOnceUptimeDrops(t *testing.T) {
	// The other side of the gate: a node that really rebooted reports an uptime
	// below what it had before, and is accepted on the image it staged.
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", active: true, uptime: 68_400},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"a:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  time.Second,
		Out:     &out,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}

	if fleet.nodes["a:7443"].applied != 1 {
		t.Errorf("applied %d times, want 1", fleet.nodes["a:7443"].applied)
	}
}

func TestRolloutRefusesToTakeDownAnAlreadyBrokenNode(t *testing.T) {
	// A node whose k0s is not running has workloads with nowhere good to go.
	// Rebooting it is not what somebody wants at that moment.
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", unhealthy: true},
	}}

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"a:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  50 * time.Millisecond,
		Out:     io.Discard,
	}

	if err := rollout.Run(t.Context()); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("Run() = %v, want %v", err, ErrUnhealthy)
	}

	if fleet.nodes["a:7443"].applied != 0 {
		t.Error("an unhealthy node was rebooted anyway")
	}
}

func TestRolloutStopsWhenANodeRefusesTheImage(t *testing.T) {
	// The signing policy refusing an image is the case from the issue thread:
	// somebody rebasing a cluster onto the wrong thing. It must stop the whole
	// rollout, not just that node.
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"a:7443": {digest: "sha256:old", failStage: errors.New("403 Forbidden: not signed")},
		"b:7443": {digest: "sha256:old"},
	}}

	rollout := &Rollout{
		Image:   "quay.io/fedora-ostree-desktops/silverblue:44",
		Nodes:   []string{"a:7443", "b:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  50 * time.Millisecond,
		Out:     io.Discard,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("Run() = nil, want a refusal")
	}

	if fleet.nodes["b:7443"].applied != 0 {
		t.Error("the second node was touched after the first refused")
	}
}

func TestHealthy(t *testing.T) {
	tests := map[string]struct {
		node *nodeinfo.Node
		ok   bool
	}{
		"a working node": {&nodeinfo.Node{
			Bootstrapped: true,
			Kubernetes:   nodeinfo.Kubernetes{Service: "k0sworker.service", Active: true},
		}, true},
		"never bootstrapped": {&nodeinfo.Node{}, false},
		"k0s stopped": {&nodeinfo.Node{
			Bootstrapped: true,
			Kubernetes:   nodeinfo.Kubernetes{Service: "k0sworker.service"},
		}, false},
		"greenboot said no": {&nodeinfo.Node{
			Bootstrapped: true,
			Kubernetes:   nodeinfo.Kubernetes{Service: "k0sworker.service", Active: true},
			Health:       nodeinfo.Health{Greenboot: "failed"},
		}, false},
	}

	for name, tc := range tests {
		if got := healthy(tc.node) == nil; got != tc.ok {
			t.Errorf("%s: healthy() ok = %v, want %v", name, got, tc.ok)
		}
	}
}

// newTestClient serves one fake node over TLS and returns a client pinned to
// it, so the rollout exercises the real client and the real wire format rather
// than a stand-in for them.
func newTestClient(t *testing.T, node *fakeNode) *Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(node.node())
	})

	mux.HandleFunc("POST /v1/upgrade/stage", func(w http.ResponseWriter, _ *http.Request) {
		if node.failStage != nil {
			// A refusal before the pull begins still arrives as a status code,
			// as it does on a real node.
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": node.failStage.Error()})

			return
		}

		node.staged = "sha256:new"

		// The wire format the real node uses: a stream of progress lines, then
		// a single terminal record with what is now staged.
		w.Header().Set("Content-Type", "application/x-ndjson")

		encoder := json.NewEncoder(w)
		_ = encoder.Encode(map[string]any{"progress": "pulling " + node.staged})
		_ = encoder.Encode(map[string]any{"staged": map[string]string{"digest": node.staged}})
	})

	mux.HandleFunc("POST /v1/upgrade/apply", func(w http.ResponseWriter, _ *http.Request) {
		if node.failApply != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": node.failApply.Error()})

			return
		}

		node.applied++

		if !node.stuckUp {
			// The reboot, compressed: the node comes back on what it staged, or
			// on whatever the test said it would come back as, and its uptime
			// resets the way a real boot's would.
			node.digest = node.staged
			if node.comesBackAs != "" {
				node.digest = node.comesBackAs
			}

			node.uptime = 30
		}

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "applying"})
	})

	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	address := strings.TrimPrefix(server.URL, "https://")

	return Dial(address, api.Fingerprint(server.Certificate().Raw))
}
