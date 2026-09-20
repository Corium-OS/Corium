package issue

import (
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

func TestRenderBootstrapped(t *testing.T) {
	node := &nodeinfo.Node{
		Bootstrapped: true,
		Role:         "worker",
		Cluster:      "prod-eu",
		Endpoint:     "https://10.0.0.1:6443",
		Kubernetes:   nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0", Service: "k0sworker", Active: true},
		Health:       nodeinfo.Health{Greenboot: "passed"},
		OS: nodeinfo.OS{
			Booted: &nodeinfo.Deployment{Version: "0.2.0", Digest: "sha256:1a2b3c4d5e6f7a8b9c0d"},
		},
	}

	got := Render(node)

	for _, want := range []string{
		"role",
		"worker",
		"cluster",
		"prod-eu",
		"https://10.0.0.1:6443",
		"v1.36.4+k0s.0 (running)",
		"passed",
		"0.2.0  sha256:1a2b3c4d5e6f",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing %q\n%s", want, got)
		}
	}
}

func TestRenderStagedImage(t *testing.T) {
	node := &nodeinfo.Node{
		Bootstrapped: true,
		Role:         "controller",
		OS: nodeinfo.OS{
			Booted: &nodeinfo.Deployment{Version: "0.2.0", Digest: "sha256:1111111111112222"},
			Staged: &nodeinfo.Deployment{Version: "0.3.0", Digest: "sha256:9f8e7d6c5b4a3333"},
		},
	}

	got := Render(node)

	if !strings.Contains(got, "0.3.0  sha256:9f8e7d6c5b4a  (next reboot)") {
		t.Errorf("staged image not rendered as expected:\n%s", got)
	}
}

func TestRenderNotBootstrapped(t *testing.T) {
	got := Render(&nodeinfo.Node{Bootstrapped: false, Role: "worker"})

	if !strings.Contains(got, "not part of a cluster") {
		t.Errorf("banner should say the node is not in a cluster:\n%s", got)
	}

	// A role recorded on a node that never finished bootstrapping is not the
	// node's state, so it must not be presented as though it were.
	if strings.Contains(got, "role") {
		t.Errorf("banner should not claim a role on an unbootstrapped node:\n%s", got)
	}
}

// TestRenderAddressEscape guards the one escape the banner relies on: \4 must
// reach the file as a single backslash so agetty expands it, and appear exactly
// once so a stray backslash in the wordmark has not been mistaken for it.
func TestRenderAddressEscape(t *testing.T) {
	got := Render(&nodeinfo.Node{Bootstrapped: true, Role: "single"})

	if n := strings.Count(got, `\4`); n != 1 {
		t.Errorf("want exactly one raw \\4 escape, got %d\n%s", n, got)
	}
}

// TestRenderWordmarkEscaped guards the other side of it: every backslash in the
// wordmark must be doubled, or agetty eats the character after it and the
// drawing falls apart. The bottom row of the "C" is the canary.
func TestRenderWordmarkEscaped(t *testing.T) {
	got := Render(&nodeinfo.Node{})

	if !strings.Contains(got, `\\____`) {
		t.Errorf("wordmark backslashes are not doubled for agetty:\n%s", got)
	}

	// And nowhere may a lone backslash be followed by a letter agetty would
	// read as an escape (\d for date, \l for the tty, and so on). The only
	// intentional escape is \4, checked above.
	for i := 0; i < len(got)-1; i++ {
		if got[i] != '\\' {
			continue
		}

		next := got[i+1]
		if next == '\\' {
			i++ // a doubled backslash: skip the pair
			continue
		}

		if next != '4' {
			t.Errorf("unescaped agetty sequence \\%c at offset %d\n%s", next, i, got)
		}
	}
}

func TestShortDigest(t *testing.T) {
	tests := map[string]string{
		"sha256:1a2b3c4d5e6f7a8b9c0d": "sha256:1a2b3c4d5e6f",
		"sha256:short":                "sha256:short",
		"":                            "",
		"not-a-digest":                "not-a-digest",
	}

	for in, want := range tests {
		if got := shortDigest(in); got != want {
			t.Errorf("shortDigest(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRenderOmitsVolatileFields guards the reprint fix. The banner must not
// carry a field that changes on every render -- uptime was one -- because the
// timer reloads agetty only when the banner changes, and agetty reprints the
// whole prompt rather than redrawing it in place. A volatile field would fire
// that reload on every tick and march a fresh banner down the console forever.
// A long-running node must render byte-identically to one just booted.
func TestRenderOmitsVolatileFields(t *testing.T) {
	fresh := &nodeinfo.Node{
		Bootstrapped: true,
		Role:         "single",
		Health:       nodeinfo.Health{Greenboot: "passed"},
	}

	aged := *fresh
	aged.Health.UptimeSeconds = 987654

	if Render(fresh) != Render(&aged) {
		t.Errorf("banner changes with uptime; it must not\n--- fresh ---\n%s--- aged ---\n%s",
			Render(fresh), Render(&aged))
	}

	if strings.Contains(Render(&aged), "uptime") {
		t.Errorf("banner still shows a volatile uptime field:\n%s", Render(&aged))
	}
}

func TestK0sState(t *testing.T) {
	tests := []struct {
		name string
		k    nodeinfo.Kubernetes
		want string
	}{
		{"running with version", nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0", Active: true}, "v1.36.4+k0s.0 (running)"},
		{"stopped with version", nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0"}, "v1.36.4+k0s.0 (not running)"},
		{"no version", nodeinfo.Kubernetes{Active: true}, "running"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := k0sState(tt.k); got != tt.want {
				t.Errorf("k0sState() = %q, want %q", got, tt.want)
			}
		})
	}
}
