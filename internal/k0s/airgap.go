package k0s

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The air-gap bundle: the k0s system images of exactly the version a node
// runs, delivered as its own signed OCI artefact. See ADR 13.
//
// k0s watches <data-dir>/images and imports every bundle it finds there into
// containerd when the worker starts, so a node with no route to a public
// registry gets pause, kube-proxy, CoreDNS, the CNI and the rest from the
// file instead of the network
// (https://docs.k0sproject.io/stable/airgap-install/).
const (
	// DefaultBundleMirror is where bundles are published when a node names no
	// other. It is the extension mirror with "-airgap" appended, and a custom
	// mirror follows the same rule, so the two artefacts of one version always
	// sit in sibling repositories (BundleReference).
	DefaultBundleMirror = DefaultMirror + "-airgap"

	// ImageDir is where k0s looks for bundles: <data-dir>/images.
	ImageDir = "/var/lib/k0s/images"

	// bundlePrefix and bundleSuffix name the files ClearBundles owns. Anything
	// else in ImageDir is not ours to remove.
	bundlePrefix = "k0s-airgap-bundle-"
	bundleSuffix = ".tar"
)

// BundleReference builds the bundle artefact reference for a version on this
// architecture.
//
// The tag is the same one Reference builds for the extension, and the
// repository is the mirror with "-airgap" appended: a mirror serves a version
// through two sibling repositories, ghcr.io/corium-os/k0s and
// ghcr.io/corium-os/k0s-airgap by default. Deriving the bundle repository from
// the mirror rather than naming a second field is what keeps the schema at two
// keys -- "serve this version from my registry" is one statement about both
// artefacts, and a mirror that carries only one of them fails the pull and is
// named, not silently half-served.
func BundleReference(mirror string, version Version) string {
	if mirror == "" {
		mirror = DefaultBundleMirror
	} else {
		mirror += "-airgap"
	}

	return fmt.Sprintf("%s:%s-%s",
		mirror, strings.ReplaceAll(version.Raw, "+", "_"), runtime.GOARCH)
}

// BundleName is the file name the bundle is stored under, and the name k0s
// sees in ImageDir.
//
// The name is upstream's asset name for the current releases
// (k0s-airgap-bundle-<version>-linux-<arch>.tar), kept uniform across the
// window even though older releases named the asset without "-linux-.tar":
// the file is ours once stored, and k0s imports any bundle in the directory.
func BundleName(version Version) string {
	return fmt.Sprintf("k0s-airgap-bundle-%s-linux-%s.tar", version.Raw, runtime.GOARCH)
}

// StoredBundle is where a downloaded bundle for a version is kept.
func StoredBundle(version Version) string {
	return filepath.Join(ImageDir, BundleName(version))
}

// FetchBundle downloads the air-gap bundle for a version into ImageDir,
// verified by the same policy.json as the extension.
//
// It must land before k0s starts: k0s imports the bundles it finds in
// <data-dir>/images when the worker starts, which is why both callers run it
// before the service is installed or restarted, and why a failure here aborts
// a version change before anything is drained -- a node that swapped its
// version and only then found it had no images is the failure this feature
// exists to remove.
//
// The bundle is not read by anything in /usr: k0s reads it from /var, so it is
// not sysext content and travels as its own artefact (ADR 13).
func FetchBundle(ctx context.Context, version Version, mirror string) error {
	if _, err := os.Stat(StoredBundle(version)); err == nil {
		slog.Info("air-gap bundle already downloaded", "version", version)

		return nil
	}

	// The bundle is placed as a real file rather than a symlink into the
	// store: k0s watches ImageDir and imports what it finds there, and a
	// dangling link would be imported as nothing. Staging happens beside the
	// extension store rather than in ImageDir itself, so the watcher never
	// sees a half-written download.
	if err := os.MkdirAll(ImageDir, 0o755); err != nil { // #nosec G301
		return fmt.Errorf("creating the image directory: %w", err)
	}

	reference := BundleReference(mirror, version)
	slog.Info("fetching the air-gap bundle", "version", version, "from", reference)

	// The bundle is several hundred megabytes, so it stages on /var like the
	// extension, not on the RAM-backed /tmp. copyArtefact does the verified
	// pull and the rename into place.
	if err := copyArtefact(ctx, reference, filepath.Dir(ExtensionStore), StoredBundle(version)); err != nil {
		return err
	}

	slog.Info("stored the air-gap bundle",
		"version", version, "path", StoredBundle(version))

	return nil
}

// ClearBundles removes every air-gap bundle in dir except the one for keep.
//
// A version change must not leave the previous minor's bundle behind: each is
// several hundred megabytes, and /var filling with one tarball per minor is a
// slow disk failure nobody diagnoses. Only files this package names are
// removed -- the directory is k0s's, and a bundle an operator placed by hand
// under another name stays.
func ClearBundles(dir string, keep Version) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	keepName := BundleName(keep)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasPrefix(name, bundlePrefix) ||
			!strings.HasSuffix(name, bundleSuffix) ||
			name == keepName {
			continue
		}

		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing the previous version's bundle %s: %w", path, err)
		}

		slog.Info("removed the previous version's air-gap bundle", "path", path)
	}

	return nil
}
