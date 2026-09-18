package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/config"
)

// enrolmentPoll is how often the bootstrap checks whether an operator has
// turned up.
//
// It is a poll rather than a watch on purpose. The thing being waited for is a
// person walking to a console, so the difference between noticing in one
// second and noticing in three is nothing, and an inotify watch on a directory
// that may not exist yet is more moving parts than the problem deserves.
const enrolmentPoll = 2 * time.Second

// enrolmentReport is how often the wait says it is still waiting. Often enough
// that `journalctl -fu corium-bootstrap` looks alive, rarely enough that an
// overnight wait does not fill the journal.
const enrolmentReport = time.Minute

// waitForEnrolment holds a node outside any cluster until an operator claims
// it.
//
// This is the ordering ADR 4 rests on. Bootstrapping first and enrolling later
// would leave a machine that runs workloads and holds cluster credentials
// while still obeying whoever first reaches an unauthenticated port; there is
// no way to defend that state, so it is not reachable.
//
// The wait has no deadline, and that is the honest behaviour rather than an
// omission: it is waiting for a human, and a timeout would resolve to either
// joining unclaimed — the one thing ruled out — or failing a node that its
// operator was on their way to.
func waitForEnrolment(ctx context.Context, store *api.Store) error {
	enrolled, err := store.Enrolled()
	if err != nil {
		return err
	}

	if enrolled {
		return nil
	}

	slog.Warn("node is not enrolled and will not join a cluster until it is",
		"claimWith", "cctl enroll",
		"pairingCode", "printed on the console by corium-apid")

	ticker := time.NewTicker(enrolmentPoll)
	defer ticker.Stop()

	lastReport := time.Now()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting to be enrolled: %w", ctx.Err())

		case <-ticker.C:
		}

		enrolled, err := store.Enrolled()
		if err != nil {
			return err
		}

		if enrolled {
			slog.Info("node enrolled, continuing with bootstrap")

			return nil
		}

		if time.Since(lastReport) >= enrolmentReport {
			slog.Info("still waiting to be enrolled")

			lastReport = time.Now()
		}
	}
}

// gateOnEnrolment decides whether this node may proceed to bootstrap.
//
// It runs after validation and before anything is mutated, because the answer
// for an unclaimed node is that nothing should be mutated at all — not a
// hostname, not a disk.
func gateOnEnrolment(ctx context.Context, cfg *config.Config, opts Options) error {
	switch cfg.API.Mode() {
	case config.APIModeDisabled, config.APIModeConfigured:
		// A configured CA is pinned by corium-apid, which is ordered before
		// this unit. Nothing here has to wait for it: a node whose owner is
		// named in its own configuration was never unclaimed.
		return nil

	case config.APIModeMaintenance:
		if opts.DryRun {
			// A dry run renders and applies nothing, so blocking it would only
			// stop somebody checking a document on their workstation.
			slog.Info("configuration asks for maintenance mode; a real boot would wait here")

			return nil
		}

		return waitForEnrolment(ctx, api.NewStore(opts.StateDir))

	default:
		// Mode returns one of the three above. A fourth means somebody added a
		// mode and did not come back here, which is worth failing over rather
		// than defaulting to "carry on".
		return errors.New("unknown api mode")
	}
}
