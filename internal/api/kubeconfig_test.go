package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/kubeconfig"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

const adminKubeconfig = `apiVersion: v1
clusters:
- cluster:
    server: https://192.168.0.122:6443
  name: local
kind: Config
`

// kubeconfigNode starts a claimed node whose recorded state is constructed, so
// the assertion is about the route rather than the host running the tests.
func kubeconfigNode(t *testing.T, state string) (*authority, string, *Store) {
	t.Helper()

	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib/corium"), 0o755); err != nil {
		t.Fatalf("creating the state directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, nodeinfo.StateFile), []byte(state), 0o600); err != nil {
		t.Fatalf("writing state: %v", err)
	}

	server.Inspect(&nodeinfo.Inspector{Root: root, Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return nil, os.ErrNotExist
	}})

	server.Kubeconfig(&kubeconfig.Manager{Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return []byte(adminKubeconfig), nil
	}})

	return ca, serveOn(t, server), store
}

func fetch(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()

	response, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}

	defer func() { _ = response.Body.Close() }()

	body := make([]byte, 4096)
	n, _ := response.Body.Read(body)

	return response.StatusCode, string(body[:n])
}

func TestKubeconfigNeedsAdmin(t *testing.T) {
	// It hands over a cluster, not a node. Every other call here acts on one
	// machine; this one outranks reset.
	ca, address, store := kubeconfigNode(t, `{"role":"single"}`)

	for _, role := range []Role{RoleReadOnly, RoleOperator} {
		status, _ := fetch(t, client(t, store, ca.issue(t, role)),
			"https://"+address+"/v1/kubeconfig")

		if status != http.StatusForbidden {
			t.Errorf("status = %d as %s, want 403", status, role)
		}
	}
}

func TestAWorkerHasNoAdminCredentialsToGive(t *testing.T) {
	// 409: nothing is wrong with the request. A worker holds kubelet
	// credentials, which are not an administrator's.
	ca, address, store := kubeconfigNode(t, `{"role":"worker"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/kubeconfig")

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", status, body)
	}

	if !strings.Contains(body, "control plane") {
		t.Errorf("error = %q, want it to say which nodes have them", body)
	}
}

func TestKubeconfigPointsAtTheRecordedEndpoint(t *testing.T) {
	// On an HA control plane that is the virtual IP, and a kubeconfig aimed at
	// one particular controller stops working the first time that controller
	// does.
	ca, address, store := kubeconfigNode(t,
		`{"role":"controller","endpoint":"192.168.0.200"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/kubeconfig")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}

	if !strings.Contains(body, "https://192.168.0.200:6443") {
		t.Errorf("kubeconfig does not point at the recorded endpoint:\n%s", body)
	}
}

func TestServerCanBeOverridden(t *testing.T) {
	ca, address, store := kubeconfigNode(t,
		`{"role":"controller","endpoint":"192.168.0.200"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/kubeconfig?server=api.example.com:8443")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}

	if !strings.Contains(body, "https://api.example.com:8443") {
		t.Errorf("kubeconfig ignored --server:\n%s", body)
	}
}

func TestAServerAddressThatIsNotOneIsRefused(t *testing.T) {
	ca, address, store := kubeconfigNode(t, `{"role":"single"}`)

	status, _ := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/kubeconfig?server=two%20words")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestKubeconfigIsNotServedAsSomethingABrowserWouldRender(t *testing.T) {
	ca, address, store := kubeconfigNode(t, `{"role":"single"}`)

	response, err := client(t, store, ca.issue(t, RoleAdmin)).
		Get("https://" + address + "/v1/kubeconfig")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if got := response.Header.Get("Content-Type"); got != "application/yaml" {
		t.Errorf("Content-Type = %q", got)
	}

	// It carries cluster-admin credentials: nothing should cache it, and
	// nothing should guess at its type.
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
