package source

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// CloudInitPath is where cloud-init writes the fully merged cloud-config
// document for the current instance. Reading the merged result rather than the
// raw user-data means multipart payloads and vendor-data are already resolved.
const CloudInitPath = "/var/lib/cloud/instance/cloud-config.txt"

// fileSource reads a configuration from a path on disk.
type fileSource struct {
	path  string
	label string
}

// File returns a source that reads the given path.
func File(path string) Source {
	return fileSource{path: path, label: path}
}

// CloudInit returns a source that reads the merged cloud-config document.
func CloudInit() Source {
	return fileSource{path: CloudInitPath, label: "cloud-init"}
}

func (f fileSource) Name() string { return f.label }

func (f fileSource) Load(_ context.Context) ([]byte, error) {
	data, err := os.ReadFile(f.path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", f.path, err)
	case len(data) == 0:
		// An empty file is an absent answer, not an empty one. Treating it as a
		// configuration would mean booting a node with no role at all.
		return nil, ErrNotFound
	}

	return data, nil
}
