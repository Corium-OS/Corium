package cctl

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/etcd"
)

// threeMembers is the membership every healthy controller in these tests sees.
var threeMembers = []etcd.Member{
	{Name: "cp-1", PeerURL: "https://10.0.0.1:2380"},
	{Name: "cp-2", PeerURL: "https://10.0.0.2:2380"},
	{Name: "cp-3", PeerURL: "https://10.0.0.3:2380"},
}

func healthyMember(name string) *etcd.Status {
	return &etcd.Status{Storage: etcd.StorageEtcd, Member: name, Healthy: true, Members: threeMembers}
}

func sickMember(name string) *etcd.Status {
	return &etcd.Status{Storage: etcd.StorageEtcd, Member: name, Reason: "no leader"}
}

func TestJudgeEtcd(t *testing.T) {
	t.Parallel()

	old := etcdAnswer{address: "c3", err: ErrUnknownRoute}

	tests := []struct {
		name    string
		answers []etcdAnswer
		wantErr string
		healthy int
		notes   int
	}{
		{
			name: "every member vouched for",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", status: healthyMember("cp-2")},
				{address: "c3", status: healthyMember("cp-3")},
			},
			healthy: 3,
		},
		{
			name: "an unhealthy member",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", status: sickMember("cp-2")},
				{address: "c3", status: healthyMember("cp-3")},
			},
			wantErr: `c2: member "cp-2" is unhealthy: no leader`,
		},
		{
			name: "a member on no controller given",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", status: healthyMember("cp-2")},
			},
			wantErr: `member "cp-3" is on none of the controllers given`,
		},
		{
			name: "an unreachable controller",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", err: errors.New("connection refused")},
				{address: "c3", status: healthyMember("cp-3")},
			},
			wantErr: "c2: connection refused",
		},
		{
			name: "controllers that disagree on membership",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", status: &etcd.Status{
					Storage: etcd.StorageEtcd, Member: "cp-2", Healthy: true, Members: threeMembers[:2],
				}},
				{address: "c3", status: healthyMember("cp-3")},
			},
			wantErr: "c1 sees members [cp-1 cp-2 cp-3], c2 sees [cp-1 cp-2]",
		},
		{
			name: "a node reporting a member etcd does not list",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-9")},
			},
			wantErr: `c1 reports member "cp-9", which etcd does not list`,
		},
		{
			name: "an old controller is noted, not blocking",
			answers: []etcdAnswer{
				{address: "c1", status: healthyMember("cp-1")},
				{address: "c2", status: healthyMember("cp-2")},
				old,
			},
			healthy: 2,
			notes:   2,
		},
		{
			name:    "kine has nothing to check",
			answers: []etcdAnswer{{address: "c1", status: &etcd.Status{Storage: etcd.StorageKine}}},
			notes:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			verdict, err := judgeEtcd(test.answers)

			if test.wantErr != "" {
				if !errors.Is(err, ErrEtcdNotWhole) || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("judgeEtcd() = %v, want %q", err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("judgeEtcd() = %v", err)
			}

			if verdict.healthy != test.healthy || len(verdict.notes) != test.notes {
				t.Errorf("verdict = %+v, want %d healthy and %d notes", verdict, test.healthy, test.notes)
			}
		})
	}
}

func etcdFleet(t *testing.T) *k8sFleet {
	t.Helper()

	return &k8sFleet{t: t, nodes: map[string]*k8sNode{
		"c1:7443": {role: "controller", version: "v1.35.8+k0s.1", etcd: healthyMember("cp-1")},
		"c2:7443": {role: "controller", version: "v1.35.8+k0s.1", etcd: healthyMember("cp-2")},
		"c3:7443": {role: "controller", version: "v1.35.8+k0s.1", etcd: healthyMember("cp-3")},
		"w1:7443": {role: "worker", version: "v1.35.8+k0s.1"},
	}}
}

func etcdRollout(fleet *k8sFleet, out *strings.Builder) *KubernetesRollout {
	return &KubernetesRollout{
		Version:     "v1.36.4+k0s.0",
		Controllers: []string{"c1:7443", "c2:7443", "c3:7443"},
		Workers:     []string{"w1:7443"},
		Connect:     fleet.connect,
		Out:         out,
		Settle:      20 * time.Millisecond,
		Poll:        time.Millisecond,
	}
}

func TestKubernetesRolloutChecksEtcdAroundEachController(t *testing.T) {
	t.Parallel()

	fleet := etcdFleet(t)

	var out strings.Builder

	if err := etcdRollout(fleet, &out).Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}

	if got := strings.Count(out.String(), "3 of 3 members healthy"); got != 6 {
		t.Errorf("etcd checked %d times, want before and after each controller (6)\n%s", got, out.String())
	}
}

func TestKubernetesRolloutWillNotTakeAControllerFromASickEtcd(t *testing.T) {
	t.Parallel()

	fleet := etcdFleet(t)
	fleet.nodes["c2:7443"].etcd = sickMember("cp-2")

	var out strings.Builder

	err := etcdRollout(fleet, &out).Run(t.Context())
	if !errors.Is(err, ErrEtcdNotWhole) {
		t.Fatalf("Run() = %v, want ErrEtcdNotWhole\n%s", err, out.String())
	}

	for address, node := range fleet.nodes {
		if node.moved != 0 {
			t.Errorf("%s was moved with etcd already short a member", address)
		}
	}
}

func TestKubernetesRolloutStopsWhenAMovedControllerLeavesEtcdSick(t *testing.T) {
	t.Parallel()

	fleet := etcdFleet(t)
	fleet.nodes["c1:7443"].etcdAfterMove = sickMember("cp-1")

	var out strings.Builder

	rollout := etcdRollout(fleet, &out)

	err := rollout.Run(t.Context())
	if !errors.Is(err, ErrEtcdNotWhole) {
		t.Fatalf("Run() = %v, want ErrEtcdNotWhole\n%s", err, out.String())
	}

	if fleet.nodes["c2:7443"].moved != 0 {
		t.Error("the rollout took a second controller while the first had not rejoined etcd")
	}

	// It did change version, so the report must not call it failed.
	if first := rollout.Report().Nodes[0]; first.Result != ResultMoved || first.Error == "" {
		t.Errorf("c1 reported as %+v, want moved with the etcd error", first)
	}
}

func TestKubernetesRolloutNotesAControllerThatPredatesTheEtcdCheck(t *testing.T) {
	t.Parallel()

	fleet := etcdFleet(t)
	fleet.nodes["c3:7443"].etcd = nil

	var out strings.Builder

	if err := etcdRollout(fleet, &out).Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}

	if !strings.Contains(out.String(), "c3:7443 predates the etcd check") {
		t.Errorf("the old controller was not reported:\n%s", out.String())
	}
}

func TestKubernetesDryRunChecksEtcd(t *testing.T) {
	t.Parallel()

	fleet := etcdFleet(t)
	fleet.nodes["c3:7443"].etcd = sickMember("cp-3")

	var out strings.Builder

	rollout := etcdRollout(fleet, &out)
	rollout.DryRun = true

	if err := rollout.Run(t.Context()); !errors.Is(err, ErrEtcdNotWhole) {
		t.Fatalf("Run() = %v, want ErrEtcdNotWhole\n%s", err, out.String())
	}
}

func TestOSRolloutWillNotRebootAControllerFromASickEtcd(t *testing.T) {
	t.Parallel()

	sick := sickMember("cp-1")
	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"c1:7443": {digest: "sha256:old", active: true, role: "controller", etcd: sick},
		"w1:7443": {digest: "sha256:old", active: true},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"w1:7443", "c1:7443"},
		Connect: fleet.connect,
		Poll:    time.Millisecond,
		Settle:  time.Second,
		Out:     &out,
	}

	err := rollout.Run(t.Context())
	if !errors.Is(err, ErrEtcdNotWhole) {
		t.Fatalf("Run() = %v, want ErrEtcdNotWhole\n%s", err, out.String())
	}

	if fleet.nodes["c1:7443"].applied != 0 {
		t.Error("a controller was rebooted with its etcd member unhealthy")
	}

	if fleet.nodes["w1:7443"].applied != 1 {
		t.Error("a worker was held back by etcd, which it has no part in")
	}
}

func TestOSDryRunChecksEtcd(t *testing.T) {
	t.Parallel()

	fleet := &fleet{t: t, nodes: map[string]*fakeNode{
		"c1:7443": {digest: "sha256:old", role: "controller", etcd: sickMember("cp-1")},
	}}

	var out strings.Builder

	rollout := &Rollout{
		Image:   "ghcr.io/corium-os/corium:0.2",
		Nodes:   []string{"c1:7443"},
		Connect: fleet.connect,
		DryRun:  true,
		Out:     &out,
	}

	if err := rollout.Run(t.Context()); !errors.Is(err, ErrEtcdNotWhole) {
		t.Fatalf("Run() = %v, want ErrEtcdNotWhole\n%s", err, out.String())
	}
}
