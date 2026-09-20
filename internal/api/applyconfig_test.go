package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
)

// aDocument is the smallest configuration a node will accept.
const aDocument = "corium:\n  role: single\n"

// nodeThatHasNotBootstrapped points the server's inspector at a filesystem
// with no bootstrap marker in it, rather than at the machine running the test.
func nodeThatHasNotBootstrapped(t *testing.T, server *Server) {
	t.Helper()

	server.inspector = &nodeinfo.Inspector{Root: t.TempDir()}
}

// nodeThatHasBootstrapped does the opposite, by planting the marker a real one
// writes.
func nodeThatHasBootstrapped(t *testing.T, server *Server) {
	t.Helper()

	root := t.TempDir()
	marker := filepath.Join(root, nodeinfo.MarkerFile)

	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatalf("creating the marker directory: %v", err)
	}

	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}

	server.inspector = &nodeinfo.Inspector{Root: root}
}

// configurableServer builds a claimed node whose configuration paths land in a
// test's own directories, without serving it yet -- so a test can seed a
// baseline or stub systemd before the first request. configPath and appliedPath
// are returned for exactly that.
func configurableServer(t *testing.T, ca *authority, bootstrapped bool) (*Server, *Store, string, string) {
	t.Helper()

	store := newTestStore(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if err := store.RecordClaim(ClaimedWithPairingCode); err != nil {
		t.Fatalf("RecordClaim() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	appliedPath := filepath.Join(dir, "applied.yaml")

	// Pointed at the test's own paths, never the real /etc or /var, so a run
	// neither reads the machine's configuration nor writes over it.
	server.configPath = configPath
	server.appliedPath = appliedPath
	server.k0sConfigPath = filepath.Join(dir, "k0s.yaml")

	if bootstrapped {
		nodeThatHasBootstrapped(t, server)
	} else {
		nodeThatHasNotBootstrapped(t, server)
	}

	return server, store, configPath, appliedPath
}

// configurable starts a claimed node whose applied configuration lands
// somewhere a test may look at it.
func configurable(t *testing.T, ca *authority, bootstrapped bool) (string, *Store, string) {
	t.Helper()

	server, store, configPath, _ := configurableServer(t, ca, bootstrapped)

	return serveOn(t, server), store, configPath
}

func configBody(t *testing.T, document string) string {
	t.Helper()

	encoded, err := json.Marshal(configRequest{Document: document})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	return string(encoded)
}

func TestApplyWritesTheDocumentBeforeBootstrap(t *testing.T) {
	ca := newAuthority(t)
	address, store, path := configurable(t, ca, false)

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/config", configBody(t, aDocument))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if role, _ := body["role"].(string); role != "single" {
		t.Errorf("role = %q, want the node's own reading of the document", role)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading what was written: %v", err)
	}

	if string(written) != aDocument {
		t.Errorf("written = %q, want the document that was sent", written)
	}

	// A corium: document may carry a join token or a VRRP password. Writing it
	// where every local account can read it would give away at the last step
	// what resolving those from a SecretSource exists to protect.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

func TestApplyIsRefusedWithoutARecordedBaseline(t *testing.T) {
	// A bootstrapped node with nothing recorded to diff against cannot tell a
	// safe change from an unsafe one, so it refuses rather than guessing -- and
	// says the same thing it always did: reset.
	ca := newAuthority(t)
	address, store, path := configurable(t, ca, true)

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/config", configBody(t, aDocument))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "cctl reset") {
		t.Errorf("error = %q, want it to say what to do instead", message)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused apply wrote the document anyway")
	}
}

func TestApplyRefusesAnImmutableChangeOnARunningNode(t *testing.T) {
	// The rule the whole endpoint rests on. A node whose role, cluster or name
	// can be rewritten under a running Kubernetes is a node whose configuration
	// and behaviour are two different facts. The refusal names the field.
	ca := newAuthority(t)
	server, store, configPath, appliedPath := configurableServer(t, ca, true)

	// The node is running a single-node cluster; the operator tries to rename it.
	if err := os.WriteFile(appliedPath, []byte("role: single\n"), 0o600); err != nil {
		t.Fatalf("seeding the baseline: %v", err)
	}

	address := serveOn(t, server)
	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/config",
		configBody(t, "corium:\n  role: single\n  node:\n    name: renamed\n"))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	message, _ := body["error"].(string)
	if !strings.Contains(message, "node") || !strings.Contains(message, "cctl reset") {
		t.Errorf("error = %q, want it to name the field and point at cctl reset", message)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("a refused apply wrote the document anyway")
	}
}

func TestApplyReconcilesAddonsOnARunningNode(t *testing.T) {
	// The safe subset: an operator adds a chart to a node already in service,
	// and the node re-renders k0s and cycles the control plane to pick it up,
	// rather than sending them to reset. See ADR 8.
	ca := newAuthority(t)
	server, store, configPath, appliedPath := configurableServer(t, ca, true)

	if err := os.WriteFile(appliedPath, []byte("role: single\n"), 0o600); err != nil {
		t.Fatalf("seeding the baseline: %v", err)
	}

	var restarted string
	server.Supervise(&systemd.Manager{
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "restart" {
				restarted = args[1]
			}

			return nil, nil
		},
	})

	address := serveOn(t, server)
	admin := client(t, store, ca.issue(t, RoleAdmin))

	document := "corium:\n" +
		"  role: single\n" +
		"  addons:\n" +
		"    - name: cert-manager\n" +
		"      chart: jetstack/cert-manager\n" +
		"      namespace: cert-manager\n" +
		"      repository: {name: jetstack, url: 'https://charts.jetstack.io'}\n"

	status, body := postRaw(t, admin, "https://"+address+"/v1/config", configBody(t, document))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if got, _ := body["status"].(string); got != "reconciled" {
		t.Errorf("status = %q, want reconciled", got)
	}

	if restarted != "k0scontroller.service" {
		t.Errorf("restarted %q, want k0scontroller.service", restarted)
	}

	rendered, err := os.ReadFile(server.k0sConfigPath)
	if err != nil {
		t.Fatalf("the reconcile did not write a k0s configuration: %v", err)
	}

	if !strings.Contains(string(rendered), "cert-manager") {
		t.Errorf("k0s.yaml does not carry the new chart:\n%s", rendered)
	}

	written, err := os.ReadFile(configPath)
	if err != nil || string(written) != document {
		t.Errorf("the reconciled document was not recorded: %v\n%s", err, written)
	}
}

func TestApplyRefusesRemovingAnAddonOnARunningNode(t *testing.T) {
	// k0s leaves a dropped chart's release running, so a removal is refused
	// rather than reported as done. The message points at the k0s way to remove.
	ca := newAuthority(t)
	server, store, configPath, appliedPath := configurableServer(t, ca, true)

	baseline := "role: single\n" +
		"api: { enabled: true }\n" +
		"addons:\n" +
		"  - name: cert-manager\n" +
		"    chart: jetstack/cert-manager\n" +
		"    namespace: cert-manager\n" +
		"    repository: { name: jetstack, url: 'https://charts.jetstack.io' }\n"

	if err := os.WriteFile(appliedPath, []byte(baseline), 0o600); err != nil {
		t.Fatalf("seeding the baseline: %v", err)
	}

	address := serveOn(t, server)
	admin := client(t, store, ca.issue(t, RoleAdmin))

	// The same node, with cert-manager dropped from the document.
	status, body := postRaw(t, admin, "https://"+address+"/v1/config",
		configBody(t, "corium:\n  role: single\n  api: { enabled: true }\n"))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	message, _ := body["error"].(string)
	if !strings.Contains(message, "cert-manager") || !strings.Contains(message, "kubectl delete chart") {
		t.Errorf("error = %q, want it to name the chart and the k0s way to remove it", message)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("a refused apply wrote the document anyway")
	}
}

func TestApplyOnARunningNodeIsANoOpWhenUnchanged(t *testing.T) {
	// A document that matches what the node is running does nothing: no restart,
	// no rewrite of the rendered k0s configuration.
	ca := newAuthority(t)
	server, store, _, appliedPath := configurableServer(t, ca, true)

	if err := os.WriteFile(appliedPath, []byte("role: single\n"), 0o600); err != nil {
		t.Fatalf("seeding the baseline: %v", err)
	}

	restarted := false
	server.Supervise(&systemd.Manager{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 1 && args[0] == "restart" {
				restarted = true
			}

			return nil, nil
		},
	})

	address := serveOn(t, server)
	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/config", configBody(t, aDocument))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if got, _ := body["status"].(string); got != "unchanged" {
		t.Errorf("status = %q, want unchanged", got)
	}

	if restarted {
		t.Error("an unchanged apply restarted the control plane")
	}

	if _, err := os.Stat(server.k0sConfigPath); !os.IsNotExist(err) {
		t.Error("an unchanged apply rewrote the k0s configuration")
	}
}

func TestApplyRefusesADocumentTheNodeWouldNotBootstrapWith(t *testing.T) {
	// Checked on the node and not only in cctl. Accepting over the wire what
	// its own bootstrap would refuse leaves a node holding a document
	// guaranteed to fail on the next boot, with nobody there to read it.
	ca := newAuthority(t)
	address, store, path := configurable(t, ca, false)

	admin := client(t, store, ca.issue(t, RoleAdmin))

	for name, document := range map[string]string{
		"not a corium document": "hello: there\n",
		"an unknown role":       "corium:\n  role: overlord\n",
		"not YAML at all":       "\tthis: [is not\n",
	} {
		t.Run(name, func(t *testing.T) {
			status, body := postRaw(t, admin, "https://"+address+"/v1/config",
				configBody(t, document))
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%v)", status, body)
			}
		})
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected document was written anyway")
	}
}

func TestApplyNeedsAdmin(t *testing.T) {
	ca := newAuthority(t)
	address, store, _ := configurable(t, ca, false)

	for _, role := range []Role{RoleReadOnly, RoleOperator} {
		caller := client(t, store, ca.issue(t, role))

		status, _ := postRaw(t, caller, "https://"+address+"/v1/config",
			configBody(t, aDocument))
		if status != http.StatusForbidden {
			t.Errorf("%s got status %d, want 403", role, status)
		}
	}
}

func TestEnrolmentCarriesTheConfigurationWithIt(t *testing.T) {
	// Claiming a node in maintenance mode is what releases its bootstrap, so a
	// document that arrives with the claim is the only one guaranteed to be on
	// disk before the node starts becoming something.
	store := newTestStore(t)

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	server.configPath = path
	nodeThatHasNotBootstrapped(t, server)

	address := serveOn(t, server)
	ca := newAuthority(t)

	encoded, err := json.Marshal(enrolRequest{
		Code:       server.PairingCode(),
		OperatorCA: string(ca.pem),
		Document:   aDocument,
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	status, body := postRaw(t, client(t, store), "https://"+address+"/v1/enroll", string(encoded))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the enrolment did not write the document: %v", err)
	}

	if string(written) != aDocument {
		t.Errorf("written = %q, want the document sent with the enrolment", written)
	}
}

func TestABadDocumentDoesNotHalfEnrolTheNode(t *testing.T) {
	// The document is written before the claim is recorded, so a document the
	// node refuses must leave it unclaimed rather than claimed and
	// misconfigured.
	store := newTestStore(t)

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	server.configPath = filepath.Join(t.TempDir(), "config.yaml")
	nodeThatHasNotBootstrapped(t, server)

	address := serveOn(t, server)
	ca := newAuthority(t)

	encoded, err := json.Marshal(enrolRequest{
		Code:       server.PairingCode(),
		OperatorCA: string(ca.pem),
		Document:   "corium:\n  role: overlord\n",
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	status, _ := postRaw(t, client(t, store), "https://"+address+"/v1/enroll", string(encoded))
	if status == http.StatusOK {
		t.Fatal("the node was claimed despite refusing the configuration sent with the claim")
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("the node is enrolled after an enrolment that failed")
	}

	// And the attempt was not charged: the caller proved they were at the
	// console, and what went wrong was not the code.
	if !server.enroller.Open() {
		t.Error("the node stopped accepting enrolments over a bad document")
	}
}

func TestApplyTellsAHeldBootstrapThatSomebodyAnswered(t *testing.T) {
	// The bootstrap waits on this marker rather than on the document, because
	// the two units are not ordered against each other and a document can
	// arrive before anybody is watching for it.
	ca := newAuthority(t)
	store := newTestStore(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if err := store.RecordClaim(ClaimedWithPairingCode); err != nil {
		t.Fatalf("RecordClaim() error = %v", err)
	}

	session := SessionDir(t.TempDir())

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, session)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	server.configPath = filepath.Join(t.TempDir(), "config.yaml")
	nodeThatHasNotBootstrapped(t, server)

	address := serveOn(t, server)
	marker := filepath.Join(string(session), AppliedMarker)

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the marker exists before anything was applied")
	}

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := postRaw(t, admin, "https://"+address+"/v1/config", configBody(t, aDocument))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a held bootstrap was never told the configuration arrived: %v", err)
	}
}

func TestClaimingANodeThatWouldBootstrapIsRefusedUntilItIsMeant(t *testing.T) {
	// Recording the claim is what releases a held bootstrap, so a node whose
	// own document names a role starts becoming that node the instant it is
	// claimed. This is not a permission check -- the caller holds the pairing
	// code and is entitled to do it -- it is the node declining to do something
	// irreversible on an unstated assumption.
	store := newTestStore(t)

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	nodeThatHasNotBootstrapped(t, server)
	server.BootedConfig(&config.Config{
		Role:    config.RoleSingle,
		Cluster: config.Cluster{Name: "corium"},
	})

	address := serveOn(t, server)
	ca := newAuthority(t)
	code := server.PairingCode()

	claim := func(acknowledge bool) (int, map[string]any) {
		t.Helper()

		encoded, err := json.Marshal(enrolRequest{
			Code:        code,
			OperatorCA:  string(ca.pem),
			Acknowledge: acknowledge,
		})
		if err != nil {
			t.Fatalf("encoding the request: %v", err)
		}

		return postRaw(t, client(t, store), "https://"+address+"/v1/enroll", string(encoded))
	}

	status, body := claim(false)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	if body["reason"] != ReasonWouldBootstrap {
		t.Errorf("reason = %v, want %q: cctl decides what to do next from this, not from the prose",
			body["reason"], ReasonWouldBootstrap)
	}

	if enrolled, err := store.Enrolled(); err != nil || enrolled {
		t.Fatalf("Enrolled() = %v (err %v), want false: a refused claim claims nothing", enrolled, err)
	}

	// The refusal cost nothing. The same pairing code works on the way back,
	// which is the only thing that makes asking a person a reasonable design.
	if status, body = claim(true); status != http.StatusOK {
		t.Fatalf("status = %d after acknowledging, want 200 (%v)", status, body)
	}
}

func TestANodeWaitingToBeToldIsClaimedWithoutQuestion(t *testing.T) {
	// The fleet pattern: a document that names no role builds nothing on being
	// claimed, so there is nothing here to warn anybody about. Asking would
	// train operators to type y at the one prompt that matters.
	store := newTestStore(t)

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	nodeThatHasNotBootstrapped(t, server)
	server.BootedConfig(&config.Config{API: config.API{Enabled: enabledPointer(true)}})

	address := serveOn(t, server)
	ca := newAuthority(t)

	encoded, err := json.Marshal(enrolRequest{
		Code:       server.PairingCode(),
		OperatorCA: string(ca.pem),
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	status, body := postRaw(t, client(t, store), "https://"+address+"/v1/enroll", string(encoded))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
}

func enabledPointer(v bool) *bool { return &v }
