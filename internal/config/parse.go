package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// ErrNoCoriumBlock reports that a document parsed cleanly but contained no
// `corium:` key. This is not necessarily a failure: a node may legitimately be
// provisioned with a plain cloud-config and no Kubernetes role at all, and
// callers decide whether that is acceptable.
var ErrNoCoriumBlock = errors.New("no corium block in cloud-config")

// document is the subset of a cloud-config document Corium cares about.
//
// The corium block is captured as a raw node rather than decoded in place, so
// that the surrounding document keeps its ordinary cloud-config keys — users,
// write_files, runcmd and the rest — without Corium having to know about any of
// them. Corium is a guest in this document, not its owner.
type document struct {
	// A value, not a pointer: yaml.v3 leaves a *yaml.Node field untouched
	// during decoding, so a pointer here silently yields an empty node and
	// every document looks like it has no corium block.
	Corium yaml.Node `yaml:"corium"`
}

// Parse extracts the `corium:` block from a cloud-config document and applies
// defaults. It does not validate; call Validate separately so that callers can
// distinguish a malformed document from a well-formed but invalid one.
//
// It returns ErrNoCoriumBlock if the document has no `corium:` key.
func Parse(data []byte) (*Config, error) {
	var doc document

	// No KnownFields here: unknown keys at the top level belong to cloud-init.
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing cloud-config: %w", err)
	}

	if doc.Corium.IsZero() {
		return nil, ErrNoCoriumBlock
	}

	cfg, err := decodeStrict(&doc.Corium)
	if err != nil {
		return nil, err
	}

	cfg.ApplyDefaults()

	return cfg, nil
}

// decodeStrict decodes the corium block, rejecting keys Corium does not know.
//
// Strictness is worth the round-trip through YAML here: inside our own block a
// key we do not recognise is a typo, and silently ignoring it means an operator
// discovers their node booted without the setting they carefully wrote.
func decodeStrict(node *yaml.Node) (*Config, error) {
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("reading corium block: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing corium block: %w", err)
	}

	return &cfg, nil
}

// ParseFile reads and parses a cloud-config document from disk.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return cfg, nil
}
