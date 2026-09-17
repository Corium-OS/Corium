package cctl

import (
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

func render(t *testing.T, node *nodeinfo.Node) string {
	t.Helper()

	var out strings.Builder
	FormatNode(&out, "192.168.1.51:7443", node)

	return out.String()
}

func TestFormatNodeShowsWhatMattersInAnIncident(t *testing.T) {
	report := render(t, &nodeinfo.Node{
		Hostname:     "worker-01",
		MachineID:    "6b3a1c7e",
		Bootstrapped: true,
		Role:         "worker",
		Cluster:      "prod",
		OS: nodeinfo.OS{
			Name:   "Fedora Linux 44",
			Kernel: "6.14.3",
			Booted: &nodeinfo.Deployment{
				Image:  "ghcr.io/corium-os/corium:0.1",
				Digest: "sha256:aaaa",
			},
		},
		Kubernetes: nodeinfo.Kubernetes{
			Version: "v1.31.2+k0s.0",
			Service: "k0sworker.service",
			Active:  true,
		},
		Health: nodeinfo.Health{Greenboot: "passed", UptimeSeconds: 3600},
	})

	// The digest above all: a tag says what was asked for, a digest says what
	// booted, and that is the difference an incident turns on.
	for _, want := range []string{
		"worker-01", "worker", "prod", "sha256:aaaa",
		"ghcr.io/corium-os/corium:0.1", "v1.31.2+k0s.0", "running", "passed", "1h0m0s",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not mention %q:\n%s", want, report)
		}
	}
}

func TestFormatNodeSaysWhenAnUpgradeIsWaiting(t *testing.T) {
	// A staged image means the next reboot moves this node, which an operator
	// about to reboot it needs to be told rather than left to infer.
	report := render(t, &nodeinfo.Node{
		Bootstrapped: true,
		Role:         "worker",
		OS: nodeinfo.OS{
			Booted: &nodeinfo.Deployment{Image: "corium:0.1", Digest: "sha256:aaaa"},
			Staged: &nodeinfo.Deployment{Image: "corium:0.2", Digest: "sha256:bbbb"},
		},
	})

	for _, want := range []string{"staged", "sha256:bbbb", "next reboot"} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not mention %q:\n%s", want, report)
		}
	}
}

func TestFormatNodeLeavesUnknownFieldsOutRatherThanGuessing(t *testing.T) {
	// A blank is honest where a dash or a zero invites somebody to read it as
	// a value -- and this is read just before something irreversible.
	report := render(t, &nodeinfo.Node{
		Hostname:     "worker-01",
		Bootstrapped: true,
		Role:         "worker",
	})

	for _, absent := range []string{"kernel", "greenboot", "uptime", "digest", "k0s "} {
		if strings.Contains(report, absent) {
			t.Errorf("report shows %q for a field it does not know:\n%s", absent, report)
		}
	}
}

func TestFormatNodeIsPlainAboutAMachineThatIsNotANode(t *testing.T) {
	report := render(t, &nodeinfo.Node{Hostname: "some-host"})

	if !strings.Contains(report, "not a fault") {
		t.Errorf("report reads as a failure rather than a valid outcome:\n%s", report)
	}

	if strings.Contains(report, "role") {
		t.Errorf("report claims a role for a machine that has none:\n%s", report)
	}
}
