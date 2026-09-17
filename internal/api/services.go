package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Corium-OS/Corium/internal/systemd"
)

// handleServices lists the units this API knows about, and what they are doing.
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.systemd.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"services": statuses})
}

// handleRestart cycles a unit.
//
// Which units may be cycled is decided by the systemd package rather than
// here, because whether restarting something can destroy a node is a property
// of the unit. This handler's job is to turn its refusal into a status code.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	status, err := s.systemd.Restart(r.Context(), r.PathValue("unit"))

	switch {
	case err == nil:
	case errors.Is(err, systemd.ErrUnknownUnit):
		writeError(w, http.StatusNotFound, err.Error())

		return
	case errors.Is(err, systemd.ErrNotRestartable):
		// 403 rather than 404: the unit exists and is readable, and the caller
		// is entitled to know the refusal is about this unit rather than about
		// their certificate.
		writeError(w, http.StatusForbidden, err.Error())

		return
	default:
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, status)
}

// handleLogs streams a journal as newline-delimited JSON.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	options, err := logOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	// Everything that can be refused is refused here, before a single byte of
	// the response is written. Past this point the status is committed and a
	// bad request would arrive as an empty stream, which reads like a node
	// with nothing to say rather than like a mistake.
	if err := s.systemd.CheckLogOptions(options); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	// A followed stream outlives the server's WriteTimeout, which would
	// otherwise cut it off mid-record once the log went quiet for longer than
	// the timeout -- a failure that only shows up in the case the feature
	// exists for. Clearing the deadline is the supported way to say "this one
	// is long-lived"; the stream's own bound is systemd.MaxFollow.
	if options.Follow {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
			writeError(w, http.StatusInternalServerError,
				"this server cannot stream: "+err.Error())

			return
		}
	}

	// Content-Type before anything is written, and a 200 committed up front:
	// once records start flowing there is no way to change the status, so a
	// failure after this point ends the stream rather than reporting itself.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if err := s.systemd.Logs(r.Context(), options, w); err != nil {
		// The client has a partial stream and a non-zero exit is the only
		// signal left; the journal says the rest.
		logStreamFailed(r, err)
	}
}

// logStreamFailed records a stream that ended badly. It is separate only so
// that the handler reads as one thing.
func logStreamFailed(r *http.Request, err error) {
	slog.Warn("log stream ended early",
		"unit", r.URL.Query().Get("unit"), "from", r.RemoteAddr, "error", err)
}

func logOptions(r *http.Request) (systemd.LogOptions, error) {
	query := r.URL.Query()

	options := systemd.LogOptions{
		Unit:   query.Get("unit"),
		Since:  query.Get("since"),
		Follow: query.Get("follow") == "true",
	}

	if raw := query.Get("lines"); raw != "" {
		lines, err := strconv.Atoi(raw)
		if err != nil {
			return options, errors.New("lines: not a number")
		}

		if lines < 0 {
			return options, errors.New("lines: not a count")
		}

		options.Lines = lines
	}

	return options, nil
}
