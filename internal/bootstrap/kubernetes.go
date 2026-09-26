package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// windowPath is where the booted image records which k0s versions it supports.
// A variable so tests can point it at a temporary file.
var windowPath = k0s.WindowPath

// applyKubernetesVersion puts the requested k0s version in place before k0s is
// installed.
//
// The ordering is the feature, and it is not subtle: `k0s install` and
// everything after it run /usr/bin/k0s, so the version that fills that path has
// to be settled before the first of them. An extension merged afterwards would
// be a node that installed one version's service and then ran another's binary
// under it.
//
// A node that declares no version does nothing here and runs the floor its
// image ships. That is the path that needs no network, and keeping it free of
// one is why the floor exists at all: reaching a particular version must never
// become a precondition of becoming a node.
//
// See docs/adr/0010-kubernetes-version-axis.md.
func applyKubernetesVersion(ctx context.Context, cfg *config.Config) error {
	requested := cfg.Kubernetes.Version
	if requested == "" {
		return nil
	}

	// Already checked for shape by config validation, which reads nothing from
	// disk. Parsed again here because a Version is what the rest of this takes.
	version, err := k0s.ParseVersion(requested)
	if err != nil {
		return fmt.Errorf("kubernetes.version: %w", err)
	}

	window, err := k0s.ReadWindow(windowPath)
	if err != nil {
		return err
	}

	// Refused before anything is downloaded, and named against what this image
	// does support. An operator who asked for a version outside the window has
	// to take an OS upgrade, and finding that out now costs nothing.
	if err := window.Check(version); err != nil {
		return err
	}

	// What is merged right now, which on a first boot is the floor.
	active, err := k0s.ActiveVersion(k0s.ExtensionRelease, window.Floor)
	if err != nil {
		return err
	}

	if active.Raw == version.Raw {
		slog.Info("k0s is already at the requested version", "version", version)

		return nil
	}

	// The skew rules are k0s's, and they are checked even here, where the node
	// has no cluster yet. A single-node cluster bootstrapping straight onto a
	// version two minors from its floor would come up, and then be a node whose
	// binary and whose staged payload disagree about what they support.
	if err := k0s.CheckSkew(active, version); err != nil {
		return err
	}

	if err := k0s.Fetch(ctx, version, cfg.Kubernetes.Mirror); err != nil {
		return err
	}

	if err := k0s.Activate(ctx, version); err != nil {
		return err
	}

	// Belt to the per-version timestamps the extensions are built with: k0s
	// skips re-staging its payload when an already-staged file's mtime and size
	// match, and a node running a stale kubelet against a new control plane is
	// not a failure anybody enjoys diagnosing. On a first boot there is usually
	// nothing here to remove.
	if err := k0s.ClearStaged(); err != nil {
		return err
	}

	// Confirmed rather than assumed. An extension that does not match is not an
	// error systemd reports -- it simply does not merge, and the node carries on
	// with the floor. Checking here turns that into a refusal to bootstrap,
	// which is the behaviour AGENTS section 7 asks for: a node that stops with a
	// clear reason beats one that half-joins a cluster running something nobody
	// asked for.
	merged, err := k0s.ActiveVersion(k0s.ExtensionRelease, window.Floor)
	if err != nil {
		return err
	}

	if merged.Raw != version.Raw {
		return fmt.Errorf(
			"k0s %s was installed but %s is merged; the extension does not match "+
				"this image (check ID and SYSEXT_LEVEL in /etc/os-release)",
			version, merged)
	}

	slog.Info("k0s version applied", "version", version, "was", active)

	return nil
}
