package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
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

// configurable starts a claimed node whose applied configuration lands
// somewhere a test may look at it.
func configurable(t *testing.T, ca *authority, bootstrapped bool) (string, *Store, string) {
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

	path := filepath.Join(t.TempDir(), "config.yaml")
	server.configPath = path

	if bootstrapped {
		nodeThatHasBootstrapped(t, server)
	} else {
		nodeThatHasNotBootstrapped(t, server)
	}

	return serveOn(t, server), store, path
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

func TestApplyIsRefusedOnceTheNodeHasBootstrapped(t *testing.T) {
	// The rule the whole endpoint rests on. A node whose role or cluster can be
	// rewritten under a running Kubernetes is a node whose configuration and
	// behaviour are two different facts.
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
