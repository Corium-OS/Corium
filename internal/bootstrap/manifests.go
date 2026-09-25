package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Corium-OS/Corium/internal/config"
)

// manifestsDir is the directory the k0s manifest deployer watches. A variable
// so that tests can write a real tree into a temporary directory rather than
// reimplementing the writing to check it.
var manifestsDir = "/var/lib/k0s/manifests"

// manifestsDirMode is what k0s creates the same directory with. Corium gets
// there first, before `k0s install`, and a mode k0s disagrees with is one it
// silently corrects on startup — so agree with it here rather than have the two
// take turns.
const manifestsDirMode = 0o755

// manifestFileMode keeps the files themselves root-only. They are not secrets
// by definition, but a manifest is as free to carry a Secret as anything else,
// and the deployer reads them as root, so there is nothing to pay for it.
const manifestFileMode = 0o600

// applyManifests writes every declared stack into the directory k0s watches.
//
// This runs before `k0s install`, and the ordering is the point. The deployer
// sweeps the directory as soon as the controller comes up, so a file written
// afterwards is applied to a cluster that has already declared itself ready:
// the difference between a node that boots with its address pool and one that
// boots, reports healthy, and acquires the pool some time later — by which
// point whatever was waiting on a LoadBalancer has already failed.
//
// Each file is written whole rather than appended to, so bootstrapping the same
// node twice produces the same bytes instead of a second copy of the same
// objects under a name the deployer would then treat as a separate resource.
func applyManifests(cfg *config.Config) error {
	for _, stack := range cfg.Manifests {
		dir := filepath.Join(manifestsDir, stack.Name)

		if err := os.MkdirAll(dir, manifestsDirMode); err != nil { //nolint:gosec // k0s owns this directory and creates it 0755
			return fmt.Errorf("creating %s: %w", dir, err)
		}

		for _, file := range stack.Files {
			path := filepath.Join(dir, file.Name)

			if err := os.WriteFile(path, []byte(file.Content), manifestFileMode); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
		}

		slog.Info("wrote bundled manifests",
			"stack", stack.Name, "directory", dir, "files", len(stack.Files))
	}

	return nil
}
