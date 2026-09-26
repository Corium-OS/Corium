package k0s

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The system extension that fills the k0s slot.
//
// The name is fixed rather than versioned because it names the slot, not its
// contents: systemd matches /var/lib/extensions/<name>.raw against the
// extension-release file inside the image, and a node has one k0s extension at
// a time. Which version is in it is the question the metadata answers.
const (
	// ExtensionName is what systemd knows the extension as.
	ExtensionName = "corium-k0s"

	// ExtensionPath is the entry systemd merges, always a symlink into the
	// store below. A node changes version by repointing it.
	ExtensionPath = "/var/lib/extensions/" + ExtensionName + ".raw"

	// ExtensionStore holds the images a node has downloaded, under their own
	// versioned names.
	//
	// They live beside /var/lib/extensions rather than inside it because
	// systemd merges *every* entry in that directory: a directory of versions
	// is not somewhere an inactive version can be kept.
	ExtensionStore = "/var/lib/corium/k0s"

	// ExtensionRelease is where the merged extension's metadata appears. It is
	// readable only while the extension is merged, which is precisely what
	// makes it the answer to "what is actually running".
	ExtensionRelease = "/usr/lib/extension-release.d/extension-release." + ExtensionName

	// WindowPath is the set of versions the booted image supports, written at
	// image build time from build/k0s.lock.
	WindowPath = "/usr/lib/corium/k0s.window"

	// StagedBinaries is where k0s extracts its embedded payload.
	StagedBinaries = "/var/lib/k0s/bin"
)

// versionKey is the field carrying the k0s version in an extension-release
// file. It is Corium's own, not systemd's.
const versionKey = "CORIUM_K0S_VERSION"

// ErrOutsideWindow reports a version the booted image does not support.
var ErrOutsideWindow = errors.New("outside the window this image supports")

// Window is the set of k0s versions a booted image will run.
//
// It is a property of the image, not of the node: moving outside it is still an
// OS upgrade. The axes are independent, not unbounded.
type Window struct {
	// Floor is the version baked into /usr/bin/k0s, run when nothing else is
	// merged.
	Floor Version

	// Versions are every version this image supports, the floor among them.
	Versions []Version
}

// ReadWindow loads the window the booted image was built with.
func ReadWindow(path string) (Window, error) {
	file, err := os.Open(path) // #nosec G304 -- a path from our own /usr.
	if err != nil {
		return Window{}, fmt.Errorf("reading the supported version window: %w", err)
	}

	defer func() {
		// Nothing was written, so a close error says nothing a read error has
		// not already said.
		_ = file.Close()
	}()

	var window Window

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if raw, ok := strings.CutPrefix(line, "K0S_FLOOR="); ok {
			if window.Floor, err = ParseVersion(raw); err != nil {
				return Window{}, fmt.Errorf("%s: floor: %w", path, err)
			}

			continue
		}

		version, err := ParseVersion(line)
		if err != nil {
			return Window{}, fmt.Errorf("%s: %w", path, err)
		}

		window.Versions = append(window.Versions, version)
	}

	if err := scanner.Err(); err != nil {
		return Window{}, fmt.Errorf("reading %s: %w", path, err)
	}

	if window.Floor.Raw == "" {
		return Window{}, fmt.Errorf("%s: no K0S_FLOOR declared", path)
	}

	return window, nil
}

// Supports reports whether a version can run on this image.
//
// The match is exact rather than by minor. A window entry is a version somebody
// built, signed and published an extension for; a version on the same minor
// that nobody published is not something a node can be asked to run, and saying
// so here is better than a pull that fails with a 404.
func (w Window) Supports(version Version) bool {
	if version.Raw == w.Floor.Raw {
		return true
	}

	for _, supported := range w.Versions {
		if supported.Raw == version.Raw {
			return true
		}
	}

	return false
}

// Check reports whether this image will run a version, naming what it does
// support when it will not.
func (w Window) Check(version Version) error {
	if w.Supports(version) {
		return nil
	}

	supported := make([]string, 0, len(w.Versions))
	for _, version := range w.Versions {
		supported = append(supported, version.Raw)
	}

	return fmt.Errorf(
		"kubernetes.version: %s is %w; this image supports %s. "+
			"Reaching another version is an OS upgrade",
		version, ErrOutsideWindow, strings.Join(supported, ", "))
}

// ActiveVersion reports the k0s version currently filling the slot.
//
// It reads the merged extension's metadata rather than executing the binary,
// which matters when the reason for asking is that the binary did not start. A
// node with no extension merged is running its floor, which is an ordinary
// state and not an error: it is what every node does until it is told
// otherwise, and what a node falls back to if its extension stops matching.
func ActiveVersion(releasePath string, floor Version) (Version, error) {
	file, err := os.Open(releasePath) // #nosec G304 -- a path from our own /usr.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return floor, nil
		}

		return Version{}, fmt.Errorf("reading the merged extension: %w", err)
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		raw, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), versionKey+"=")
		if !ok {
			continue
		}

		version, err := ParseVersion(raw)
		if err != nil {
			return Version{}, fmt.Errorf("%s: %s: %w", releasePath, versionKey, err)
		}

		return version, nil
	}

	if err := scanner.Err(); err != nil {
		return Version{}, fmt.Errorf("reading %s: %w", releasePath, err)
	}

	// An extension is merged but does not say which version it carries. This is
	// not a Corium extension, or not one this image understands, and guessing
	// that it is the floor would report a version the node is demonstrably not
	// running.
	return Version{}, fmt.Errorf(
		"%s: no %s; the merged extension is not one this image built",
		releasePath, versionKey)
}

// StoredImage is where a downloaded extension for a version is kept.
func StoredImage(version Version) string {
	// The tag contains a '+', which is legal in a filename and is kept: the
	// file is named after the thing it holds, and a mangled name is one more
	// mapping to get wrong when somebody is reading the directory by hand.
	return filepath.Join(ExtensionStore, version.Raw+".raw")
}

// Activate points the extension slot at a version and merges it.
//
// It does not stop or start k0s. Swapping the binary under a running service is
// the one sequence k0s documents as unsupported, so the caller drains and stops
// first -- this function is the middle of that sequence and refuses to be the
// whole of it.
func Activate(ctx context.Context, version Version) error {
	image := StoredImage(version)

	if _, err := os.Stat(image); err != nil {
		return fmt.Errorf("k0s %s is not downloaded: %w", version, err)
	}

	// Written and renamed rather than replaced in place: a node interrupted
	// mid-swap finds either the old extension or the new one in the slot, never
	// a dangling symlink that merges as nothing and boots without Kubernetes.
	staging := ExtensionPath + ".new"

	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clearing a previous staged extension link: %w", err)
	}

	if err := os.Symlink(image, staging); err != nil {
		return fmt.Errorf("linking the extension: %w", err)
	}

	if err := os.Rename(staging, ExtensionPath); err != nil {
		return fmt.Errorf("installing the extension: %w", err)
	}

	// refresh, not merge: it unmerges whatever is there before merging what is
	// there now, which is the difference between changing version and being
	// told the slot is already occupied.
	if err := run(ctx, "systemd-sysext", "refresh"); err != nil {
		return fmt.Errorf("merging the extension: %w", err)
	}

	return nil
}

// ClearStaged removes the binaries k0s extracted from a previous version.
//
// This is not tidiness. k0s skips re-staging its embedded payload when an
// already-staged file's mtime equals the k0s executable's and its size matches
// (pkg/assets/stage.go), so a version change can leave a node running a stale
// kubelet against a new control plane. The extensions are built with per-version
// timestamps so the collision should not arise; this removes the possibility
// rather than reasoning about when it can happen, and costs one re-extraction
// on a path that has already stopped the service.
func ClearStaged() error {
	if err := os.RemoveAll(StagedBinaries); err != nil {
		return fmt.Errorf("clearing staged k0s binaries: %w", err)
	}

	return nil
}
