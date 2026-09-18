package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Corium-OS/Corium/internal/access"
)

// maxAccessBody caps an add-key request. An SSH public key line is a few
// hundred bytes; this leaves room for a generous comment and no room for a
// request body to be a memory budget.
const maxAccessBody = 8 << 10

// addSSHKeyRequest is what `cctl access ssh add` sends.
type addSSHKeyRequest struct {
	// User is the account the key is trusted for. It must already exist: this
	// API adds keys, it does not create users. See ADR 5.
	User string `json:"user"`

	// Key is one authorized_keys line.
	Key string `json:"key"`
}

// handleAddSSHKey trusts an SSH public key for an existing user.
//
// admin, and logged loudly, because it grants a shell -- the one thing that
// steps outside every guard rail the rest of this API keeps.
func (s *Server) handleAddSSHKey(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAccessBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request addSSHKeyRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	key, err := s.access.Add(r.Context(), request.User, request.Key)

	switch {
	case err == nil:

	case errors.Is(err, access.ErrUnknownUser):
		// 409, not 404: the request is well-formed, the node simply has no such
		// account -- and the way to make one is cloud-init, which the message
		// says.
		writeError(w, http.StatusConflict, err.Error())

		return

	default:
		// An empty or unparseable key. The message says what was wrong and never
		// echoes the key back.
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	slog.Warn("ssh key trusted",
		"user", key.User, "fingerprint", key.Fingerprint, "requestedBy", r.RemoteAddr)

	writeJSON(w, http.StatusOK, key)
}

// handleListSSHKeys reports the keys the node trusts, by fingerprint.
func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.access.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// An empty list is [] and not null, so a client can range over the reply
	// without a nil check.
	if keys == nil {
		keys = []access.Key{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// handleRevokeSSHKey stops the node trusting a key.
//
// The key is named by a fingerprint query parameter rather than a path segment
// because an SSH SHA-256 fingerprint contains '/', which a path segment cannot
// carry.
func (s *Server) handleRevokeSSHKey(w http.ResponseWriter, r *http.Request) {
	fingerprint := r.URL.Query().Get("fingerprint")
	if fingerprint == "" {
		writeError(w, http.StatusBadRequest,
			"name the key to revoke with ?fingerprint=SHA256:...; "+
				"`cctl access ssh list` shows the fingerprints")

		return
	}

	removed, err := s.access.Revoke(r.Context(), fingerprint)

	switch {
	case err == nil:

	case errors.Is(err, access.ErrNoSuchKey):
		writeError(w, http.StatusNotFound, err.Error())

		return

	default:
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	for _, key := range removed {
		slog.Warn("ssh key revoked",
			"user", key.User, "fingerprint", key.Fingerprint, "requestedBy", r.RemoteAddr)
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "removed": removed})
}
