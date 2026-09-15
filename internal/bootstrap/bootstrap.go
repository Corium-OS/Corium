// Package bootstrap turns a Corium configuration into a running Kubernetes node.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/qjoly/corium/internal/config"
	"github.com/qjoly/corium/internal/k0s"
	"github.com/qjoly/corium/internal/source"
)

// StateDir holds Corium's own persistent state. It lives under /var because
// that is the only part of the filesystem that survives an OS upgrade.
const StateDir = "/var/lib/corium"

// MarkerFile records that this node has been bootstrapped.
//
// Its existence is the only thing standing between a reboot and a node
// re-running cluster bootstrap on a machine that already joined a cluster, so
// it is written last and never removed automatically.
var MarkerFile = filepath.Join(StateDir, "bootstrapped")

// Options controls a bootstrap run.
type Options struct {
	// ConfigPath reads one specific document instead of searching. Empty means
	// search the standard source chain, which is what happens on a real boot;
	// setting it is for testing a document by hand.
	ConfigPath string

	// DryRun renders everything and applies nothing.
	DryRun bool
}

// Run bootstraps the node. It is idempotent: on an already-bootstrapped node it
// logs that fact and returns nil.
func Run(ctx context.Context, opts Options) error {
	if !opts.DryRun {
		bootstrapped, err := alreadyBootstrapped()
		if err != nil {
			return err
		}

		if bootstrapped {
			slog.Info("node is already bootstrapped, nothing to do",
				"marker", MarkerFile)

			return nil
		}
	}

	cfg, err := load(ctx, opts.ConfigPath)
	if err != nil {
		if errors.Is(err, config.ErrNoCoriumBlock) || errors.Is(err, source.ErrNotFound) {
			// A machine provisioned without a Corium configuration is a valid
			// outcome: someone wanted a host, not a Kubernetes node. Say so and
			// stop, rather than failing.
			slog.Info("no corium configuration found, leaving node unconfigured")

			return nil
		}

		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	slog.Info("configuration accepted",
		"role", cfg.Role,
		"cluster", cfg.Cluster.Name,
		"cni", cfg.Network.CNI,
		"storage", cfg.Storage.Type,
		"addons", len(cfg.Addons))

	rendered, err := k0s.Render(cfg)
	if err != nil {
		return err
	}

	// A dry run reaches no further than this process. Resolving the token would
	// mean contacting a secret store to produce output nobody applies, which is
	// both a surprise and, on a shared network, a leak of intent.
	if opts.DryRun {
		return report(cfg, rendered, k0s.InstallArgs(cfg, cfg.Join.Required()))
	}

	token, err := resolveToken(ctx, cfg)
	if err != nil {
		return err
	}

	args := k0s.InstallArgs(cfg, token != "")

	return apply(ctx, cfg, rendered, args, token)
}

// apply writes the node's configuration and starts k0s.
//
// Ordering matters: everything k0s reads is on disk before k0s is told about
// it, and the marker is written only once the service is running. A failure
// part-way leaves the node unmarked, so the next boot retries from a known
// point rather than resuming into an unknown one.
func apply(ctx context.Context, cfg *config.Config, rendered []byte, args []string, token string) error {
	if cfg.Role.IsController() {
		if err := writeFile(k0s.ConfigPath, rendered, 0o600); err != nil {
			return err
		}

		slog.Info("wrote k0s configuration", "path", k0s.ConfigPath)
	}

	if token != "" {
		if err := writeFile(k0s.TokenPath, []byte(token), 0o600); err != nil {
			return err
		}

		// Deliberately logged without the token itself.
		slog.Info("wrote join token", "path", k0s.TokenPath)
	}

	slog.Info("installing k0s service", "args", args)

	if err := k0s.Install(ctx, args); err != nil {
		return err
	}

	service := k0s.ServiceName(cfg.Role)
	slog.Info("starting k0s", "service", service)

	if err := k0s.Start(ctx, cfg.Role); err != nil {
		return err
	}

	if err := markBootstrapped(); err != nil {
		return err
	}

	slog.Info("bootstrap complete", "role", cfg.Role, "service", service)

	return nil
}

// report prints what a run would do without touching the system.
func report(cfg *config.Config, rendered []byte, args []string) error {
	fmt.Printf("# role: %s\n", cfg.Role)
	fmt.Printf("# command: %s %v\n\n", k0s.Binary, args)

	if cfg.Role.IsController() {
		fmt.Printf("# %s\n%s\n", k0s.ConfigPath, rendered)
	} else {
		fmt.Printf("# %s is not written for workers; they take their\n"+
			"# configuration from the control plane they join.\n", k0s.ConfigPath)
	}

	return nil
}

func alreadyBootstrapped() (bool, error) {
	_, err := os.Stat(MarkerFile)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("checking %s: %w", MarkerFile, err)
	}
}

func markBootstrapped() error {
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", StateDir, err)
	}

	if err := os.WriteFile(MarkerFile, nil, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", MarkerFile, err)
	}

	return nil
}

// writeFile writes a file and its parent directory with a restrictive mode.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// load finds and parses the node's configuration.
//
// With no explicit path it searches the standard source chain, so a node can be
// configured by cloud-init, by a file an operator placed, by the kernel command
// line, or by a default baked into the image -- covering platforms that have no
// cloud-init datasource at all.
func load(ctx context.Context, path string) (*config.Config, error) {
	if path != "" {
		return config.ParseFile(path)
	}

	found, err := source.Resolve(ctx, source.Default())
	if err != nil {
		return nil, err
	}

	cfg, err := config.Parse(found.Document)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", found.Source, err)
	}

	return cfg, nil
}
