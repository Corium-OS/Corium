package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// immutableChangeError reports a day-two apply that would change a field only a
// reset may change. It names the offending fields so the operator sees which
// part of their document is the problem, not just that there was one.
type immutableChangeError struct{ fields []string }

func (e *immutableChangeError) Error() string {
	return fmt.Sprintf(
		"these fields cannot change on a node that has already bootstrapped: %s; "+
			"use cctl reset to return it to maintenance mode and re-bootstrap",
		strings.Join(e.fields, ", "))
}

// errNoBaseline reports a bootstrapped node with no recorded configuration to
// diff a proposal against -- one that predates day-two apply, or whose state
// was cleared. Reconciling blind would risk mistaking an unapplied edit in
// /etc for the running configuration, so the node refuses and asks for a reset
// rather than guessing.
var errNoBaseline = errors.New(
	"this node has no recorded configuration to reconcile against; " +
		"use cctl reset to return it to maintenance mode and re-bootstrap")

// reconcileResult is what a successful day-two apply reports back.
type reconcileResult struct {
	// changed names the safe fields that were re-applied, e.g. "addons".
	changed []string

	// restarted is the unit bounced to pick the change up, if any.
	restarted string
}

// reconcile re-applies the safe subset of a document to a node that has already
// bootstrapped, and refuses anything outside it.
//
// It is what turns `cctl apply` on a running node from a flat refusal into the
// gated re-application ADR 8 describes: the add-on set is handed back to k0s to
// reconcile, and every field that defines what the node is refuses to move.
func (s *Server) reconcile(ctx context.Context, next *config.Config, document []byte) (reconcileResult, error) {
	baseline, err := s.baselineConfig()
	if err != nil {
		return reconcileResult{}, err
	}

	plan := config.PlanReconcile(baseline, next)

	if !plan.Reconcilable() {
		return reconcileResult{}, &immutableChangeError{fields: plan.Immutable}
	}

	// A document that matches what the node is already running is a no-op, not
	// an action: nothing is written and nothing is restarted, so a second
	// identical apply cannot bounce the control plane.
	if plan.Empty() {
		return reconcileResult{}, nil
	}

	var result reconcileResult

	if plan.Addons {
		result.changed = append(result.changed, "addons")

		// Add-ons render into the k0s configuration as Helm extensions, so they
		// are a control-plane concern: on a worker the charts are declared by
		// the controllers, and rewriting this node's copy changes nothing it
		// runs. Only a controller re-renders and restarts.
		if next.Role.IsController() {
			rendered, err := k0s.Render(next)
			if err != nil {
				return reconcileResult{}, fmt.Errorf("rendering k0s configuration: %w", err)
			}

			// Reusing writeConfigDocument for its atomic 0600 write: a k0s.yaml
			// is not a login banner, and a half-written one is worse than none.
			if err := writeConfigDocument(s.k0sConfigPath, rendered); err != nil {
				return reconcileResult{}, err
			}

			// k0s reads its configuration at startup and reconciles its Helm
			// chart set to match, so a restart is how a changed add-on set is
			// picked up. On a single node this is a brief control-plane pause;
			// the kubelet and its pods keep running. See ADR 8.
			service := k0s.ServiceName(next.Role)
			if _, err := s.systemd.Restart(ctx, service); err != nil {
				return reconcileResult{}, fmt.Errorf("restarting %s: %w", service, err)
			}

			result.restarted = service
		}
	}

	// Persisted last, once the change is live: the document the node acted on
	// becomes both the file in /etc and the baseline the next apply diffs
	// against, so the two never drift from what is actually running.
	if err := writeConfigDocument(s.configPath, document); err != nil {
		return reconcileResult{}, err
	}

	if err := writeConfigDocument(s.appliedPath, document); err != nil {
		return reconcileResult{}, err
	}

	slog.Warn("configuration reconciled",
		"changed", result.changed, "restarted", result.restarted)

	return result, nil
}

// baselineConfig is the configuration a day-two apply is measured against: what
// the node recorded applying, falling back to the document in /etc for a node
// that bootstrapped before the baseline was recorded. A node with neither has
// nothing safe to reason from, and says so.
func (s *Server) baselineConfig() (*config.Config, error) {
	for _, path := range []string{s.appliedPath, s.configPath} {
		data, err := os.ReadFile(path) //nolint:gosec // G304: server-owned paths, not caller input
		if err != nil {
			continue
		}

		cfg, err := config.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("reading the recorded configuration at %s: %w", path, err)
		}

		return cfg, nil
	}

	return nil, errNoBaseline
}
