// Command corium-agent configures a Corium node on first boot.
//
// It reads the `corium:` block from the cloud-config document that cloud-init
// has already fetched and merged, renders the k0s configuration, and starts the
// appropriate k0s service for the node's role.
//
// The agent is idempotent: on a node that has already been bootstrapped it logs
// that fact and exits successfully. Re-bootstrapping a node that has joined a
// cluster destroys data, so the check is deliberate rather than incidental.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Corium-OS/Corium/internal/bootstrap"
	"github.com/Corium-OS/Corium/internal/config"
)

// Build metadata, injected at link time.
var (
	version = "dev"
	commit  = "unknown"
)

const usage = `corium-agent %s (%s)

Usage:
  corium-agent <command> [flags]

Commands:
  bootstrap    Configure this node and start k0s (run once, on first boot)
  validate     Parse and validate a configuration without applying it
  issue        Refresh the console status banner (run on a timer)
  backup       Snapshot the control plane with k0s backup (run on a timer)
  api set-ca   Replace the operator CA this node obeys, locally
  version      Print version information

Run 'corium-agent <command> -h' for command-specific flags.
`

func main() {
	if err := run(); err != nil {
		slog.Error("corium-agent failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version, commit)
		return errors.New("no command given")
	}

	// Bootstrap must not be interrupted halfway through. Cancelling the context
	// lets each step stop at a consistent point rather than being killed
	// between writing a config and starting the service that reads it.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	command, args := os.Args[1], os.Args[2:]

	switch command {
	case "bootstrap":
		return bootstrapCommand(ctx, args)
	case "validate":
		return validateCommand(ctx, args)
	case "issue":
		return issueCommand(ctx, args)
	case "backup":
		return backupCommand(ctx, args)
	case "api":
		return apiCommand(args)
	case "version":
		fmt.Printf("corium-agent %s (%s)\n", version, commit)
		return nil
	case "-h", "--help", "help":
		fmt.Printf(usage, version, commit)
		return nil
	default:
		fmt.Fprintf(os.Stderr, usage, version, commit)
		return fmt.Errorf("unknown command: %q", command)
	}
}

// bootstrapCommand configures the node and starts k0s.
func bootstrapCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	configPath := fs.String("config", "",
		"read one specific document instead of searching the source chain")
	dryRun := fs.Bool("dry-run", false,
		"render the configuration and print it without applying anything")

	if err := fs.Parse(args); err != nil {
		return err
	}

	slog.Info("starting bootstrap", "version", version, "dryRun", *dryRun)

	return bootstrap.Run(ctx, bootstrap.Options{
		ConfigPath: *configPath,
		DryRun:     *dryRun,
	})
}

// backupCommand snapshots the control plane and applies retention.
//
// corium-backup.service is what calls it, with the target and the retention
// count arriving as environment variables from the drop-in the bootstrap wrote
// from corium.backup. The flags exist so an operator can take one by hand
// without editing a unit.
//
// There is deliberately no default target. A missing drop-in means the schedule
// is not the one the node was configured with, and writing the cluster's
// secrets to a fallback directory nobody asked for is a worse answer than
// saying so.
func backupCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	path := fs.String("path", os.Getenv("CORIUM_BACKUP_PATH"),
		"directory the archive is written to")
	keep := fs.Int("keep", envInt("CORIUM_BACKUP_KEEP", config.DefaultBackupKeep),
		"how many archives to keep, newest first")

	if err := fs.Parse(args); err != nil {
		return err
	}

	return bootstrap.RunBackup(ctx, *path, *keep)
}

// envInt reads an integer from the environment, falling back to a default.
//
// A value systemd cannot have written -- the drop-in only ever holds a decimal
// integer -- means something edited it by hand and got it wrong, so the default
// is used and the run still produces a backup.
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return fallback
	}

	return value
}

// validateCommand parses and validates a configuration without applying it.
//
// It performs no network access, so it is safe to run in CI and on a
// workstation against a configuration destined for a node that does not exist
// yet.
func validateCommand(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("validate: expected exactly one file argument")
	}

	path := fs.Arg(0)

	cfg, err := config.ParseFile(path)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s is not valid:\n%w", path, err)
	}

	// A document that defers its role is valid and says so in those terms.
	// Printing an empty role would read as a bug in the validator rather than
	// as the configuration doing what it was written to do.
	role := string(cfg.Role)
	if role == "" {
		role = "not yet decided; this node waits to be told"
	}

	fmt.Printf("%s: valid (role %s, cluster %s)\n", path, role, cfg.Cluster.Name)

	return nil
}
