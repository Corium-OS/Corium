package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Corium-OS/Corium/internal/config"
)

// ConfigPath is where an applied document lands.
//
// It is first in the source chain, ahead of cloud-init, and it is there for
// the machines that have no datasource at all -- which is the same set of
// machines this endpoint exists for. Writing it means an applied document
// beats the one the node booted with, which is the only behaviour that makes
// sense: an operator who has just sent a configuration did not send it to be
// ignored in favour of the metadata the machine happened to start with.
const ConfigPath = "/etc/corium/config.yaml"

// maxConfigBody caps the request. A node is meant to be describable in twenty
// lines of YAML; this leaves room for an escape hatch or two and stops a
// request body from being a memory budget.
const maxConfigBody = 1 << 20

// ErrAlreadyBootstrapped reports a node that is past the point where its
// configuration can still be chosen.
var ErrAlreadyBootstrapped = errors.New(
	"this node has already bootstrapped, so its configuration is no longer a " +
		"question it can answer; use cctl reset to return it to maintenance mode")

// configRequest carries a node's corium: document.
type configRequest struct {
	// Document is YAML, in either shape config.Parse accepts: a cloud-config
	// with a corium: key, or the block on its own.
	Document string `json:"document"`
}

// handleApplyConfig writes the configuration this node will bootstrap with.
//
// The one rule that makes this safe is checked here rather than left to the
// caller: a node that has bootstrapped is refused. See ADR 4, "Configuration,
// before a node is a node" -- rewriting the role or cluster of a machine that
// is already running Kubernetes would make its configuration and its behaviour
// two different facts, which is the thing decisions 6 and 7 exist to prevent.
func (s *Server) handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request configRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	cfg, err := config.Parse([]byte(request.Document))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("that document is not usable: %v", err))

		return
	}

	// Validated here and not only in cctl. A node that accepted over the wire
	// what its own bootstrap would refuse would be a node holding a document
	// guaranteed to fail on the next boot, with nobody there to read the
	// failure.
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	// Asked of the node itself rather than of the store, so that a node
	// bootstrapped by a release that predates node.json is still recognised --
	// the same signal cctl upgrade's health gate leans on.
	//
	// A bootstrapped node no longer refuses outright: it re-applies the safe
	// subset of the document and refuses only the fields that define what it is.
	// See ADR 8.
	if s.inspector.Collect(r.Context()).Bootstrapped {
		s.applyToRunningNode(w, r, cfg, []byte(request.Document))

		return
	}

	if err := writeConfigDocument(s.configPath, []byte(request.Document)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	s.recordApplied()

	slog.Warn("configuration applied",
		"path", s.configPath,
		"role", cfg.Role,
		"requestedBy", r.RemoteAddr)

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"path":   s.configPath,
		"role":   string(cfg.Role),
		"api":    cfg.API.Mode() != config.APIModeDisabled,
	})
}

// applyToRunningNode reconciles the safe subset of a document into a node that
// has already bootstrapped, and turns the outcome into a response.
//
// A change the node cannot make day-two -- an identity, cluster or disk field,
// or a node with nothing recorded to diff against -- is a 409: nothing is wrong
// with the request, the node's state simply does not allow it without a reset,
// which is the same answer this endpoint has always given here, now with the
// reason and the field.
func (s *Server) applyToRunningNode(w http.ResponseWriter, r *http.Request, cfg *config.Config, document []byte) {
	result, err := s.reconcile(r.Context(), cfg, document)
	if err != nil {
		var refused *refusedApplyError

		switch {
		case errors.As(err, &refused), errors.Is(err, errNoBaseline):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}

		return
	}

	// "reconciled" when something in the safe subset was re-applied, "unchanged"
	// when the document matched what the node was already running: a second
	// identical apply is a no-op, not an error.
	status := "reconciled"
	if len(result.changed) == 0 {
		status = "unchanged"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"path":       s.configPath,
		"role":       string(cfg.Role),
		"api":        cfg.API.Mode() != config.APIModeDisabled,
		"reconciled": result.changed,
		"restarted":  result.restarted,
	})
}

// applyDocumentDuringEnrolment turns a document sent with an enrolment into
// the step that runs just before the node is claimed.
//
// It returns nil when there is no document, which is the ordinary enrolment
// and must stay exactly as cheap as it was.
func (s *Server) applyDocumentDuringEnrolment(r *http.Request, document string) func() error {
	if document == "" {
		return nil
	}

	return func() error {
		// The same refusal as the standalone endpoint. Enrolment is not a way
		// around it: a node that has bootstrapped and been reset is unenrolled
		// again, but one that has bootstrapped and not been reset must not have
		// its configuration rewritten by whoever claims it next.
		if s.inspector.Collect(r.Context()).Bootstrapped {
			return ErrAlreadyBootstrapped
		}

		cfg, err := config.Parse([]byte(document))
		if err != nil {
			return fmt.Errorf("the configuration sent with this enrolment is not usable: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("the configuration sent with this enrolment is invalid: %w", err)
		}

		if err := writeConfigDocument(s.configPath, []byte(document)); err != nil {
			return err
		}

		s.recordApplied()

		slog.Info("configuration applied as part of the enrolment",
			"path", s.configPath, "role", cfg.Role)

		return nil
	}
}

// writeConfigDocument replaces the machine-local configuration atomically.
//
// 0600, unlike most of /etc: a corium: document may carry a join token or a
// VRRP password, and the whole argument for resolving those from a SecretSource
// is that they should not be readable by everything on the machine. Writing
// them world-readable here would give that away at the last step.
func writeConfigDocument(path string, document []byte) error {
	directory := filepath.Dir(path)

	if err := os.MkdirAll(directory, 0o755); err != nil { //nolint:gosec // G301: /etc/corium is readable
		return fmt.Errorf("creating %s: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".config.yaml.*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", directory, err)
	}

	defer func() { _ = os.Remove(temporary.Name()) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()

		return err
	}

	if _, err := temporary.Write(document); err != nil {
		_ = temporary.Close()

		return err
	}

	// Flushed before the rename, so that a machine losing power here is left
	// with either the old document or the new one, and never with a file whose
	// contents arrive after its name does.
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()

		return err
	}

	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(temporary.Name(), path)
}

// recordApplied tells a held bootstrap that its operator has answered.
//
// Best effort in the sense that it cannot fail the request -- the document is
// already written, and refusing an apply that succeeded would be a worse
// answer than a node that keeps waiting -- but loud, because a node that keeps
// waiting is exactly what an operator would then be looking at.
func (s *Server) recordApplied() {
	if s.sessionDir == "" {
		return
	}

	if err := os.MkdirAll(string(s.sessionDir), 0o700); err != nil {
		slog.Warn("could not record that a configuration was applied", "error", err)

		return
	}

	path := filepath.Join(string(s.sessionDir), AppliedMarker)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		slog.Warn("could not record that a configuration was applied",
			"path", path, "error", err)
	}
}
