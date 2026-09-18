package kubeconfig

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// What k0s actually emits, trimmed. The certificate authority is the part that
// matters for the parsing decision: it is base64 and contains no clue that it
// is not a server line.
const k0sOutput = `apiVersion: v1
clusters:
- cluster:
    server: https://192.168.0.122:6443
    certificate-authority-data: LS0tLXNlcnZlcjogaHR0cHM6Ly9ldmlsOjY0NDMK
  name: local
contexts:
- context:
    cluster: local
    user: user
  name: Default
current-context: Default
kind: Config
users:
- name: user
  user:
    client-certificate-data: LS0tCg==
    client-key-data: LS0tCg==
`

func serverOf(t *testing.T, raw []byte) string {
	t.Helper()

	var document struct {
		Clusters []struct {
			Cluster struct {
				Server    string `yaml:"server"`
				Authority string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}

	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("result is not a kubeconfig: %v", err)
	}

	if len(document.Clusters) == 0 {
		t.Fatal("result names no clusters")
	}

	return document.Clusters[0].Cluster.Server
}

func TestRetargetRewritesOnlyTheServer(t *testing.T) {
	out, err := Retarget([]byte(k0sOutput), "192.168.0.200")
	if err != nil {
		t.Fatalf("Retarget() error = %v", err)
	}

	if got := serverOf(t, out); got != "https://192.168.0.200:6443" {
		t.Errorf("server = %q, want the virtual IP", got)
	}

	// The certificate authority in this fixture decodes to something that
	// looks like a server line. Anything substituting rather than parsing
	// would corrupt it, and the node would be unreachable for a reason nobody
	// would think to look for.
	if !strings.Contains(string(out), "LS0tLXNlcnZlcjogaHR0cHM6Ly9ldmlsOjY0NDMK") {
		t.Error("the certificate authority was altered")
	}

	// And the rest of the file survives.
	for _, want := range []string{"current-context: Default", "client-key-data", "kind: Config"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("result lost %q:\n%s", want, out)
		}
	}
}

func TestRetargetRefusesSomethingThatIsNotAKubeconfig(t *testing.T) {
	// Returning a file that looks retargeted and is not would be worse than
	// refusing: it fails later, somewhere else.
	for name, raw := range map[string]string{
		"not yaml":    "\tnot: [a",
		"no clusters": "apiVersion: v1\nkind: Config\n",
		"empty list":  "clusters: []\n",
		"wrong shape": "clusters:\n- name: local\n",
	} {
		if _, err := Retarget([]byte(raw), "10.0.0.1"); err == nil {
			t.Errorf("Retarget(%s) = nil, want a refusal", name)
		}
	}
}

func TestServerURL(t *testing.T) {
	for given, want := range map[string]string{
		"192.168.0.200":             "https://192.168.0.200:6443",
		"192.168.0.200:6443":        "https://192.168.0.200:6443",
		"192.168.0.200:8443":        "https://192.168.0.200:8443",
		"api.example.com":           "https://api.example.com:6443",
		"https://api.example.com":   "https://api.example.com",
		"http://api.example.com:80": "http://api.example.com:80",
		// An IPv6 literal must not have its last group read as a port.
		"2001:db8::1":        "https://[2001:db8::1]:6443",
		"[2001:db8::1]:6443": "https://[2001:db8::1]:6443",
	} {
		if got := ServerURL(given); got != want {
			t.Errorf("ServerURL(%q) = %q, want %q", given, got, want)
		}
	}
}

func TestAdminLeavesTheAddressAloneWhenNotAsked(t *testing.T) {
	// An empty server means "whatever k0s said", which is right for a single
	// node and is the caller's decision rather than this package's.
	manager := &Manager{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(k0sOutput), nil
	}}

	out, err := manager.Admin(t.Context(), "")
	if err != nil {
		t.Fatalf("Admin() error = %v", err)
	}

	if string(out) != k0sOutput {
		t.Error("Admin() altered a kubeconfig it was not asked to retarget")
	}
}

func TestAdminReportsWhatK0sSaid(t *testing.T) {
	manager := &Manager{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("Error: failed to get admin kubeconfig")
	}}

	_, err := manager.Admin(t.Context(), "")
	if err == nil || !strings.Contains(err.Error(), "admin kubeconfig") {
		t.Errorf("Admin() = %v, want k0s's own words", err)
	}
}
