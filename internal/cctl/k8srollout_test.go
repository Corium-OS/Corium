package cctl

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/etcd"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// controllerBackup is the snapshot a fake controller reports taking.
const controllerBackup = "/var/lib/corium/backups/corium-backup-20261101T120000Z.tar.gz"

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

	// checks serves POST /v1/kubernetes/check; without it the node predates
	// the route.
	checks bool

	// refuseCheck makes the check reject the version.
	refuseCheck error

	cluster string

	// etcd serves GET /v1/etcd; nil means the node predates the route.
	// etcdAfterMove replaces it once the node is moved.
	etcd, etcdAfterMove *etcd.Status
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
			Cluster:      node.cluster,
			Kubernetes: nodeinfo.Kubernetes{
				Version: node.version,
				Service: node.service(),
				Active:  !node.unhealthy,
			},
		})
	})

	if node.etcd != nil {
		mux.HandleFunc("GET /v1/etcd", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(node.etcd)
		})
	}

	if node.checks {
		mux.HandleFunc("POST /v1/kubernetes/check", func(w http.ResponseWriter, _ *http.Request) {
			if node.refuseCheck != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": node.refuseCheck.Error()})

				return
			}

			_ = json.NewEncoder(w).Encode(map[string]string{"kubernetes": node.version})
		})
	}

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

		if node.etcdAfterMove != nil {
			node.etcd = node.etcdAfterMove
		}

		if node.comesBackAs != "" {
			node.version = node.comesBackAs
		}

		reply := map[string]string{
			"status":     "changed",
			"kubernetes": node.version,
		}

		if node.role == "controller" {
			reply["backup"] = controllerBackup
		}

		_ = json.NewEncoder(w).Encode(reply)
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

	// Only controllers hold a datastore, so only they report a snapshot.
	if got := strings.Count(out.String(), "snapshot "+controllerBackup); got != 2 {
		t.Errorf("printed %d snapshot paths, want one per controller:\n%s", got, out.String())
	}

	for _, node := range rollout.Report().Nodes {
		want := ""
		if node.Stage == "controller" {
			want = controllerBackup
		}

		if node.Backup != want {
			t.Errorf("report for %s has backup %q, want %q", node.Address, node.Backup, want)
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

// TestPreflightStopsTheRolloutBeforeAnyNodeMoves covers every refusal the
// preflight owns: each one must leave every node untouched, including the
// controllers listed before the node at fault.
func TestPreflightStopsTheRolloutBeforeAnyNodeMoves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		worker  k8sNode
		workers []string
	}{
		{
			name: "a worker refuses the version",
			worker: k8sNode{role: "worker", checks: true,
				refuseCheck: errors.New("k0s does not support skipping a minor")},
			workers: []string{"w1:7443"},
		},
		{
			name:    "a controller listed as a worker",
			worker:  k8sNode{role: "controller", checks: true},
			workers: []string{"w1:7443"},
		},
		{
			name:    "a node from another cluster",
			worker:  k8sNode{role: "worker", checks: true, cluster: "elsewhere"},
			workers: []string{"w1:7443"},
		},
		{
			name:    "an address listed twice",
			worker:  k8sNode{role: "worker", checks: true},
			workers: []string{"w1:7443", "w1:7443"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			worker := test.worker
			worker.version = "v1.35.8+k0s.1"

			if worker.cluster == "" {
				worker.cluster = "lab"
			}

			fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
				"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true, cluster: "lab"},
				"w1:7443": &worker,
			}}

			rollout := &KubernetesRollout{
				Version:     "v1.36.4+k0s.0",
				Controllers: []string{"c1:7443"},
				Workers:     test.workers,
				Connect:     fleet.connect,
			}

			if err := rollout.Run(t.Context()); err == nil {
				t.Fatal("the rollout went ahead")
			}

			for address, node := range fleet.nodes {
				if node.moved != 0 {
					t.Errorf("%s moved %d times, want 0", address, node.moved)
				}
			}
		})
	}
}

func TestDryRunMovesNothing(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
		DryRun:      true,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for address, node := range fleet.nodes {
		if node.moved != 0 {
			t.Errorf("%s moved %d times, want 0", address, node.moved)
		}
	}

	for _, result := range rollout.Report().Nodes {
		if result.Result != ResultChecked {
			t.Errorf("%s: result %q, want %q", result.Address, result.Result, ResultChecked)
		}
	}
}

func TestRolloutResultsRecordEveryNode(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
		"c2:7443": {
			role: "controller", version: "v1.35.8+k0s.1", checks: true,
			refuse: errors.New("drain refused"),
		},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443", "c2:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("the rollout carried on past a node that failed")
	}

	got := rollout.Report().Nodes
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(got), got)
	}

	if !strings.Contains(got[1].Error, "drain refused") {
		t.Errorf("failed node error = %q, want the node's reason", got[1].Error)
	}

	got[1].Error = ""

	from, to := "v1.35.8+k0s.1", "v1.36.4+k0s.0"
	want := []KubernetesNodeResult{
		{Address: "c1:7443", Stage: "controller", From: from, To: to, Result: ResultMoved,
			Backup: controllerBackup},
		{Address: "c2:7443", Stage: "controller", From: from, To: to, Result: ResultFailed},
		{Address: "w1:7443", Stage: "worker", From: from, To: to, Result: ResultPending},
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRolloutResultsOnAPreflightRefusal(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
		"w1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
	}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("the rollout went ahead")
	}

	got := rollout.Report().Nodes
	if len(got) != 2 || got[0].Result != ResultChecked || got[1].Result != ResultRefused {
		t.Fatalf("results = %+v, want c1 checked and w1 refused", got)
	}

	if !strings.Contains(got[1].Error, "listed as a worker") {
		t.Errorf("refusal error = %q, want it to say why", got[1].Error)
	}
}

func TestResumedRolloutSkipsNodesAlreadyOnTheTarget(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.36.4+k0s.0", checks: true},
		"c2:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
		"w1:7443": {role: "worker", version: "v1.35.2+k0s.0", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443", "c2:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := fleet.nodes["c1:7443"].moved; got != 0 {
		t.Errorf("the node already on the target moved %d times, want 0", got)
	}

	want := []string{ResultSkipped, ResultMoved, ResultMoved}
	for i, result := range rollout.Report().Nodes {
		if result.Result != want[i] {
			t.Errorf("%s: result %q, want %q", result.Address, result.Result, want[i])
		}
	}
}

func TestPreflightRefusesNodesToMoveFromDifferentMinors(t *testing.T) {
	t.Parallel()

	// Each node accepts the target alone; together they are not one rollout.
	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", checks: true},
		"w1:7443": {role: "worker", version: "v1.36.2+k0s.0", checks: true},
		"w2:7443": {role: "worker", version: "v1.36.4+k0s.0", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Workers:     []string{"w1:7443", "w2:7443"},
		Connect:     fleet.connect,
	}

	err := rollout.Run(t.Context())
	if err == nil {
		t.Fatal("the rollout went ahead across two minors")
	}

	for _, want := range []string{"c1:7443 on 1.35", "w1:7443 on 1.36"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	if strings.Contains(err.Error(), "w2:7443") {
		t.Errorf("error %q names a node already on the target", err)
	}

	for address, node := range fleet.nodes {
		if node.moved != 0 {
			t.Errorf("%s moved %d times, want 0", address, node.moved)
		}
	}
}

func TestPreflightAcceptsDifferentPatchesOfOneMinor(t *testing.T) {
	t.Parallel()

	fleet := &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.36.3+k0s.0", checks: true},
		"w1:7443": {role: "worker", version: "v1.36.2+k0s.1", checks: true},
	}}

	rollout := &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
		DryRun:      true,
	}

	if err := rollout.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRolloutReportEncodesNoNodesAsAnEmptyList(t *testing.T) {
	t.Parallel()

	rollout := &KubernetesRollout{Version: "v1.36.4+k0s.0"}

	if err := rollout.Run(t.Context()); err == nil {
		t.Fatal("a rollout with no nodes was accepted")
	}

	encoded, err := json.Marshal(rollout.Report())
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"version":"v1.36.4+k0s.0","dryRun":false,"nodes":[]}`; string(encoded) != want {
		t.Errorf("report = %s, want %s", encoded, want)
	}
}
