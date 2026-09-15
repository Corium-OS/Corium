package k0s

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/qjoly/corium/internal/config"
)

// Binary is the k0s executable, baked into the read-only system tree.
const Binary = "/usr/bin/k0s"

// TokenPath is where a join token is written before k0s reads it.
const TokenPath = "/etc/k0s/join-token"

// ServiceName returns the systemd unit k0s installs for a role.
func ServiceName(role config.Role) string {
	if role.IsController() {
		return "k0scontroller.service"
	}

	return "k0sworker.service"
}

// InstallArgs builds the k0s command line for a node.
//
// It is pure and returns the arguments rather than running them, so that the
// exact command can be asserted in tests and printed by --dry-run without any
// risk of it differing from what actually runs.
func InstallArgs(cfg *config.Config, hasToken bool) []string {
	args := []string{"install"}

	if cfg.Role.IsController() {
		args = append(args, "controller", "--config", ConfigPath)

		switch cfg.Role {
		case config.RoleSingle:
			// Implies --enable-worker and a SQLite-backed control plane. The
			// cluster cannot gain controllers later.
			args = append(args, "--single")
		case config.RoleControllerWorker:
			// Without --no-taints k0s taints the node NoSchedule, which would
			// make "controller+worker" schedule nothing and look broken.
			args = append(args, "--enable-worker", "--no-taints")
		}
	} else {
		args = append(args, "worker")
	}

	if hasToken {
		args = append(args, "--token-file", TokenPath)
	}

	args = append(args, nodeArgs(cfg)...)

	return args
}

// nodeArgs renders the kubelet-level attributes for this machine.
func nodeArgs(cfg *config.Config) []string {
	var args []string

	if len(cfg.Node.Labels) > 0 {
		// Sorted so that the command line is deterministic: identical input
		// must produce an identical command, or golden tests are worthless and
		// logs are hard to compare across nodes.
		keys := make([]string, 0, len(cfg.Node.Labels))
		for key := range cfg.Node.Labels {
			keys = append(keys, key)
		}

		sort.Strings(keys)

		pairs := make([]string, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", key, cfg.Node.Labels[key]))
		}

		args = append(args, "--labels", strings.Join(pairs, ","))
	}

	if len(cfg.Node.Taints) > 0 {
		taints := make([]string, 0, len(cfg.Node.Taints))
		for _, taint := range cfg.Node.Taints {
			taints = append(taints,
				fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect))
		}

		args = append(args, "--taints", strings.Join(taints, ","))
	}

	return args
}

// Install registers the k0s systemd unit for this node.
func Install(ctx context.Context, args []string) error {
	return run(ctx, Binary, args...)
}

// Start starts the k0s service.
func Start(ctx context.Context, role config.Role) error {
	return run(ctx, "systemctl", "start", ServiceName(role))
}

// run executes a command and folds its output into the returned error.
//
// k0s reports why it refused to do something on stderr; dropping that and
// surfacing only "exit status 1" would make every failure a guessing game.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}

		return fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, trimmed)
	}

	return nil
}
