package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/ui"
)

// guarded builds a dashboard whose nodes are never dialled. Every test here
// is about a request that must be refused before anything reaches a node, so
// the connector failing loudly is the point: if one of these ever gets past
// the guard, the test fails on the connector rather than passing quietly.
func guarded(t *testing.T) (*ui.Server, http.Handler) {
	t.Helper()

	server, err := ui.NewServer(listen, []string{"a:7443"}, func(string) (*cctl.Client, error) {
		t.Error("a refused request reached the connector")

		return nil, http.ErrNotSupported
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	return server, server.Handler()
}

func TestGuardRefusesARequestWithNoToken(t *testing.T) {
	server, handler := guarded(t)

	r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/api/overview", nil)
	r.Host = listen

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}

	if strings.Contains(w.Body.String(), token(server, t)) {
		t.Error("the refusal handed out the token")
	}
}

func TestGuardRefusesTheWrongToken(t *testing.T) {
	_, handler := guarded(t)

	r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/api/overview", nil)
	r.Host = listen
	r.AddCookie(&http.Cookie{Name: "corium_ui", Value: "not-the-token"})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestGuardRefusesAHostThatIsNotLoopback(t *testing.T) {
	// DNS rebinding: a name the attacker controls resolves to 127.0.0.1, so
	// the browser calls the request same-origin and attaches the cookie. What
	// it cannot do is change the Host header, which still says the attacker's
	// name.
	server, handler := guarded(t)

	for _, host := range []string{
		"rebind.example.com:7500", // the attack
		"127.0.0.1:8080",          // loopback, but not this server's port
		"127.0.0.1",               // no port at all means 80, never bound here
		"10.0.0.5:7500",           // a real interface
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/api/overview", nil)
		r.Host = host
		r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusForbidden {
			t.Errorf("Host %q = %d, want 403", host, w.Code)
		}
	}
}

func TestGuardRefusesACrossSitePost(t *testing.T) {
	// The cookie is SameSite=Strict, but a browser that sends it anyway --
	// or a request the page did not make -- is refused on the browser's own
	// account of where it came from.
	server, handler := guarded(t)

	for _, header := range []struct{ name, value string }{
		{"Origin", "https://evil.example"},
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+listen+"/api/nodes/a:7443/drain", nil)
		r.Host = listen
		r.Header.Set(header.name, header.value)
		r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusForbidden {
			t.Errorf("%s: %s = %d, want 403", header.name, header.value, w.Code)
		}
	}
}

func TestTheTokenLinkSetsACookieAndLeavesTheAddressBar(t *testing.T) {
	// The token is in the URL exactly once. After that it lives in an
	// HttpOnly cookie, so it is not in the address bar, the history, or any
	// Referer the page later produces.
	server, handler := guarded(t)

	r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/?token="+token(server, t), nil)
	r.Host = listen

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	cookies := (&http.Response{Header: w.Header()}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}

	cookie := cookies[0]

	if cookie.Value != token(server, t) {
		t.Error("the cookie does not carry the token")
	}

	if !cookie.HttpOnly {
		t.Error("the cookie is readable from script")
	}

	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", cookie.SameSite)
	}
}

func TestTheWrongTokenInTheLinkSetsNoCookie(t *testing.T) {
	_, handler := guarded(t)

	r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/?token=guessed", nil)
	r.Host = listen

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}

	if len((&http.Response{Header: w.Header()}).Cookies()) != 0 {
		t.Error("a guessed token was given a cookie")
	}
}

func TestThePageLoadsForAnAuthorisedBrowser(t *testing.T) {
	server, handler := guarded(t)

	r := httptest.NewRequest(http.MethodGet, "http://"+listen+"/", nil)
	r.Host = listen
	r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}

	if !strings.Contains(w.Body.String(), "/static/app.js") {
		t.Error("the page does not load its script")
	}

	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q, want the page locked to itself", got)
	}
}

func TestStaticFilesAreServedAndNothingElseIs(t *testing.T) {
	server, handler := guarded(t)

	// Only the two files the page loads. The tree behind this is an embedded
	// filesystem rooted at assets/, and the handler takes the base name of
	// what was asked for, so a traversal names a file that is not there
	// rather than one that is somewhere else.
	for path, want := range map[string]int{
		"/static/app.js":     http.StatusOK,
		"/static/style.css":  http.StatusOK,
		"/static/index.html": http.StatusNotFound,
		"/static/nothing.js": http.StatusNotFound,
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+listen+path, nil)
		r.Host = listen
		r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", path, w.Code, want)
		}
	}

	// A traversal must not reach anything, whether the mux normalises it into
	// a redirect or the handler refuses it outright. Either is fine; a body is
	// not.
	for _, path := range []string{
		"/static/..%2fassets.go",
		"/static/../../go.mod",
		"/static/%2e%2e%2f%2e%2e%2fgo.mod",
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+listen+path, nil)
		r.Host = listen
		r.AddCookie(&http.Cookie{Name: "corium_ui", Value: token(server, t)})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code == http.StatusOK {
			t.Errorf("GET %s served something: %s", path, w.Body)
		}
	}
}
