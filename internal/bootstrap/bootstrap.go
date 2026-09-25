// Package bootstrap turns a Corium configuration into a running Kubernetes node.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/source"
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

	// StateDir overrides where the management API keeps the operator CA.
	// Empty means the real path; setting it is for tests.
	StateDir string
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

	// A node nobody has claimed is a node in no cluster. This is checked before
	// the hostname is settled and before any disk is touched, because for an
	// unclaimed node the correct amount of the machine to change is none of it.
	// The gate may hand back a different configuration from the one that went
	// in: an operator can send a node its document while it is held, and that
	// document is the one this boot is supposed to build.
	cfg, err = gateOnEnrolment(ctx, cfg, opts)
	if err != nil {
		return err
	}

	// The hostname must be final before k0s starts: k0s registers the node
	// under whatever it reads at startup, and renaming afterwards leaves the
	// old Node object behind. A dry run must not rename the machine, so it is
	// skipped there.
	hostname := cfg.Node.Name

	if !opts.DryRun {
		if hostname, err = ensureHostname(ctx, cfg.Node.Name); err != nil {
			return err
		}
	}

	slog.Info("configuration accepted",
		"hostname", hostname,
		"role", cfg.Role,
		"cluster", cfg.Cluster.Name,
		"cni", cfg.Network.CNI,
		"storage", cfg.Storage.Type,
		"addons", len(cfg.Addons))

	// Disks first. Everything after this point may want to write to an array,
	// and an array created after the fact is an array something has already
	// written past.
	if !opts.DryRun {
		if err := applyRAID(ctx, cfg); err != nil {
			return err
		}

		// ZFS pools on the same footing as RAID, and before k0s for the same
		// reason: a pool imported after the kubelet has started is a mount it has
		// already written past.
		if err := applyZFS(ctx, cfg); err != nil {
			return err
		}

		// Encrypted volumes last of the three, and before k0s for the same
		// reason again. Last because a volume may sit on top of a /dev/md/<name>
		// raid[] has just assembled, so the arrays have to exist first; nothing
		// runs the other way round, which is why zfs[] pools cannot be built on
		// an encrypted volume.
		if err := applyLUKS(ctx, cfg); err != nil {
			return err
		}

		// The overlay before k0s, for the same reason as the disks: the node
		// registers over it and joins over it, so the interface has to be up
		// before k0s decides its address or reaches the control plane.
		if err := applyWireGuard(ctx, cfg); err != nil {
			return err
		}
	}

	rendered, err := k0s.Render(cfg)
	if err != nil {
		return err
	}

	// The address the kubelet registers with. An overlay interface the operator
	// marked as the node address wins: computed from configuration alone, so a
	// dry run reports the same address a real boot would register.
	nodeIP := cfg.WireGuardNodeAddress()

	// A dry run reaches no further than this process. Resolving the token would
	// mean contacting a secret store to produce output nobody applies, which is
	// both a surprise and, on a shared network, a leak of intent.
	if opts.DryRun {
		return report(cfg, rendered, k0s.InstallArgs(cfg, cfg.Join.Required(), nodeIP))
	}

	token, err := resolveToken(ctx, cfg)
	if err != nil {
		return err
	}

	// The VRRP password is resolved after validation and before rendering the
	// final configuration, because the rendered file has to carry it.
	authPass, err := resolveAuthPass(ctx, cfg)
	if err != nil {
		return err
	}

	if authPass != "" {
		cfg.HA.AuthPass = authPass

		// Re-render: the first pass ran before the secret was available.
		if rendered, err = k0s.Render(cfg); err != nil {
			return err
		}
	}

	// Pin the address the kubelet registers with. The overlay address, if the
	// operator marked one, is already in nodeIP. Otherwise, on an HA controller,
	// detect the real address so a node holding the virtual IP does not advertise
	// one that moves on failover.
	switch {
	case nodeIP != "":
		slog.Info("registering with the overlay address", "nodeIP", nodeIP)
	case cfg.HA.Enabled:
		if nodeIP, err = detectNodeIP(cfg.HA.VirtualIP); err != nil {
			return err
		}

		slog.Info("registering with a fixed node address",
			"nodeIP", nodeIP, "virtualIP", cfg.HA.VirtualIP)
	}

	args := k0s.InstallArgs(cfg, token != "", nodeIP)

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

	// After k0s is running: an upgrade policy is about the node's future, not
	// about bringing it up, and failing here should not leave a cluster
	// half-joined.
	if err := applyUpgradePolicy(ctx, cfg); err != nil {
		return err
	}

	// A backup schedule belongs here for the same reason: `k0s backup` reads a
	// control plane that has to be up, and scheduling one is about the node's
	// future rather than about bringing it up.
	if err := applyBackupPolicy(ctx, cfg); err != nil {
		return err
	}

	// What the node became, recorded before the marker so that a machine the
	// marker calls bootstrapped can always say what it was bootstrapped as.
	if err := recordState(cfg); err != nil {
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

// recordState writes what this node was actually made into.
//
// It is deliberately separate from the configuration it came from: a
// cloud-config can be edited after a node has joined a cluster, and from then
// on it describes an intention rather than a machine. The management API
// reports from this file for that reason.
func recordState(cfg *config.Config) error {
	state := nodeinfo.State{
		Role:           string(cfg.Role),
		Cluster:        cfg.Cluster.Name,
		Endpoint:       clusterEndpoint(cfg),
		BootstrappedAt: time.Now().UTC(),
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encoding node state: %w", err)
	}

	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", StateDir, err)
	}

	if err := writeFile(nodeinfo.StateFile, encoded, 0o644); err != nil {
		return err
	}

	// The baseline a day-two apply diffs against. Recorded here, from the
	// configuration the node was actually built with, so that the first apply on
	// a node has something true to compare a proposal to. 0600, because the
	// document can carry secret references and inline escape-hatch values, the
	// same care writeConfigDocument takes. See ADR 8.
	document, err := cfg.Document()
	if err != nil {
		return fmt.Errorf("recording applied configuration: %w", err)
	}

	return writeFile(nodeinfo.AppliedConfigFile, document, 0o600)
}

// clusterEndpoint is the address clients should use to reach this cluster.
//
// The virtual IP wins where there is one: on an HA control plane it is the
// whole point, since it is the address that survives losing any one
// controller, and a kubeconfig pointing at a particular controller is a
// kubeconfig that stops working the first time that controller does.
//
// Empty means the node's own address is the answer, which is true for a single
// node and for a cluster nobody gave an endpoint.
func clusterEndpoint(cfg *config.Config) string {
	if cfg.HA.Enabled && cfg.HA.VirtualIP != "" {
		// Stored in CIDR form because keepalived needs the prefix length to
		// add the address to an interface. A client wants the address.
		if address, _, found := strings.Cut(cfg.HA.VirtualIP, "/"); found {
			return address
		}

		return cfg.HA.VirtualIP
	}

	return cfg.Cluster.Endpoint
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
