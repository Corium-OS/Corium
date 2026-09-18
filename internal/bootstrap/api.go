package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// gateOnEnrolment decides whether this node may proceed to bootstrap, and
// returns the configuration it should proceed with.
//
// It runs after validation and before anything is mutated, because the answer
// for an unclaimed node is that nothing should be mutated at all — not a
// hostname, not a disk.
//
// The configuration comes back out because it may have changed while the gate
// was holding: an operator who sends a document with `cctl enroll --config` or
// `cctl apply` is describing the node this boot is about to build, and
// bootstrapping the document the machine happened to start with would ignore
// them. See ADR 4, "Configuration, before a node is a node".
func gateOnEnrolment(
	ctx context.Context, cfg *config.Config, opts Options,
) (*config.Config, error) {
	if opts.DryRun {
		// A dry run renders and applies nothing, so blocking it would only
		// stop somebody checking a document on their workstation.
		if cfg.API.Mode() == config.APIModeMaintenance {
			slog.Info("configuration asks for maintenance mode; a real boot would wait here")
		}

		return cfg, nil
	}

	switch cfg.API.Mode() {
	case config.APIModeDisabled:
		return cfg, nil

	case config.APIModeConfigured:
		// A configured CA is pinned by corium-apid, which is ordered before
		// this unit. Nothing here has to wait for it: a node whose owner is
		// named in its own configuration was never unclaimed. It may still
		// have been told to wait for somebody to say what it is.
		return awaitConfiguration(ctx, cfg)

	case config.APIModeMaintenance:
		if err := waitForEnrolment(ctx, api.NewStore(opts.StateDir)); err != nil {
			return nil, err
		}

		return awaitConfiguration(ctx, cfg)

	default:
		// Mode returns one of the three above. A fourth means somebody added a
		// mode and did not come back here, which is worth failing over rather
		// than defaulting to "carry on".
		return nil, errors.New("unknown api mode")
	}
}

// awaitConfiguration waits for an operator to say what this node is, when it
// has been told to, and then re-reads whatever the answer turned out to be.
//
// The re-read happens either way, and that is deliberate: an enrolment can
// carry a document, so even a node that was not told to wait may have been
// handed a new configuration between booting and being released.
func awaitConfiguration(ctx context.Context, cfg *config.Config) (*config.Config, error) {
	if cfg.API.AwaitConfig {
		marker := filepath.Join(string(api.DefaultSessionDir), api.AppliedMarker)
		if err := waitForConfiguration(ctx, marker); err != nil {
			return nil, err
		}
	}

	return reload(ctx, cfg)
}

// waitForConfiguration blocks until an operator sends this node a document.
//
// It waits on the marker corium-apid writes, not on the document itself.
// Watching the file looks equivalent and is not: corium-apid and this unit are
// ordered against cloud-init and not against each other, so an apply can land
// before this has read anything at all -- and a node watching for a change
// that has already happened waits for ever. The marker answers the question
// actually being asked, which is whether somebody has answered.
func waitForConfiguration(ctx context.Context, marker string) error {
	if applied(marker) {
		// Already answered, before the wait even began. This is the race the
		// marker exists to make harmless.
		slog.Info("a configuration was applied before this node began waiting for one")

		return nil
	}

	slog.Warn("node is waiting to be told what it is",
		"configureWith", "cctl apply, or cctl enroll --config")

	ticker := time.NewTicker(enrolmentPoll)
	defer ticker.Stop()

	lastReport := time.Now()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for a configuration: %w", ctx.Err())

		case <-ticker.C:
		}

		if applied(marker) {
			slog.Info("configuration received, continuing with bootstrap")

			return nil
		}

		if time.Since(lastReport) >= enrolmentReport {
			slog.Info("still waiting for a configuration")

			lastReport = time.Now()
		}
	}
}

// applied reports whether an operator has sent a configuration this boot.
func applied(marker string) bool {
	_, err := os.Stat(marker)

	return err == nil
}

// reload re-resolves the configuration chain and validates what it finds.
//
// A node that was never sent anything reads back exactly what it booted with,
// so this costs nothing in the ordinary case. A node that was sent something
// gets it, which is the point.
func reload(ctx context.Context, booted *config.Config) (*config.Config, error) {
	current, err := load(ctx, "")
	if err != nil {
		// Whatever the node booted with parsed and validated, so falling back
		// to it leaves a working machine rather than a failed unit. The
		// warning matters: an operator who applied a document is entitled to
		// know it is not the one being used.
		slog.Warn("could not re-read the configuration; continuing with the one this node booted with",
			"error", err)

		return booted, nil
	}

	if err := current.Validate(); err != nil {
		return nil, fmt.Errorf("the configuration this node was given is invalid: %w", err)
	}

	// Validation lets a document that says awaitConfig omit the role, because
	// that document is a promise to be told later rather than a description of
	// a node. By here the telling has happened, so an absent role is no longer
	// deferred -- it is missing, and building a node around an empty one would
	// produce a machine of no particular kind.
	if current.Role == "" {
		return nil, errors.New(
			"the configuration this node was given does not name a role; " +
				"a document that finishes the wait has to say what the node is")
	}

	return current, nil
}
