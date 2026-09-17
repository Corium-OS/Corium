package nodeinfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeRoot builds the parts of a node's filesystem this package reads.
func fakeRoot(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for path, contents := range files {
		full := filepath.Join(root, path)

		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(full), err)
		}

		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing %s: %v", full, err)
		}
	}

	return root
}

// commands stubs the tools a node ships, keyed by the command name.
func commands(replies map[string]string) Runner {
	return func(_ context.Context, name string, _ ...string) ([]byte, error) {
		reply, ok := replies[name]
		if !ok {
			return nil, errors.New("not installed")
		}

		return []byte(reply), nil
	}
}

// A real bootc reply, trimmed to the fields read here. The schema is upstream's
// and documented as unstable, which is why the parser is permissive and this
// fixture is small.
const bootcReply = `{
  "apiVersion": "org.containers.bootc/v1alpha1",
  "kind": "BootcHost",
  "status": {
    "staged": {
      "image": {
        "image": {"image": "ghcr.io/corium-os/corium:0.2", "transport": "registry"},
        "version": "0.2.0",
        "imageDigest": "sha256:bbbb"
      }
    },
    "booted": {
      "image": {
        "image": {"image": "ghcr.io/corium-os/corium:0.1", "transport": "registry"},
        "version": "0.1.0",
        "imageDigest": "sha256:aaaa"
      }
    }
  }
}`

func TestCollectOnABootstrappedNode(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		"/etc/machine-id":            "6b3a1c7e0f1b4e9a\n",
		"/etc/os-release":            "ID=fedora\nPRETTY_NAME=\"Fedora Linux 44 (Cloud Edition)\"\n",
		"/proc/sys/kernel/osrelease": "6.14.3-300.fc44.x86_64\n",
		"/proc/uptime":               "128450.32 1010203.44\n",
		StateFile:                    `{"role":"controller+worker","cluster":"prod","bootstrappedAt":"2026-09-01T10:00:00Z"}`,
	})

	inspector := &Inspector{Root: root, Run: commands(map[string]string{
		"bootc":     bootcReply,
		"k0s":       "v1.31.2+k0s.0\n",
		"systemctl": "active\n",
	})}

	node := inspector.Collect(t.Context())

	if !node.Bootstrapped {
		t.Error("Bootstrapped = false, want true")
	}

	if node.Role != "controller+worker" || node.Cluster != "prod" {
		t.Errorf("role/cluster = %q/%q, want controller+worker/prod", node.Role, node.Cluster)
	}

	if node.MachineID != "6b3a1c7e0f1b4e9a" {
		t.Errorf("MachineID = %q", node.MachineID)
	}

	if node.OS.Name != "Fedora Linux 44 (Cloud Edition)" {
		t.Errorf("OS.Name = %q", node.OS.Name)
	}

	if node.OS.Kernel != "6.14.3-300.fc44.x86_64" {
		t.Errorf("OS.Kernel = %q", node.OS.Kernel)
	}

	// The digest is the field that matters in an incident: a tag says what was
	// asked for, a digest says what booted.
	if node.OS.Booted == nil || node.OS.Booted.Digest != "sha256:aaaa" {
		t.Errorf("booted = %+v, want digest sha256:aaaa", node.OS.Booted)
	}

	if node.OS.Staged == nil || node.OS.Staged.Digest != "sha256:bbbb" {
		t.Errorf("staged = %+v, want digest sha256:bbbb", node.OS.Staged)
	}

	if node.Kubernetes.Service != "k0scontroller.service" || !node.Kubernetes.Active {
		t.Errorf("kubernetes = %+v, want an active controller service", node.Kubernetes)
	}

	if node.Health.UptimeSeconds != 128450 {
		t.Errorf("UptimeSeconds = %d, want 128450", node.Health.UptimeSeconds)
	}
}

func TestCollectOnAMachineThatIsNotACoriumNode(t *testing.T) {
	// Provisioning a host without a corium block is a valid outcome, not a
	// fault, and the report must say so rather than look broken.
	inspector := &Inspector{Root: fakeRoot(t, map[string]string{
		"/etc/machine-id": "abc\n",
	}), Run: commands(nil)}

	node := inspector.Collect(t.Context())

	if node.Bootstrapped {
		t.Error("Bootstrapped = true on a machine with no state file")
	}

	if node.Role != "" || node.Kubernetes.Service != "" {
		t.Errorf("node claims a role it never had: %+v", node)
	}
}

func TestCollectSurvivesEveryToolBeingMissing(t *testing.T) {
	// This is the state an operator most needs a report from. Losing the
	// readable fields because bootc is broken would be exactly backwards.
	inspector := &Inspector{Root: fakeRoot(t, map[string]string{
		"/etc/machine-id": "abc\n",
		"/proc/uptime":    "42.0 0.0\n",
		StateFile:         `{"role":"worker"}`,
	}), Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no such file or directory")
	}}

	node := inspector.Collect(t.Context())

	if node.Role != "worker" {
		t.Errorf("Role = %q, want worker", node.Role)
	}

	if node.Health.UptimeSeconds != 42 {
		t.Errorf("UptimeSeconds = %d, want 42", node.Health.UptimeSeconds)
	}

	if node.OS.Booted != nil || node.Kubernetes.Version != "" {
		t.Error("fields that could not be read came back populated")
	}

	// The service is named from the role, which is known, even though nothing
	// could be asked about it.
	if node.Kubernetes.Service != "k0sworker.service" || node.Kubernetes.Active {
		t.Errorf("kubernetes = %+v", node.Kubernetes)
	}
}

func TestBootcSchemaChangingDoesNotTakeTheRestDown(t *testing.T) {
	// bootc documents this output as unstable. A shape we no longer recognise
	// must cost the deployment fields and nothing else.
	inspector := &Inspector{Root: fakeRoot(t, map[string]string{
		"/etc/machine-id": "abc\n",
		"/etc/os-release": "PRETTY_NAME=\"Fedora\"\n",
	}), Run: commands(map[string]string{
		"bootc": `{"status":{"booted":"a string where an object used to be"}}`,
	})}

	node := inspector.Collect(t.Context())

	if node.OS.Booted != nil {
		t.Error("an unrecognised bootc schema produced a deployment")
	}

	if node.OS.Name != "Fedora" {
		t.Errorf("OS.Name = %q, want the field that did parse", node.OS.Name)
	}
}

func TestGreenbootVerdict(t *testing.T) {
	for reply, want := range map[string]string{
		"success\n":   "passed",
		"exit-code\n": "failed",
		"timeout\n":   "failed",
		"\n":          "",
	} {
		inspector := &Inspector{
			Root: fakeRoot(t, nil),
			Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name != "systemctl" {
					return nil, errors.New("not installed")
				}

				return []byte(reply), nil
			},
		}

		if got := inspector.Collect(t.Context()).Health.Greenboot; got != want {
			t.Errorf("Result %q gave greenboot %q, want %q", reply, got, want)
		}
	}
}

func TestOSRelease(t *testing.T) {
	contents := "NAME=Fedora\nVERSION=\"44 (Cloud)\"\nID=fedora\n"

	if got := osRelease(contents, "VERSION"); got != "44 (Cloud)" {
		t.Errorf("osRelease() = %q, want the unquoted value", got)
	}

	if got := osRelease(contents, "MISSING"); got != "" {
		t.Errorf("osRelease() = %q for an absent key, want empty", got)
	}
}
