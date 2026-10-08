package api

import (
	"net/http"
	"os"
	"testing"
)

// getJSON performs an authenticated GET and decodes the JSON reply.
func getJSON(t *testing.T, c *http.Client, url string) (int, map[string]any) {
	t.Helper()

	response, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}

	defer func() { _ = response.Body.Close() }()

	return response.StatusCode, decode(t, response.Body)
}

// seed writes a document to one of a test server's configuration paths.
func seed(t *testing.T, path, document string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("seeding %s: %v", path, err)
	}
}

func TestShowConfigPrefersTheRecordedBaseline(t *testing.T) {
	// The whole point of the endpoint: /var holds what the node acted on and
	// /etc holds what somebody wrote, and only the first describes what is
	// running. Reporting the wrong one would be worse than reporting nothing,
	// because the next apply diffs against the first.
	ca := newAuthority(t)
	server, store, configPath, appliedPath := configurableServer(t, ca, true)
	address := serveOn(t, server)

	seed(t, appliedPath, aDocument)
	seed(t, configPath, "corium:\n  role: controller\n")

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := getJSON(t, admin, "https://"+address+"/v1/config")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if got, _ := body["document"].(string); got != aDocument {
		t.Errorf("document = %q, want the recorded baseline %q", got, aDocument)
	}

	if got, _ := body["source"].(string); got != configSourceApplied {
		t.Errorf("source = %q, want %q", got, configSourceApplied)
	}

	if got, _ := body["path"].(string); got != appliedPath {
		t.Errorf("path = %q, want %q", got, appliedPath)
	}

	if edited, _ := body["localEdit"].(bool); !edited {
		t.Error("localEdit = false, want true: /etc was hand-edited and is not running")
	}
}

func TestShowConfigReportsNoLocalEditWhenTheTwoAgree(t *testing.T) {
	// A node that has only ever been configured over the API writes both files
	// from the same bytes. Flagging that as an edit would teach an operator to
	// ignore the field.
	ca := newAuthority(t)
	server, store, configPath, appliedPath := configurableServer(t, ca, true)
	address := serveOn(t, server)

	seed(t, appliedPath, aDocument)
	seed(t, configPath, aDocument+"\n")

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := getJSON(t, admin, "https://"+address+"/v1/config")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if edited, _ := body["localEdit"].(bool); edited {
		t.Error("localEdit = true, want false: the two differ only by a trailing newline")
	}
}

func TestShowConfigFallsBackToEtcWithoutABaseline(t *testing.T) {
	// A node that bootstrapped before the baseline was recorded still has
	// something to show, and the reply says which of the two it is so the
	// operator knows how much to trust it.
	ca := newAuthority(t)
	server, store, configPath, _ := configurableServer(t, ca, true)
	address := serveOn(t, server)

	seed(t, configPath, aDocument)

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := getJSON(t, admin, "https://"+address+"/v1/config")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if got, _ := body["source"].(string); got != configSourceEtc {
		t.Errorf("source = %q, want %q", got, configSourceEtc)
	}

	if edited, _ := body["localEdit"].(bool); edited {
		t.Error("localEdit = true, want false: /etc is the document being reported")
	}
}

func TestShowConfigRefusesWhenThereIsNothingRecorded(t *testing.T) {
	// 409, not 404: nothing is wrong with the request, the node simply has no
	// configuration of its own -- it bootstrapped from a source it does not keep.
	ca := newAuthority(t)
	server, store, _, _ := configurableServer(t, ca, true)
	address := serveOn(t, server)

	admin := client(t, store, ca.issue(t, RoleAdmin))

	status, body := getJSON(t, admin, "https://"+address+"/v1/config")
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}
}

func TestShowConfigNeedsAdmin(t *testing.T) {
	// A corium: document may carry an inline join token, a VRRP authPass or a
	// WireGuard private key, and k0s.patch may carry anything. That puts it at
	// the rung GET /v1/join-token already sits on, and out of reach of the two
	// below it.
	ca := newAuthority(t)
	server, store, _, appliedPath := configurableServer(t, ca, true)
	address := serveOn(t, server)

	seed(t, appliedPath, aDocument)

	for _, role := range []Role{RoleReadOnly, RoleOperator} {
		status, _ := getJSON(t, client(t, store, ca.issue(t, role)), "https://"+address+"/v1/config")
		if status != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", role, status)
		}
	}
}
