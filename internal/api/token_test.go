package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/token"
)

// joinTokenNode starts a claimed node whose recorded state is constructed, with
// a token manager that echoes the arguments it was handed back as the "token".
// That lets a test assert what k0s was asked for without a k0s to ask.
func joinTokenNode(t *testing.T, state string) (*authority, string, *Store) {
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

	server.Tokens(&token.Manager{Run: func(
		_ context.Context, _ string, args ...string,
	) ([]byte, error) {
		return []byte("token[" + strings.Join(args, " ") + "]\n"), nil
	}})

	return ca, serveOn(t, server), store
}

func TestJoinTokenMintsAWorkerTokenByDefault(t *testing.T) {
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/join-token")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}

	// Defaults: a worker token, valid for the manager's default expiry.
	for _, want := range []string{"--role=worker", "--expiry=1h"} {
		if !strings.Contains(body, want) {
			t.Errorf("k0s was not asked for %q:\n%s", want, body)
		}
	}
}

func TestJoinTokenPassesRoleAndExpiryThrough(t *testing.T) {
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/join-token?role=controller&expiry=2h")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}

	for _, want := range []string{"--role=controller", "--expiry=2h"} {
		if !strings.Contains(body, want) {
			t.Errorf("k0s was not asked for %q:\n%s", want, body)
		}
	}
}

func TestJoinTokenNeedsAdmin(t *testing.T) {
	// A join token adds a machine to the cluster, so it outranks operator work.
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	for _, role := range []Role{RoleReadOnly, RoleOperator} {
		status, _ := fetch(t, client(t, store, ca.issue(t, role)),
			"https://"+address+"/v1/join-token")

		if status != http.StatusForbidden {
			t.Errorf("status = %d as %s, want 403", status, role)
		}
	}
}

func TestAWorkerCannotMintAJoinToken(t *testing.T) {
	// 409: nothing is wrong with the request. Only a controller has k0s and the
	// cluster CA to mint from.
	ca, address, store := joinTokenNode(t, `{"role":"worker"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/join-token")

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", status, body)
	}

	if !strings.Contains(body, "control plane") {
		t.Errorf("error = %q, want it to say which nodes can mint", body)
	}
}

func TestJoinTokenRejectsAnUnknownRole(t *testing.T) {
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	status, _ := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/join-token?role=admin")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestJoinTokenRejectsABadExpiry(t *testing.T) {
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	status, _ := fetch(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/join-token?expiry=soon")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestJoinTokenIsNotServedAsSomethingABrowserWouldRender(t *testing.T) {
	ca, address, store := joinTokenNode(t, `{"role":"controller"}`)

	response, err := client(t, store, ca.issue(t, RoleAdmin)).
		Get("https://" + address + "/v1/join-token")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}

	// It is a live credential: nothing should cache it, nothing should guess it.
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
