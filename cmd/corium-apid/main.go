// Command corium-apid serves a Corium node's management API.
//
// It has two shapes and never both at once. On a node no operator has claimed,
// it serves a single unauthenticated route — enrolment — and prints a pairing
// code on the console; the node joins no cluster until somebody uses it. On a
// claimed node it requires a client certificate signed by the operator CA and
// does not serve enrolment at all.
//
// The daemon is off unless a node's configuration asks for it. With no `api:`
// block it exits successfully having done nothing, which is what every node
// provisioned before this existed does.
//
// See docs/adr/0004-management-api.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/source"
)

// Build metadata, injected at link time.
var (
	version = "dev"
	commit  = "unknown"
)

// exitDisabled is EX_CONFIG, and the systemd unit knows it by number: it means
// the node's configuration asks for no management API, which is a success and
// must not be restarted into a loop. Exiting 0 would mean "enrolled, restart
// me", and every ordinary node in a fleet would restart forever and land in
// `failed`.
const exitDisabled = 78

// errDisabled is the sentinel carrying that exit status out of run().
var errDisabled = errors.New("management API is disabled")

func main() {
	err := run()

	switch {
	case err == nil:
		return
	case errors.Is(err, errDisabled):
		os.Exit(exitDisabled)
	default:
		slog.Error("corium-apid failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	flags := flag.NewFlagSet("corium-apid", flag.ExitOnError)

	var (
		configPath = flags.String("config", "",
			"read one configuration document instead of searching the source chain")
		stateDir = flags.String("state-dir", api.StateDir,
			"where the operator CA and the serving identity are kept")
		listen = flags.String("listen", net.JoinHostPort("", strconv.Itoa(api.DefaultPort)),
			"address to serve on")
		showVersion = flags.Bool("version", false, "print version information and exit")
	)

	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *showVersion {
		fmt.Printf("corium-apid %s (%s)\n", version, commit)

		return nil
	}

	// SIGTERM must reach the listener rather than the process, so that a
	// request in flight finishes and an enrolment is never half-recorded.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, *configPath, *stateDir, *listen)
}

func serve(ctx context.Context, configPath, stateDir, listen string) error {
	store := api.NewStore(stateDir)

	// A node that has already been claimed serves the authenticated API
	// whatever its configuration now says. Enrolment is one-way, and reading
	// the configuration first would let an edited cloud-config reopen a door
	// that is meant to stay shut.
	enrolled, err := store.Enrolled()
	if err != nil {
		return err
	}

	if !enrolled {
		mode, err := claimFromConfiguration(ctx, store, configPath)
		if err != nil {
			return err
		}

		if mode == config.APIModeDisabled {
			slog.Info("management API is disabled, exiting")

			return errDisabled
		}
	}

	server, err := api.NewServer(store, listen)
	if err != nil {
		return err
	}

	return server.Serve(ctx)
}

// claimFromConfiguration reads the node's configuration and pins the operator
// CA it names, if it names one.
//
// A configuration that cannot be found is not an error here. A machine
// provisioned without a Corium block is a valid outcome — somebody wanted a
// host, not a Kubernetes node — and the answer for the API is the same as for
// the rest of the agent: do nothing, and say so.
func claimFromConfiguration(ctx context.Context, store *api.Store, path string) (config.APIMode, error) {
	cfg, err := load(ctx, path)

	switch {
	case errors.Is(err, config.ErrNoCoriumBlock), errors.Is(err, source.ErrNotFound):
		slog.Info("no corium configuration found, management API stays off")

		return config.APIModeDisabled, nil

	case err != nil:
		return config.APIModeDisabled, err
	}

	if err := cfg.Validate(); err != nil {
		return config.APIModeDisabled, fmt.Errorf("invalid configuration: %w", err)
	}

	mode := cfg.API.Mode()
	slog.Info("management API mode resolved", "mode", mode)

	if err := api.Claim(ctx, store, cfg); err != nil {
		return mode, err
	}

	return mode, nil
}

func load(ctx context.Context, path string) (*config.Config, error) {
	var (
		cfg *config.Config
		err error
	)

	if path != "" {
		cfg, err = config.ParseFile(path)
	} else {
		var found *source.Result

		if found, err = source.Resolve(ctx, source.Default()); err == nil {
			cfg, err = config.Parse(found.Document)
			if err != nil {
				err = fmt.Errorf("%s: %w", found.Source, err)
			}
		}
	}

	if err != nil {
		return nil, err
	}

	cfg.ApplyDefaults()

	return cfg, nil
}
