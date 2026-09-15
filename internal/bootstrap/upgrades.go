package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
)

const (
	// downloadTimer stages a new image without rebooting.
	downloadTimer = "corium-upgrade-download.timer"

	// bootcTimer is bootc's own unit, which downloads and reboots. The image
	// masks it, so enabling it means unmasking first.
	bootcTimer = "bootc-fetch-apply-updates.timer"

	// dropInDir holds the schedule override. /etc rather than /usr, because
	// this is a machine-local decision made at first boot.
	dropInDir = "/etc/systemd/system"
)

// applyUpgradePolicy wires up whatever unattended upgrade the operator asked
// for. Doing nothing is the default and the common case.
func applyUpgradePolicy(ctx context.Context, cfg *config.Config) error {
	policy := cfg.Upgrades.Automatic

	if policy == "" || policy == config.UpgradeNone {
		slog.Info("unattended upgrades disabled", "policy", config.UpgradeNone)

		return nil
	}

	timer := downloadTimer
	if policy == config.UpgradeApply {
		timer = bootcTimer

		// The image masks this deliberately; enabling it is the operator
		// explicitly asking for a node that reboots on its own.
		if err := run(ctx, "systemctl", "unmask", timer); err != nil {
			return err
		}
	}

	if err := writeSchedule(timer, cfg.Upgrades.Schedule); err != nil {
		return err
	}

	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	if err := run(ctx, "systemctl", "enable", "--now", timer); err != nil {
		return err
	}

	slog.Info("unattended upgrades enabled",
		"policy", policy, "schedule", cfg.Upgrades.Schedule, "timer", timer)

	return nil
}

// writeSchedule overrides a timer's OnCalendar.
//
// The empty OnCalendar= first is not redundant: systemd accumulates OnCalendar
// entries across drop-ins, so without clearing it the unit would fire on both
// its built-in schedule and the operator's.
func writeSchedule(timer, schedule string) error {
	if schedule == "" || schedule == config.DefaultUpgradeSchedule {
		return nil
	}

	dir := filepath.Join(dropInDir, timer+".d")

	// 0755 matches every other directory under /etc/systemd/system. The
	// contents are a schedule, not a secret, and keeping them readable means
	// `systemctl cat` works without root when someone is diagnosing a node.
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	content := "# Written by corium-agent from corium.upgrades.schedule.\n" +
		"[Timer]\n" +
		"OnCalendar=\n" +
		"OnCalendar=" + schedule + "\n"

	// 0644 for the same reason as the directory: a systemd drop-in holding a
	// cron-like expression is configuration, not a credential, and the files
	// systemd ships alongside it are readable too.
	path := filepath.Join(dir, "10-corium-schedule.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { // #nosec G306
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

// run executes a command, folding its output into the error.
func run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
}
