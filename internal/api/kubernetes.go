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

// ErrSwapInProgress reports a version change asked for while one is running.
var ErrSwapInProgress = errors.New(
	"a k0s version change is already in progress on this node; " +
		"watch its journal and retry once it has finished")

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
	// A retried request must not start a second swap under one its client
	// abandoned.
	if !s.swapping.TryLock() {
		return "", ErrSwapInProgress
	}
	defer s.swapping.Unlock()

	plan, err := s.checkKubernetesVersion(next)
	if err != nil {
		return "", err
	}

	active, requested := plan.active, plan.requested

	if active.Raw == requested.Raw {
		return active.Raw, nil
	}

	// Before anything is stopped. See the note above.
	if err := k0s.Fetch(ctx, requested, next.Kubernetes.Mirror); err != nil {
		return "", err
	}

	return s.commitSwap(ctx, plan, next.Role)
}

// swapTimeout bounds a committed swap. It sits above the sum of its steps' own
// bounds, so it only cuts a step that is genuinely hung.
const swapTimeout = 30 * time.Minute

// commitSwap is the part of a swap that takes the node out of service.
//
// Detached from the caller: a client that leaves after the drain started would
// otherwise strand the node cordoned, or with k0s stopped.
func (s *Server) commitSwap(
	ctx context.Context, plan kubernetesPlan, role config.Role,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	active, requested, window := plan.active, plan.requested, plan.window

	drained, err := s.drainForSwap(ctx)
	if err != nil {
		return "", err
	}

	service := k0s.ServiceName(role)

	slog.Warn("swapping the k0s version",
		"from", active, "to", requested, "service", service)

	if err := k0s.Stop(ctx, role); err != nil {
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

	if err := k0s.Start(ctx, role); err != nil {
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
		// Wait for the control plane before asking it for anything. On a
		// controller -- and every single-node cluster is one -- the API server
		// this node needs in order to uncordon itself is the API server it just
		// restarted, and it is not up the instant systemd reports the unit
		// started.
		//
		// Without this the uncordon fails on a node that is otherwise perfectly
		// healthy, and it fails *misleadingly*: the check underneath reports
		// "only a controller holds cluster admin credentials, and this node is
		// not one", which is untrue and sends an operator looking at roles
		// instead of at timing. Found by the end-to-end test, which is the only
		// place a real control plane is restarted.
		if err := s.waitForClusterAccess(ctx); err != nil {
			return running.Raw, fmt.Errorf(
				"k0s %s is running, but %w; uncordon it with `cctl uncordon`",
				running, err)
		}

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

// kubernetesPlan is what a version change would do, decided without doing it.
type kubernetesPlan struct {
	window    k0s.Window
	active    k0s.Version
	requested k0s.Version
}

// checkKubernetesVersion holds every refusal a swap can reach without a
// registry, a cluster or systemd, so a rollout can ask it of every node before
// moving the first.
func (s *Server) checkKubernetesVersion(next *config.Config) (kubernetesPlan, error) {
	window, err := k0s.ReadWindow(s.k0sWindowPath)
	if err != nil {
		return kubernetesPlan{}, err
	}

	requested, err := k0s.ParseVersion(next.Kubernetes.Version)
	if err != nil {
		return kubernetesPlan{}, fmt.Errorf("kubernetes.version: %w", err)
	}

	if err := window.Check(requested); err != nil {
		return kubernetesPlan{}, err
	}

	// Measured against what is actually merged, not against what the recorded
	// configuration claims. Those disagree on a node whose extension stopped
	// matching after an OS rebase, and the skew rules apply to the binary that
	// is really there.
	active, err := k0s.ActiveVersion(k0s.ExtensionRelease, window.Floor)
	if err != nil {
		return kubernetesPlan{}, err
	}

	if active.Raw != requested.Raw {
		if err := k0s.CheckSkew(active, requested); err != nil {
			return kubernetesPlan{}, err
		}
	}

	return kubernetesPlan{window: window, active: active, requested: requested}, nil
}

// clusterSettleTimeout bounds how long a restarted control plane is given to
// answer before the node gives up on returning itself to service. Generous
// because the cost of being wrong is asymmetric: a node left cordoned needs a
// person, while waiting another minute costs nothing.
const clusterSettleTimeout = 5 * time.Minute

// clusterPollInterval is how often it asks.
const clusterPollInterval = 5 * time.Second

// waitForClusterAccess blocks until this node can act on its own Node object.
//
// It exists because a version change restarts the very control plane the node
// then needs in order to put itself back into service.
func (s *Server) waitForClusterAccess(ctx context.Context) error {
	deadline := time.Now().Add(clusterSettleTimeout)

	ticker := time.NewTicker(clusterPollInterval)
	defer ticker.Stop()

	for {
		if s.lifecycle.CanReachCluster(ctx) {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"its control plane did not answer within %s, so the node could "+
					"not be returned to service", clusterSettleTimeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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

	baseline, next, ok := s.readKubernetesRequest(w, r)
	if !ok {
		return
	}

	running, err := s.swapKubernetesVersion(r.Context(), next)
	if errors.Is(err, ErrSwapInProgress) {
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	status := "changed"
	if baseline.Kubernetes.Version == next.Kubernetes.Version {
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

// handleKubernetesCheck answers whether this node would accept a version,
// changing nothing.
//
// A route of its own rather than a flag on POST /v1/kubernetes: a node older
// than the flag would ignore it and perform the swap.
func (s *Server) handleKubernetesCheck(w http.ResponseWriter, r *http.Request) {
	_, next, ok := s.readKubernetesRequest(w, r)
	if !ok {
		return
	}

	plan, err := s.checkKubernetesVersion(next)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	status := "would-change"
	if plan.active.Raw == plan.requested.Raw {
		status = "unchanged"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"kubernetes": plan.active.Raw,
	})
}

// readKubernetesRequest builds the configuration a version request would
// leave the node with, answering the request itself when it is unusable.
func (s *Server) readKubernetesRequest(
	w http.ResponseWriter, r *http.Request,
) (*config.Config, *config.Config, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxKubernetesBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return nil, nil, false
	}

	var request kubernetesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return nil, nil, false
	}

	if request.Version == "" {
		writeError(w, http.StatusBadRequest, "version is required")

		return nil, nil, false
	}

	// Diffed against what the node recorded applying, for the reason ADR 8
	// gives: a file in /etc may have been hand-edited into something the node
	// never acted on.
	baseline, err := s.baselineConfig()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())

		return nil, nil, false
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

		return nil, nil, false
	}

	return baseline, &next, true
}
