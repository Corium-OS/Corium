package k0s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultMirror is where extensions are published when a node names no other.
const DefaultMirror = "ghcr.io/corium-os/k0s"

// fetchTool is the command that pulls and verifies an extension.
//
// skopeo rather than a registry client of our own, and the reason is the
// verification rather than the download. /etc/containers/policy.json is not a
// file a node interprets, it is a contract the containers/image library
// implements, and skopeo is that library's command line: an unflagged
// `skopeo copy` refuses a source the policy will not vouch for, before it reads
// anything else about the image. Writing that ourselves is what decision 13
// exists to refuse. skopeo runs nothing and starts nothing; it copies.
const fetchTool = "skopeo"

// ErrNoLayer reports an artefact that does not carry exactly one blob.
var ErrNoLayer = errors.New("not a k0s extension artefact")

// manifest is the part of an OCI manifest this needs: the blobs, and their
// digests, which is how the copied files are named on disk.
type manifest struct {
	Layers []struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
	} `json:"layers"`
}

// Reference builds the artefact reference for a version on this architecture.
//
// The tag is the version with its '+' replaced -- a registry tag may not
// contain one, and '_' is the conventional substitute the publishing side uses,
// so the mapping is mechanical in both directions rather than a lookup -- and
// then the architecture.
//
// One tag per architecture rather than a multi-architecture index. An index
// would let every node pull the same tag, but an artefact index has to carry
// platform descriptors for the registry to select on, and a node that pulled
// the wrong one would find out when the extension failed to merge rather than
// when it failed to download. The architecture is a thing the node knows for
// certain; putting it in the tag makes a mismatch a 404 at the point of asking.
func Reference(mirror string, version Version) string {
	if mirror == "" {
		mirror = DefaultMirror
	}

	return fmt.Sprintf("%s:%s-%s",
		mirror, strings.ReplaceAll(version.Raw, "+", "_"), runtime.GOARCH)
}

// Fetch downloads a k0s extension into the store, verified.
//
// Two checks happen inside skopeo rather than here, and both matter. The
// signature policy gates the source before anything is read, so an artefact
// nobody vouched for never reaches the disk. And every blob is digest-checked
// as it is copied, independently of signatures, so a truncated or corrupted
// download fails rather than installing an extension that will not merge.
//
// The copy lands in a `dir:` layout, which writes each blob verbatim under its
// own hex digest -- no compression, no tar, no extraction. The extension image
// is the blob. That is the whole reason this transport was chosen over pulling
// an ordinary single-layer image, which would mean decompressing and untarring
// 250 MB to recover one file that was already sitting there.
func Fetch(ctx context.Context, version Version, mirror string) error {
	if _, err := os.Stat(StoredImage(version)); err == nil {
		slog.Info("k0s extension already downloaded", "version", version)

		return nil
	}

	if err := os.MkdirAll(ExtensionStore, 0o700); err != nil {
		return fmt.Errorf("creating the extension store: %w", err)
	}

	// Staged inside the store rather than in /tmp: the image is a quarter of a
	// gigabyte, /tmp is a tmpfs sized from RAM on this image, and a download
	// that dies for want of memory on a small node is a confusing way to fail.
	staging, err := os.MkdirTemp(ExtensionStore, ".fetch-*")
	if err != nil {
		return fmt.Errorf("creating a staging directory: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			slog.Warn("could not remove the staging directory",
				"path", staging, "error", err)
		}
	}()

	reference := Reference(mirror, version)
	slog.Info("fetching the k0s extension", "version", version, "from", reference)

	// No --insecure-policy, and that omission is the security control: with it
	// skopeo accepts anything, and without it the node's own policy.json
	// decides. It is not passed conditionally anywhere.
	if err := run(ctx, fetchTool, "copy",
		"docker://"+reference, "dir:"+staging); err != nil {
		return fmt.Errorf("pulling %s: %w", reference, err)
	}

	blob, err := extensionBlob(staging)
	if err != nil {
		return fmt.Errorf("%s: %w", reference, err)
	}

	// Renamed into place as the last step, so an interrupted fetch leaves no
	// half-written file that a later run would mistake for a complete download.
	if err := os.Rename(blob, StoredImage(version)); err != nil {
		return fmt.Errorf("storing the extension: %w", err)
	}

	slog.Info("stored the k0s extension",
		"version", version, "path", StoredImage(version))

	return nil
}

// extensionBlob finds the single blob a copied artefact carries.
//
// Exactly one is required rather than "the first" or "the biggest". An artefact
// with several layers is not something this code knows how to install, and
// picking one of them would be a guess that succeeds most of the time -- which
// is the worst behaviour available, because the time it guesses wrong a node
// installs the wrong file and reports success.
func extensionBlob(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json")) // #nosec G304
	if err != nil {
		return "", fmt.Errorf("reading the artefact manifest: %w", err)
	}

	var parsed manifest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parsing the artefact manifest: %w", err)
	}

	if len(parsed.Layers) != 1 {
		return "", fmt.Errorf(
			"%w: expected exactly one blob, found %d",
			ErrNoLayer, len(parsed.Layers))
	}

	// dir: names each blob by the hex of its digest, with no algorithm prefix
	// and no extension.
	_, hex, found := strings.Cut(parsed.Layers[0].Digest, ":")
	if !found || hex == "" {
		return "", fmt.Errorf("%w: malformed blob digest %q",
			ErrNoLayer, parsed.Layers[0].Digest)
	}

	blob := filepath.Join(dir, hex)
	if _, err := os.Stat(blob); err != nil {
		return "", fmt.Errorf("the manifest names a blob that was not copied: %w", err)
	}

	return blob, nil
}
