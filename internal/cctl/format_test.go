package cctl

import (
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
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

func TestFormatRecordAttributesEveryLine(t *testing.T) {
	// A log read with no unit filter is the common case -- somebody who does
	// not yet know where the problem is -- and unattributed messages are no
	// use to them.
	var out strings.Builder

	FormatRecord(&out, systemd.Record{
		Time:     time.Date(2026, 9, 17, 8, 14, 2, 0, time.UTC),
		Unit:     "k0sworker.service",
		Priority: 4,
		Message:  "node not ready",
	})

	line := out.String()

	for _, want := range []string{"warn", "k0sworker", "node not ready"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q does not mention %q", line, want)
		}
	}

	// The .service suffix is on every unit and so carries no information.
	if strings.Contains(line, ".service") {
		t.Errorf("line %q repeats the suffix every unit has", line)
	}
}

func TestFormatRecordNamesTheKernel(t *testing.T) {
	var out strings.Builder

	FormatRecord(&out, systemd.Record{Priority: 3, Message: "I/O error"})

	if !strings.Contains(out.String(), "kernel") {
		t.Errorf("a record with no unit is not attributed: %q", out.String())
	}
}

func TestFormatRecordSurvivesAnUnknownPriority(t *testing.T) {
	var out strings.Builder

	FormatRecord(&out, systemd.Record{Priority: 42, Message: "hello"})

	if !strings.Contains(out.String(), "42") || !strings.Contains(out.String(), "hello") {
		t.Errorf("line = %q, want the number shown rather than a blank", out.String())
	}
}

func TestFormatNodeShoutsAboutAnUnauthenticatedClaim(t *testing.T) {
	// The thing an operator is least likely to think to ask, and most needs to
	// know: this machine's owner was decided by whoever reached it first.
	report := render(t, &nodeinfo.Node{
		Hostname:     "worker-01",
		Bootstrapped: true,
		Role:         "worker",
		Management: nodeinfo.Management{
			ClaimedBy:       "open",
			Unauthenticated: true,
		},
	})

	if !strings.Contains(report, "without authentication") {
		t.Errorf("report does not say how the node was claimed:\n%s", report)
	}
}

func TestFormatNodeShoutsAboutAnOpenNode(t *testing.T) {
	report := render(t, &nodeinfo.Node{
		Hostname:   "worker-01",
		Management: nodeinfo.Management{OpenEnrolment: true},
	})

	if !strings.Contains(report, "first client to reach it owns it") {
		t.Errorf("report does not warn that the node is open:\n%s", report)
	}
}

func TestFormatNodeStaysQuietWhenTheClaimWasAuthenticated(t *testing.T) {
	report := render(t, &nodeinfo.Node{
		Hostname:     "worker-01",
		Bootstrapped: true,
		Role:         "worker",
		Management:   nodeinfo.Management{ClaimedBy: "pairing-code"},
	})

	if strings.Contains(report, "!!") {
		t.Errorf("a properly claimed node is being warned about:\n%s", report)
	}
}
