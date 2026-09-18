package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Corium-OS/Corium/internal/access"
)

// nodeWithAccess starts a claimed node whose SSH keys land in a test directory,
// so a handler test never writes to the real /var/lib/corium/ssh.
func nodeWithAccess(t *testing.T, manager *access.Manager) (*authority, string, *Store) {
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

	server.Access(manager)

	return ca, serveOn(t, server), store
}

// testAccessManager stubs the two things a real one reaches for: the user
// database and the SELinux relabel. Both are made to succeed silently.
func testAccessManager(t *testing.T) *access.Manager {
	t.Helper()

	return &access.Manager{
		Dir:        t.TempDir(),
		LookupUser: func(string) error { return nil },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
}

func sshPublicKey(t *testing.T) string {
	t.Helper()

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	wrapped, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("wrapping key: %v", err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(wrapped))) + " tester@host"
}

func deleteRequest(t *testing.T, c *http.Client, address string) (int, map[string]any) {
	t.Helper()

	request, err := http.NewRequest(http.MethodDelete, "https://"+address, nil)
	if err != nil {
		t.Fatalf("building DELETE: %v", err)
	}

	response, err := c.Do(request)
	if err != nil {
		t.Fatalf("DELETE %s: %v", address, err)
	}

	defer func() { _ = response.Body.Close() }()

	return response.StatusCode, decode(t, response.Body)
}

func TestAddingAnSSHKeyNeedsAdmin(t *testing.T) {
	// Trusting a key grants a shell, which is the one thing that steps outside
	// the API's guard rails. Reading, and even operator, is not enough.
	ca, address, store := nodeWithAccess(t, testAccessManager(t))

	status, _ := post(t, client(t, store, ca.issue(t, RoleReadOnly)), address,
		"/v1/access/ssh", addSSHKeyRequest{User: "core", Key: sshPublicKey(t)})
	if status != http.StatusForbidden {
		t.Errorf("readonly add status = %d, want 403", status)
	}

	status, _ = post(t, client(t, store, ca.issue(t, RoleOperator)), address,
		"/v1/access/ssh", addSSHKeyRequest{User: "core", Key: sshPublicKey(t)})
	if status != http.StatusForbidden {
		t.Errorf("operator add status = %d, want 403", status)
	}
}

func TestAddListAndRevokeRoundTrip(t *testing.T) {
	ca, address, store := nodeWithAccess(t, testAccessManager(t))

	status, body := post(t, client(t, store, ca.issue(t, RoleAdmin)), address,
		"/v1/access/ssh", addSSHKeyRequest{User: "core", Key: sshPublicKey(t)})
	if status != http.StatusOK {
		t.Fatalf("admin add status = %d, want 200 (%v)", status, body)
	}

	fingerprint, _ := body["fingerprint"].(string)
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q, want a SHA256: fingerprint", fingerprint)
	}

	// Listing is readonly: seeing what a node trusts is not itself a grant.
	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/access/ssh")
	if err != nil {
		t.Fatalf("GET /v1/access/ssh: %v", err)
	}

	listed := decode(t, response.Body)
	_ = response.Body.Close()

	keys, ok := listed["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("list = %v, want the one key just added", listed)
	}

	if first, _ := keys[0].(map[string]any); first["fingerprint"] != fingerprint {
		t.Errorf("listed fingerprint = %v, want %q", keys[0], fingerprint)
	}

	// Revoking names the key by fingerprint in the query, because a SHA-256
	// fingerprint carries '/' and cannot be a path segment.
	revoke := "/v1/access/ssh?" + url.Values{"fingerprint": {fingerprint}}.Encode()

	status, body = deleteRequest(t, client(t, store, ca.issue(t, RoleAdmin)), address+revoke)
	if status != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200 (%v)", status, body)
	}

	if removed, ok := body["removed"].([]any); !ok || len(removed) != 1 {
		t.Errorf("removed = %v, want one key", body["removed"])
	}

	response, err = client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/access/ssh")
	if err != nil {
		t.Fatalf("GET /v1/access/ssh after revoke: %v", err)
	}

	listed = decode(t, response.Body)
	_ = response.Body.Close()

	if keys, _ := listed["keys"].([]any); len(keys) != 0 {
		t.Errorf("list after revoke = %v, want empty", listed)
	}
}

func TestAddingForAnUnknownUserIsAConflict(t *testing.T) {
	manager := testAccessManager(t)
	manager.LookupUser = func(name string) error {
		return fmt.Errorf("%q: %w", name, access.ErrUnknownUser)
	}

	ca, address, store := nodeWithAccess(t, manager)

	status, body := post(t, client(t, store, ca.issue(t, RoleAdmin)), address,
		"/v1/access/ssh", addSSHKeyRequest{User: "ghost", Key: sshPublicKey(t)})
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "cloud-init") {
		t.Errorf("error = %q, want it to point at cloud-init as the way to make a user", message)
	}
}

func TestAddingAGarbageKeyIsABadRequest(t *testing.T) {
	ca, address, store := nodeWithAccess(t, testAccessManager(t))

	status, _ := post(t, client(t, store, ca.issue(t, RoleAdmin)), address,
		"/v1/access/ssh", addSSHKeyRequest{User: "core", Key: "ssh-ed25519 not-a-real-key"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d for an unparseable key, want 400", status)
	}
}

func TestRevokingNeedsAdmin(t *testing.T) {
	ca, address, store := nodeWithAccess(t, testAccessManager(t))

	revoke := "/v1/access/ssh?" + url.Values{"fingerprint": {"SHA256:whatever"}}.Encode()

	status, _ := deleteRequest(t, client(t, store, ca.issue(t, RoleReadOnly)), address+revoke)
	if status != http.StatusForbidden {
		t.Errorf("readonly revoke status = %d, want 403", status)
	}
}
