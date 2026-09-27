package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
	"github.com/Corium-OS/Corium/internal/lifecycle"
)

// swapKubernetesVersion moves a running node to another k0s version.
//
// Unlike the rest of the safe subset, this one cannot be handed to a reconciler
// Corium already delegates to. An add-on change is a file k0s reads at startup;
// a version change is the binary underneath the service, and k0s documents the
// only supported way to replace it: stop, swap, start. So this is a sequence the
// node performs on itself, and the ordering carries most of the design.
//
// The version is fetched before anything is taken out of service. A download is
// the step most likely to fail -- a registry that is unreachable, a signature
// that does not verify, a disk with no room -- and a node that has been drained
// and stopped before discovering any of those is a node that lost its workloads
// to find out something it could have learned first.
//
// There is deliberately no automatic rollback. If k0s does not come back, the
// node stops with the reason and stays cordoned rather than swapping the
// extension back: a minor upgrade may have migrated state on its way up, and
// k0s does not support moving back a minor at all (ADR 10). Reversing that
// automatically, on a node whose control plane is already unhappy, is how a
// recoverable problem becomes an unrecoverable one. An operator decides.
//
// Returns the version now running.
func (s *Server) swapKubernetesVersion(ctx context.Context, next *config.Config) (string, error) {
	window, err := k0s.ReadWindow(s.k0sWindowPath)
	if err != nil {
		return "", err
	}

	requested, err := k0s.ParseVersion(next.Kubernetes.Version)
	if err != nil {
		return "", fmt.Errorf("kubernetes.version: %w", err)
	}

	if err := window.Check(requested); err != nil {
		return "", err
	}

	// Measured against what is actually merged, not against what the recorded
	// configuration claims. Those disagree on a node whose extension stopped
	// matching after an OS rebase, and the skew rules apply to the binary that
	// is really there.
	active, err := k0s.ActiveVersion(k0s.ExtensionRelease, window.Floor)
	if err != nil {
		return "", err
	}

	if active.Raw == requested.Raw {
		return active.Raw, nil
	}

	if err := k0s.CheckSkew(active, requested); err != nil {
		return "", err
	}

	// Before anything is stopped. See the note above.
	if err := k0s.Fetch(ctx, requested, next.Kubernetes.Mirror); err != nil {
		return "", err
	}

	drained, err := s.drainForSwap(ctx)
	if err != nil {
		return "", err
	}

	service := k0s.ServiceName(next.Role)

	slog.Warn("swapping the k0s version",
		"from", active, "to", requested, "service", service)

	if err := k0s.Stop(ctx, next.Role); err != nil {
		return "", fmt.Errorf("stopping %s: %w", service, err)
	}

	if err := k0s.Activate(ctx, requested); err != nil {
		// Nothing has started yet and the previous extension is still in the
		// store, so saying so is the whole of the recovery advice.
		return "", fmt.Errorf(
			"%w; %s is stopped and the node is still on %s", err, service, active)
	}

	if err := k0s.ClearStaged(); err != nil {
		return "", err
	}

	if err := k0s.Start(ctx, next.Role); err != nil {
		return "", fmt.Errorf(
			"starting %s on k0s %s: %w; the node is cordoned and the previous "+
				"version is still in %s",
			service, requested, err, k0s.ExtensionStore)
	}

	// Confirmed rather than assumed: an extension that does not match is not an
	// error systemd reports, it simply does not merge, and the node would come
	// back on its floor with the apply reporting success.
	running, err := k0s.ActiveVersion(k0s.ExtensionRelease, window.Floor)
	if err != nil {
		return "", err
	}

	if running.Raw != requested.Raw {
		return "", fmt.Errorf(
			"k0s %s was installed but %s is merged; the extension does not match "+
				"this image, and the node is cordoned",
			requested, running)
	}

	if drained {
		if err := s.lifecycle.Uncordon(ctx); err != nil {
			// The swap worked; only the return to service did not. Reported
			// rather than swallowed, but named precisely, because "run cctl
			// uncordon" is a much smaller problem than the message would
			// otherwise suggest.
			return running.Raw, fmt.Errorf(
				"k0s %s is running, but the node could not be returned to "+
					"service: %w; uncordon it with `cctl uncordon`",
				running, err)
		}
	}

	slog.Warn("k0s version swapped", "from", active, "to", running)

	return running.Raw, nil
}

// drainForSwap takes the node out of service, and reports whether it did.
//
// A node that cannot reach its own Node object is not an error here. Only a
// controller holds admin credentials locally, so a worker asked to change
// version legitimately cannot drain itself -- the same situation the upgrade
// path already handles by going ahead undrained. Refusing instead would make
// the feature controller-only for no benefit: the workloads are stopped either
// way when k0s stops, and the difference is whether the cluster was told first.
func (s *Server) drainForSwap(ctx context.Context) (bool, error) {
	if !s.lifecycle.CanReachCluster(ctx) {
		slog.Warn("changing the k0s version without draining: " +
			"this node cannot reach the cluster to evict its own workloads")

		return false, nil
	}

	if err := s.lifecycle.Drain(ctx, lifecycle.DefaultDrainTimeout); err != nil {
		// A refused drain cancels the swap rather than forcing it, which is the
		// rule corium-upgrade-apply already follows: a pod disruption budget
		// saying no is the system working, and nothing has been touched yet.
		if errors.Is(err, lifecycle.ErrNoClusterAccess) {
			return false, nil
		}

		return false, fmt.Errorf(
			"draining the node before changing the k0s version: %w; "+
				"nothing was changed", err)
	}

	return true, nil
}

// maxKubernetesBody caps the request. A version and a mirror are short strings.
const maxKubernetesBody = 4 << 10

type kubernetesRequest struct {
	Version string `json:"version"`
	Mirror  string `json:"mirror,omitempty"`
}

// handleKubernetesVersion moves this node to another k0s version.
//
// A bounded verb rather than a whole document, and that is the point of it. The
// general day-two path is `cctl apply`, which sends a configuration and lets the
// node diff it -- but a rollout across a cluster would then have to hold every
// node's document and send each one back correctly, and the failure mode of
// getting that wrong is a node rebuilt with its neighbour's identity. This
// endpoint cannot make that mistake: it is not able to express anything except
// the version.
//
// It is the fallback ADR 8 names for exactly this reason -- a verb that only
// changes one thing never has to judge whether the rest of a document was safe.
func (s *Server) handleKubernetesVersion(w http.ResponseWriter, r *http.Request) {
	// The swap downloads a quarter of a gigabyte and then drains, either of
	// which outlasts the server's write deadline. Cleared as handleDrain
	// clears it; the bound is the operation's own.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError,
			"this server cannot hold a long request open")

		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxKubernetesBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request kubernetesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	if request.Version == "" {
		writeError(w, http.StatusBadRequest, "version is required")

		return
	}

	// Diffed against what the node recorded applying, for the reason ADR 8
	// gives: a file in /etc may have been hand-edited into something the node
	// never acted on.
	baseline, err := s.baselineConfig()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	next := *baseline
	next.Kubernetes.Version = request.Version

	if request.Mirror != "" {
		next.Kubernetes.Mirror = request.Mirror
	}

	// Validated before anything moves, so a malformed mirror is a 400 rather
	// than a node that drains itself and then cannot pull.
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	running, err := s.swapKubernetesVersion(r.Context(), &next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	status := "changed"
	if baseline.Kubernetes.Version == request.Version {
		status = "unchanged"
	}

	// Recorded only once the version is actually running, and re-rendered from
	// the parsed configuration rather than patched as text. That loses an
	// operator's comments and ordering in /etc, which is a real cost and the
	// reason this is written the same way `cctl apply` writes its document: a
	// node whose recorded configuration disagrees with the version it is
	// running would send the next apply chasing a change that already happened.
	if status == "changed" {
		document, err := next.Document()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())

			return
		}

		for _, path := range []string{s.configPath, s.appliedPath} {
			if err := writeConfigDocument(path, document); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())

				return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"kubernetes": running,
	})
}
