package cctl

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// k8sNode stands in for one machine in a Kubernetes rollout.
type k8sNode struct {
	role    string
	version string

	// moved counts the version changes this node accepted, which is how a test
	// asserts that a stopped rollout really stopped.
	moved int

	// comesBackAs overrides the version reported afterwards, for the case worth
	// guarding: a node that says it moved and did not.
	comesBackAs string

	// refuse makes the node reject the change, as one outside the window or
	// breaking the skew rules would.
	refuse error

	// unhealthy makes it report a stopped k0s.
	unhealthy bool
}

func (n *k8sNode) service() string {
	if n.role == "controller" {
		return "k0scontroller.service"
	}

	return "k0sworker.service"
}

func newK8sClient(t *testing.T, node *k8sNode) *Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&nodeinfo.Node{
			Bootstrapped: true,
			Role:         node.role,
			Kubernetes: nodeinfo.Kubernetes{
				Version: node.version,
				Service: node.service(),
				Active:  !node.unhealthy,
			},
		})
	})

	mux.HandleFunc("POST /v1/kubernetes", func(w http.ResponseWriter, r *http.Request) {
		if node.refuse != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": node.refuse.Error()})

			return
		}

		var request struct {
			Version string `json:"version"`
		}

		_ = json.NewDecoder(r.Body).Decode(&request)

		node.moved++
		node.version = request.Version

		if node.comesBackAs != "" {
			node.version = node.comesBackAs
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "changed",
			"kubernetes": node.version,
		})
	})

	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	return Dial(strings.TrimPrefix(server.URL, "https://"),
		api.Fingerprint(server.Certificate().Raw))
}

// k8sFleet serves a set of fake nodes through the rollout's Connect hook.
type k8sFleet struct {
	nodes map[string]*k8sNode
	t     *testing.T

	// order records the addresses in the sequence they were moved, which is the
	// property this whole type exists to observe.
	order []string
}

func (f *k8sFleet) connect(address string) (*Client, error) {
	node, ok := f.nodes[address]
	if !ok {
		return nil, errors.New("no such node")
	}

	f.order = append(f.order, address)

	return newK8sClient(f.t, node), nil
}

// TestRolloutUpgradesControllersBeforeWorkers is the test this type exists for.
//
// The order is not a preference: k0s forbids a worker being newer than the
// controllers it talks to, so a rollout that did workers first would create
// exactly the skew k0s refuses, on a running cluster rather than at a gate.
func TestRolloutUpgradesControllersBeforeWorkers(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1"},
		"c2:7443": {role: "controller", version: "v1.35.8+k0s.1"},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1"},
		"w2:7443": {role: "worker", version: "v1.35.8+k0s.1"},
	}}

	var out strings.Builder

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443", "c2:7443"},
		Workers:     []string{"w1:7443", "w2:7443"},
		Connect:     fleet.connect,
		Out:         &out,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}

	want := []string{"c1:7443", "c2:7443", "w1:7443", "w2:7443"}
	if strings.Join(fleet.order, ",") != strings.Join(want, ",") {
		t.Errorf("moved in order %v, want %v", fleet.order, want)
	}

	for address, node := range fleet.nodes {
		if node.moved != 1 {
			t.Errorf("%s moved %d times, want 1", address, node.moved)
		}
	}
}

func TestRolloutStopsAtTheFirstNodeThatRefuses(t *testing.T) {
	t.Parallel()

	// Carrying on past a controller that did not take the change would evict
	// workloads onto a control plane already short a member.
	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1"},
		"c2:7443": {
			role: "controller", version: "v1.35.8+k0s.1",
			refuse: errors.New("outside the window this image supports"),
		},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1"},
	}}

	var out strings.Builder

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443", "c2:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
		Out:         &out,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("the rollout carried on past a node that refused")
	}

	if got := fleet.nodes["w1:7443"].moved; got != 0 {
		t.Errorf("the worker was moved %d times after a controller failed, want 0", got)
	}
}

func TestRolloutRefusesANodeThatDidNotActuallyMove(t *testing.T) {
	t.Parallel()

	// A node reporting success on a version it is not running is the failure
	// the whole design guards against: an extension that did not merge leaves
	// the node on its floor with everything else looking fine.
	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {
			role: "controller", version: "v1.35.8+k0s.1",
			comesBackAs: "v1.35.8+k0s.1",
		},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1"},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("a node that did not move was accepted")
	}

	if got := fleet.nodes["w1:7443"].moved; got != 0 {
		t.Errorf("the worker was moved %d times, want 0", got)
	}
}

func TestRolloutRefusesAnUnhealthyNode(t *testing.T) {
	t.Parallel()

	// A node that is already unhealthy is one whose workloads have nowhere
	// good to go.
	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", unhealthy: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Connect:     fleet.connect,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("an unhealthy node was moved")
	}

	if got := fleet.nodes["c1:7443"].moved; got != 0 {
		t.Errorf("moved %d times, want 0", got)
	}
}

func TestRolloutNeedsNodes(t *testing.T) {
	t.Parallel()

	rollout := &KubernetesRollout{Version: "v1.36.4+k0s.0"}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("a rollout with no nodes was accepted")
	}
}
