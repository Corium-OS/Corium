package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Corium-OS/Corium/internal/upgrade"
)

// maxUpgradeBody caps the request. An image reference is a short string.
const maxUpgradeBody = 4 << 10

type stageRequest struct {
	Image string `json:"image"`
}

// stageEvent is one line of the staging stream. Exactly one field is set per
// line: a progress line while bootc pulls, then a single terminal record --
// staged with what is now waiting, or error with what went wrong.
type stageEvent struct {
	Progress string          `json:"progress,omitempty"`
	Staged   *upgrade.Staged `json:"staged,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// handleStage pulls an image and prepares the node to boot it, streaming bootc's
// progress as it goes.
//
// A pull is hundreds of megabytes and takes minutes, so this route does not
// answer at once the way the rest do. Two things follow from that. The write
// deadline is cleared, as handleDrain clears it, so the node's own pullTimeout
// is the only bound. And the response is a stream: once the pull has started
// there is a 200 and newline-delimited JSON, and the outcome -- success or
// failure -- is the last record rather than a status code, because the header
// is long gone by the time bootc finishes. The refusals that happen before the
// pull starts, a bad reference or an image the policy rejects, still answer with
// a status code, because nothing has been written yet.
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

	controller := http.NewResponseController(w)

	// A pull can legitimately take many minutes and the write deadline is 30s;
	// clearing it is the same decision handleDrain makes, and the pullTimeout is
	// the real bound.
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "this server cannot hold a long request open")

		return
	}

	// committed flips the first time a line is streamed. Before it, the response
	// header is still ours to set and a failure can carry a status code; after
	// it, the outcome has to travel in the stream.
	committed := false

	onProgress := func(line string) {
		if !committed {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)

			committed = true
		}

		writeStageEvent(w, controller, stageEvent{Progress: line})

		// Kept on the node too, at debug: the operator now sees these live, but
		// a stage worth investigating afterwards should still leave a trace.
		slog.Debug("stage progress", "line", line)
	}

	staged, err := s.upgrades.Stage(r.Context(), request.Image, onProgress)

	if !committed {
		// The pull never started, so the image was refused before any bytes
		// moved. These are the only outcomes that still carry a status code.
		switch {
		case err == nil:
			// Not reached in practice -- Stage announces the pull before it can
			// succeed -- but writing the result keeps this honest if that ever
			// changes.
			writeJSON(w, http.StatusOK, staged)
		case errors.Is(err, upgrade.ErrBadReference), errors.Is(err, upgrade.ErrNothingStaged):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, upgrade.ErrUnsigned):
			// 403 rather than 400: the reference is well formed and the node is
			// refusing it on policy, which is a different thing to fix.
			writeError(w, http.StatusForbidden, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}

		return
	}

	// The stream is open. The last record is the outcome.
	if err != nil {
		writeStageEvent(w, controller, stageEvent{Error: err.Error()})

		return
	}

	writeStageEvent(w, controller, stageEvent{Staged: staged})
}

// writeStageEvent writes one line of the staging stream and flushes it, so the
// operator sees it now rather than when the pull ends.
func writeStageEvent(w http.ResponseWriter, controller *http.ResponseController, event stageEvent) {
	encoded, err := json.Marshal(event)
	if err != nil {
		slog.Debug("encoding stage event", "error", err)

		return
	}

	if _, err := w.Write(append(encoded, '\n')); err != nil {
		// The client is gone. Nothing to do but stop: the pull is bound to the
		// request context and is cancelled with it.
		slog.Debug("writing stage event", "error", err)

		return
	}

	_ = controller.Flush()
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
	err := s.upgrades.Rollback(r.Context())

	switch {
	case err == nil:
	case errors.Is(err, upgrade.ErrNoRollback):
		// 409: nothing is wrong with the request, and a node that has only
		// ever booted one image has nowhere to go back to.
		writeError(w, http.StatusConflict, err.Error())

		return
	default:
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
