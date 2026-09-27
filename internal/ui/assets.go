package ui

import (
	"embed"
	"net/http"
	"path"
	"strings"
)

// files is the whole front end: three files, embedded, no build step.
//
// Decision 13 in AGENTS.md is about Go dependencies, but the reasoning carries:
// a framework here would mean a node_modules tree, a bundler, and built assets
// either committed or produced in CI, to render a page that shows a dozen
// fields and has five buttons. The page is hand-written HTML, CSS and
// JavaScript, and `mise run build` stays `go build`.
//
//go:embed assets/index.html assets/app.js assets/style.css
var files embed.FS

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	body, err := files.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "the dashboard is missing its page", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Nothing on this page loads anything from anywhere else, and saying so
	// costs one header. An injected <script src> or a beaconed fetch of what
	// the page is showing -- hostnames, digests, journal lines -- does not
	// leave the machine.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; "+
			"connect-src 'self'; img-src 'self'; form-action 'none'; base-uri 'none'")

	_, _ = w.Write(body)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("file"))

	body, err := files.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)

		return
	}

	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)

		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")

	// The body is one of the two files embedded above, selected by base name
	// from a fixed set. Nothing a request carries reaches it.
	_, _ = w.Write(body) //nolint:gosec // an embedded asset, not request data
}
