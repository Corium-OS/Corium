package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Corium-OS/Corium/internal/lifecycle"
)

// maxLifecycleBody caps a request whose largest field is a hostname.
const maxLifecycleBody = 4 << 10

// handleCordon takes the node out of scheduling, or puts it back.
func (s *Server) handleCordon(w http.ResponseWriter, r *http.Request) {
	undo := r.URL.Query().Get("undo") == "true"

	var err error
	if undo {
		err = s.lifecycle.Uncordon(r.Context())
	} else {
		err = s.lifecycle.Cordon(r.Context())
	}

	if err != nil {
		s.writeLifecycleError(w, err)

		return
	}

	state := "cordoned"
	if undo {
		state = "uncordoned"
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": state})
}

// handleDrain evicts the node's workloads.
func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	// A drain can legitimately take minutes, and the server's write deadline
	// is shorter than that. Clearing it here is the same decision as for a
	// followed log: the bound is the drain's own timeout.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "this server cannot hold a long request open")

		return
	}

	if err := s.lifecycle.Drain(r.Context(), lifecycle.DefaultDrainTimeout); err != nil {
		s.writeLifecycleError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "drained"})
}

// handleReboot and handleShutdown take the machine away.
func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	s.goingAway(w, r, "rebooting", s.lifecycle.Reboot)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	s.goingAway(w, r, "shutting down", s.lifecycle.Shutdown)
}

// goingAway answers before doing the thing that ends the conversation.
//
// The response is written and flushed first, because the alternative is a
// client that cannot tell "the node refused" from "the node obeyed" -- both
// look like a connection that died.
func (s *Server) goingAway(
	w http.ResponseWriter, r *http.Request, status string, act func(context.Context) error,
) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": status})

	if err := http.NewResponseController(w).Flush(); err != nil {
		slog.Warn("could not flush before acting", "status", status, "error", err)
	}

	// Detached from the request: the connection is about to be cut by the very
	// thing being asked for, and a cancelled context must not cancel it.
	if err := act(context.WithoutCancel(r.Context())); err != nil {
		slog.Error("lifecycle action failed after answering", "status", status, "error", err)
	}
}

// resetRequest is what `cctl reset` sends.
type resetRequest struct {
	// Confirm must be the node's own hostname. A reset is the one call that
	// cannot be undone by any other call, and the address in a shell's history
	// is a poor guard against it landing on the wrong machine.
	Confirm string `json:"confirm"`
}

// handleReset takes the node out of its cluster and gives it back its
// unclaimed state.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLifecycleBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request resetRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	node, err := s.lifecycle.Node()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	if request.Confirm != node {
		writeError(w, http.StatusBadRequest,
			"this will erase "+node+" and take it out of its cluster; "+
				"send its name in `confirm` to mean it")

		return
	}

	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "this server cannot hold a long request open")

		return
	}

	slog.Warn("resetting this node", "node", node, "requestedBy", r.RemoteAddr)

	if err := s.reset(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "reset",
		"note": "the node has left its cluster and forgotten its owner; " +
			"it is rebooting and will come back unclaimed",
	})

	if err := http.NewResponseController(w).Flush(); err != nil {
		slog.Warn("could not flush before rebooting", "error", err)
	}

	if err := s.lifecycle.Reboot(context.WithoutCancel(r.Context())); err != nil {
		slog.Error("reset finished but the node did not reboot", "error", err)
	}
}

// reset runs the sequence in the only order that is safe.
//
// Leaving the cluster comes first. A node that dropped its enrolment and then
// failed to leave would be a cluster member nobody owns -- the one state this
// design exists to make unreachable -- whereas failing the other way round
// leaves a machine that is out of the cluster and still owned, which is merely
// untidy and is reported.
func (s *Server) reset(ctx context.Context) error {
	// Best effort: a node whose cluster is already gone must still be able to
	// reset, so a failed drain does not stop it.
	if err := s.lifecycle.Drain(ctx, lifecycle.DefaultDrainTimeout); err != nil {
		slog.Warn("could not drain before resetting; continuing", "error", err)
	}

	if err := s.lifecycle.LeaveCluster(ctx); err != nil {
		return err
	}

	if err := s.lifecycle.ForgetBootstrap(); err != nil {
		return err
	}

	// The keys the API was trusting leave with the enrolment that authorised
	// them. Best effort and loud: the keys live under Corium's own state, which
	// a reset is erasing anyway, and refusing a reset that otherwise succeeded
	// over a leftover key file would be the worse answer. See ADR 5.
	if err := s.access.Purge(); err != nil {
		slog.Warn("could not remove trusted SSH keys while resetting; continuing", "error", err)
	}

	// Last, and only now: the node stops being owned. Everything above has
	// already happened, so there is no way to end up unclaimed and still in a
	// cluster.
	return s.store.Forget()
}

// writeLifecycleError turns this package's refusals into status codes.
func (s *Server) writeLifecycleError(w http.ResponseWriter, err error) {
	if errors.Is(err, lifecycle.ErrNoClusterAccess) {
		// 409 rather than 403: nothing is wrong with the caller, this node
		// simply cannot do it. A worker holds kubelet credentials, which
		// cannot evict pods.
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	writeError(w, http.StatusInternalServerError, err.Error())
}
