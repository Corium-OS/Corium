package ui_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
	"github.com/Corium-OS/Corium/internal/ui"
)

const listen = "127.0.0.1:7500"

// fakeNode is one machine, served over TLS the way a real one is, so that the
// dashboard goes through the same client and the same pinning as cctl does.
type fakeNode struct {
	hostname string
	down     bool
	restarts []string
	cordons  int
}

func (f *fakeNode) client(t *testing.T) *cctl.Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&nodeinfo.Node{
			Hostname:     f.hostname,
			Bootstrapped: true,
			Role:         "single",
			Kubernetes:   nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0", Active: true},
		})
	})

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok", "role": string(api.RoleAdmin),
		})
	})

	mux.HandleFunc("GET /v1/services", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"services": []systemd.Status{{Unit: systemd.Unit{Name: "k0scontroller.service"}}},
		})
	})

	mux.HandleFunc("POST /v1/services/{unit}/restart", func(w http.ResponseWriter, r *http.Request) {
		f.restarts = append(f.restarts, r.PathValue("unit"))
		_ = json.NewEncoder(w).Encode(systemd.Status{Unit: systemd.Unit{Name: r.PathValue("unit")}})
	})

	mux.HandleFunc("POST /v1/lifecycle/cordon", func(w http.ResponseWriter, _ *http.Request) {
		f.cordons++
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "cordoned"})
	})

	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	if f.down {
		server.Close()
	}

	return cctl.Dial(strings.TrimPrefix(server.URL, "https://"),
		api.Fingerprint(server.Certificate().Raw))
}

// dashboard builds a server for the named nodes and a browser that already
// holds its token, which is what every test but the guard's own wants.
func dashboard(t *testing.T, nodes map[string]*fakeNode) (*ui.Server, func(*http.Request) *httptest.ResponseRecorder) {
	t.Helper()

	addresses := make([]string, 0, len(nodes))
	for address := range nodes {
		addresses = append(addresses, address)
	}

	server, err := ui.NewServer(listen, addresses, func(address string) (*cctl.Client, error) {
		node, ok := nodes[address]
		if !ok {
			return nil, errors.New("no such node")
		}

		return node.client(t), nil
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	handler := server.Handler()

	return server, func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}
}

func token(server *ui.Server, t *testing.T) string {
	t.Helper()

	_, after, found := strings.Cut(server.URL(), "token=")
	if !found {
		t.Fatalf("URL() = %q, want a token in it", server.URL())
	}

	return after
}

// authorised builds a request the way the page does: same host, same origin,
// carrying the cookie the first load set.
func authorised(t *testing.T, server *ui.Server, method, path string, body string) *http.Request {
	t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}

	r := httptest.NewRequest(method, "http://"+listen+path, reader)
	r.Host = listen
	r.Header.Set("Origin", "http://"+listen)
	r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

	return r
}

func TestOverviewShowsEveryNodeIncludingTheOnesThatDidNotAnswer(t *testing.T) {
	// The property the whole page rests on. A dashboard that failed whole
	// because one machine is off would hide the other nine at exactly the
	// moment somebody opened it to find out which one is off.
	nodes := map[string]*fakeNode{
		"a:7443": {hostname: "corium-a"},
		"b:7443": {hostname: "corium-b", down: true},
	}

	server, call := dashboard(t, nodes)

	response := call(authorised(t, server, http.MethodGet, "/api/overview", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
	}

	var body struct {
		Nodes []ui.Summary `json:"nodes"`
	}

	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(body.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(body.Nodes))
	}

	byAddress := map[string]ui.Summary{}
	for _, summary := range body.Nodes {
		byAddress[summary.Address] = summary
	}

	if got := byAddress["a:7443"]; got.Node == nil || got.Node.Hostname != "corium-a" {
		t.Errorf("a:7443 = %+v, want the node that answered", got)
	}

	if got := byAddress["b:7443"]; got.Error == "" {
		t.Errorf("b:7443 = %+v, want the failure reported rather than dropped", got)
	}
}

func TestDashboardRefusesAnAddressItWasNotStartedFor(t *testing.T) {
	// Without this the dashboard is an open relay for the operator's
	// certificate: any page in the browser could name any host on the
	// operator's network and have this server dial it with a fleet's
	// credentials attached.
	server, call := dashboard(t, map[string]*fakeNode{"a:7443": {hostname: "corium-a"}})

	for _, path := range []string{
		"/api/nodes/" + "evil.example:7443",
		"/api/nodes/" + "evil.example:7443/services",
	} {
		response := call(authorised(t, server, http.MethodGet, path, ""))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.Code)
		}
	}

	response := call(authorised(t, server, http.MethodPost,
		"/api/nodes/evil.example:7443/drain", "{}"))
	if response.Code != http.StatusNotFound {
		t.Errorf("POST drain = %d, want 404", response.Code)
	}
}

func TestActionsReachTheNode(t *testing.T) {
	node := &fakeNode{hostname: "corium-a"}
	server, call := dashboard(t, map[string]*fakeNode{"a:7443": node})

	response := call(authorised(t, server, http.MethodPost,
		"/api/nodes/a:7443/restart", `{"unit":"k0scontroller.service"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("restart = %d, want 200: %s", response.Code, response.Body)
	}

	response = call(authorised(t, server, http.MethodPost,
		"/api/nodes/a:7443/cordon", `{"undo":false}`))
	if response.Code != http.StatusOK {
		t.Fatalf("cordon = %d, want 200: %s", response.Code, response.Body)
	}
}

func TestRestartWithoutAUnitIsRefusedHereRatherThanAtTheNode(t *testing.T) {
	node := &fakeNode{hostname: "corium-a"}
	server, call := dashboard(t, map[string]*fakeNode{"a:7443": node})

	response := call(authorised(t, server, http.MethodPost, "/api/nodes/a:7443/restart", `{}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("restart with no unit = %d, want 400", response.Code)
	}

	if len(node.restarts) != 0 {
		t.Errorf("the node was asked anyway: %v", node.restarts)
	}
}

func TestNewServerRefusesToStartWithNothingToShow(t *testing.T) {
	if _, err := ui.NewServer(listen, nil, func(string) (*cctl.Client, error) {
		return nil, nil
	}); err == nil {
		t.Error("NewServer() with no nodes = nil, want a refusal")
	}
}
