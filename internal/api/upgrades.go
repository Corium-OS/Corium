package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Corium-OS/Corium/internal/upgrade"
)

// maxUpgradeBody caps the request. An image reference is a short string.
const maxUpgradeBody = 4 << 10

type stageRequest struct {
	Image string `json:"image"`
}

// handleStage pulls an image and prepares the node to boot it.
func (s *Server) handleStage(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUpgradeBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request stageRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	staged, err := s.upgrades.Stage(r.Context(), request.Image)

	switch {
	case err == nil:
	case errors.Is(err, upgrade.ErrBadReference), errors.Is(err, upgrade.ErrNothingStaged):
		writeError(w, http.StatusBadRequest, err.Error())

		return
	case errors.Is(err, upgrade.ErrUnsigned):
		// 403 rather than 400: the reference is well formed and the node is
		// refusing it on policy, which is a different thing to fix.
		writeError(w, http.StatusForbidden, err.Error())

		return
	default:
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, staged)
}

// handleApply drains the node and reboots it into whatever is staged.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	err := s.upgrades.Apply(r.Context())

	switch {
	case err == nil:
	case errors.Is(err, upgrade.ErrNothingStaged):
		// 409: nothing is wrong with the request, the node simply has nothing
		// to apply.
		writeError(w, http.StatusConflict, err.Error())

		return
	default:
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// 202, not 200. The node has accepted the job and is about to drain and
	// reboot; it has not finished, and it will not be here to say so.
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "applying",
		"note": "the node is draining and will reboot; watch " +
			upgrade.ApplyUnit + " until the connection drops",
	})
}

// handleRollback marks the previous deployment as the next to boot.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if err := s.upgrades.Rollback(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// Deliberately no reboot: the node comes back on the previous image when
	// the operator chooses, not when this call returns.
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "rolled back",
		"note":   "the previous image boots next; reboot when you are ready",
	})
}
