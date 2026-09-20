package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/token"
)

// handleJoinToken mints a k0s join token on this controller.
//
// A worker token lets a machine join the cluster, and a controller token is a
// cluster-admin credential; either is a secret. This is the one call besides
// kubeconfig that hands one out, so it is admin, the minting is logged without
// the token itself, and the body is served as text nothing would cache or
// render.
//
// The token is returned in the clear on purpose: `cctl worker-config` embeds it
// inline in a node's join.token. Short expiries and revocation
// (`k0s token invalidate`) are the defence, not secrecy of this response.
func (s *Server) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	node := s.inspector.Collect(r.Context())

	if !config.Role(node.Role).IsController() {
		// 409, not 500: nothing is wrong with the request. A worker simply has
		// nowhere to mint a token from.
		writeError(w, http.StatusConflict, token.ErrNoControlPlane.Error())

		return
	}

	role := r.URL.Query().Get("role")
	if role == "" {
		role = string(config.RoleWorker)
	}

	// Only the two roles k0s can mint, and only ever exactly what was asked for:
	// a typo must fail here rather than reach k0s and produce a puzzling error.
	switch config.Role(role) {
	case config.RoleWorker, config.RoleController:
	default:
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("role: must be worker or controller, got %q", role))

		return
	}

	expiry := r.URL.Query().Get("expiry")
	if expiry == "" {
		expiry = token.DefaultExpiry.String()
	}

	if d, err := time.ParseDuration(expiry); err != nil || d <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"expiry: %q is not a positive duration; use a form like 1h", expiry))

		return
	}

	raw, err := s.token.Create(r.Context(), role, expiry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	slog.Warn("minted a join token",
		"role", role, "expiry", expiry, "to", r.RemoteAddr)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The body is a token, never markup. nosniff says so to anything that would
	// otherwise guess.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	//nolint:gosec // G705: a k0s-minted token, not markup, served as text/plain
	// with nosniff; nothing written here is caller-controlled HTML.
	if _, err := w.Write(raw); err != nil {
		slog.Debug("writing the join token", "error", err)
	}
}
