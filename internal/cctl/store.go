// Package cctl is the operator's side of the management API: the files a
// person keeps on their own machine, and the client that talks to a node with
// them.
//
// Everything here runs on a workstation, never on a node. That distinction is
// the reason the package exists separately from internal/api: the state it
// manages includes the private key that owns a fleet, and nothing holding that
// key belongs in an OS image.
package cctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Files in an operator's directory.
const (
	ConfigFile = "config.yaml"

	// CACertFile is public: it is what gets pasted into cloud-init.
	CACertFile = "operator-ca.pem"

	// CAKeyFile owns every node that trusts the certificate beside it. Losing
	// it means visiting every console; leaking it means somebody else owns the
	// fleet.
	CAKeyFile = "operator-ca.key"

	ClientCertFile = "client.crt"
	ClientKeyFile  = "client.key"
)

// DefaultDir is ~/.corium.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding your home directory: %w", err)
	}

	return filepath.Join(home, ".corium"), nil
}

// Config is what the operator's machine remembers about the nodes it talks to.
//
// It holds fingerprints and nothing else. A node signs its own certificate,
// because no private key is ever carried in a Corium configuration, so there
// is no CA to validate it against and the fingerprint is the check. Recording
// it here is what makes the second conversation with a node safe without
// anybody having to read the console again.
type Config struct {
	Nodes map[string]Node `yaml:"nodes,omitempty"`
}

// Node is one remembered endpoint.
type Node struct {
	// Fingerprint is the node's serving certificate, as printed on its
	// console.
	Fingerprint string `yaml:"fingerprint"`
}

// Store is an operator's directory.
type Store struct {
	dir string
}

// NewStore opens the directory, creating nothing until asked to.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir is where this store keeps its files.
func (s *Store) Dir() string { return s.dir }

// Path names a file in the store.
func (s *Store) Path(name string) string { return filepath.Join(s.dir, name) }

// Config reads the remembered nodes. A directory with no configuration yet is
// not an error: it is a first run.
func (s *Store) Config() (*Config, error) {
	data, err := os.ReadFile(s.Path(ConfigFile))

	switch {
	case errors.Is(err, os.ErrNotExist):
		return &Config{Nodes: map[string]Node{}}, nil
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", s.Path(ConfigFile), err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", s.Path(ConfigFile), err)
	}

	if config.Nodes == nil {
		config.Nodes = map[string]Node{}
	}

	return &config, nil
}

// Remember records a node's fingerprint.
func (s *Store) Remember(address, fingerprint string) error {
	config, err := s.Config()
	if err != nil {
		return err
	}

	config.Nodes[address] = Node{Fingerprint: fingerprint}

	encoded, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding configuration: %w", err)
	}

	return s.Write(ConfigFile, encoded, 0o600)
}

// Fingerprint returns what is remembered about an address, if anything.
func (s *Store) Fingerprint(address string) (string, error) {
	config, err := s.Config()
	if err != nil {
		return "", err
	}

	return config.Nodes[address].Fingerprint, nil
}

// Write installs a file in the store, atomically and with the mode asked for.
//
// The directory is 0700 whatever the files inside it are: it holds the CA key,
// and a world-readable directory around a 0600 key is an invitation to the
// next person who adds a file to it.
func (s *Store) Write(name string, data []byte, mode os.FileMode) error {
	if err := s.mkdir(); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(s.dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", s.dir, err)
	}

	defer func() {
		// Removing a file that was renamed away fails, and that failure is the
		// success path.
		_ = os.Remove(temporary.Name())
	}()

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("setting mode on %s: %w", name, err)
	}

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}

	if err := os.Rename(temporary.Name(), s.Path(name)); err != nil {
		return fmt.Errorf("installing %s: %w", name, err)
	}

	return nil
}

// mkdir creates the directory, and narrows it if it was already there.
//
// MkdirAll leaves an existing directory's mode alone, which is the trap: a
// ~/.corium that somebody created by hand, or that an earlier version left at
// 0755, would end up holding the key that owns a fleet while being readable by
// every account on the machine. The directory belongs to this tool, so
// tightening it is a correction rather than a liberty.
func (s *Store) mkdir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", s.dir, err)
	}

	info, err := os.Stat(s.dir)
	if err != nil {
		return fmt.Errorf("checking %s: %w", s.dir, err)
	}

	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}

	// 0700 rather than the 0600 gosec asks for: this is a directory, and a
	// directory without its execute bit cannot be traversed, so 0600 here would
	// make every file inside it unreachable.
	if err := os.Chmod(s.dir, 0o700); err != nil { //nolint:gosec // G302 does not distinguish directories
		return fmt.Errorf("narrowing %s to 0700: %w", s.dir, err)
	}

	return nil
}

// Exists reports whether a file is already in the store.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.Path(name))

	return err == nil
}
