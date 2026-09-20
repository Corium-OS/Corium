package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Corium-OS/Corium/internal/issue"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// issueDir and issueName place the status banner where every getty looks.
//
// /run rather than /etc, because the banner is a snapshot of live state -- what
// k0s is doing, what is staged for the next boot -- and must not outlive the
// boot that observed it. The name sorts before the enrolment drop-in
// corium-apid writes (10-corium-enrolment), so on an unclaimed node the status
// sits above the pairing code rather than below it.
const (
	issueDir  = "/run/issue.d"
	issueName = "05-corium-status.issue"
)

// issueCommand renders the console status banner and asks the gettys to redraw.
//
// It is run on a timer rather than once, because the banner reports state that
// settles after boot: k0s coming up, a node joining, an image staged for the
// next reboot, greenboot passing. A snapshot taken once would be wrong within
// the minute, and the console it is wrong on is the one an operator reaches for
// when they cannot reach the node any other way.
func issueCommand(ctx context.Context, _ []string) error {
	node := (&nodeinfo.Inspector{}).Collect(ctx)
	banner := issue.Render(node)

	// A shared drop-in directory: 0755 and the public convention /etc/issue.d
	// sets. Unlike the pairing code, the banner holds a role and a cluster name
	// -- neither a secret -- so it is world-readable.
	if err := os.MkdirAll(issueDir, 0o755); err != nil { //nolint:gosec // G301: a shared drop-in directory
		return fmt.Errorf("creating %s: %w", issueDir, err)
	}

	path := filepath.Join(issueDir, issueName)

	// Do nothing unless the banner actually changed. agetty --reload reprints
	// the whole issue and login prompt rather than redrawing them in place, so
	// reloading on every timer tick marches a fresh copy down the screen each
	// time it fires. Gating on a real change means the console reprints only
	// when the node's state does -- k0s coming up, greenboot passing, an image
	// staged -- and then falls quiet. It is also why the banner carries no
	// uptime: a field that ticks every render would defeat this and never let
	// the console settle.
	if current, err := os.ReadFile(path); err == nil && string(current) == banner { //nolint:gosec // G304: path is issueDir/issueName, both constants
		return nil
	}

	if err := os.WriteFile(path, []byte(banner), 0o644); err != nil { //nolint:gosec // G306: a login banner is not a secret
		return fmt.Errorf("writing %s: %w", path, err)
	}

	// Best effort: a prompt already on the screen only picks up the new banner
	// when asked to redraw. A node with no getty running, or an agetty too old
	// for the flag, loses the refresh and nothing else -- the next prompt drawn
	// still reads the file.
	if err := exec.CommandContext(ctx, "agetty", "--reload").Run(); err != nil {
		slog.Debug("could not ask agetty to redraw", "error", err)
	}

	return nil
}
