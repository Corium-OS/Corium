package bootstrap

import (
	"errors"
	"log/slog"

	"github.com/Corium-OS/Corium/internal/config"
)

// errUnclaimed stops a node that has asked to be claimed by an operator before
// it joins anything.
//
// This is the whole of maintenance mode that exists today. The daemon that
// would print a pairing code and release the bootstrap on enrolment is not
// written yet, so a node configured for it stops here with an explanation
// instead of joining a cluster unclaimed -- which is precisely the state
// ADR 4 exists to make unreachable.
var errUnclaimed = errors.New(
	"api.enabled is set with no operator CA, which asks for maintenance mode: " +
		"the node must be claimed with `cctl enroll` before it joins a cluster. " +
		"corium-apid does not exist yet, so there is nothing to enrol against -- " +
		"set api.operatorCA, or remove the api block to bootstrap without a " +
		"management API. See docs/adr/0004-management-api.md")

// gateOnEnrolment decides whether this node may proceed to bootstrap.
//
// It runs after validation and before anything is mutated, because the answer
// for an unclaimed node is that nothing should be mutated at all.
func gateOnEnrolment(cfg *config.Config) error {
	switch cfg.API.Mode() {
	case config.APIModeDisabled:
		return nil

	case config.APIModeConfigured:
		// Accepted and recorded, but nothing serves it yet. Saying so in the
		// journal is the difference between a key that is not implemented and
		// a key that silently does nothing, and an operator who wrote it is
		// entitled to know which one they got.
		slog.Warn("management API configured, but corium-apid is not implemented yet",
			"node", "bootstrapping without a management API")

		return nil

	case config.APIModeMaintenance:
		return errUnclaimed

	default:
		// Mode returns one of the three above. A fourth means someone added a
		// mode and did not come back here, which is worth failing over rather
		// than defaulting to "carry on".
		return errors.New("unknown api mode")
	}
}
