package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// ErrNoCoriumBlock reports that a document parsed cleanly but held no Corium
// configuration. This is not necessarily a failure: a node may legitimately be
// provisioned with a plain cloud-config and no Kubernetes role at all, and
// callers decide whether that is acceptable.
var ErrNoCoriumBlock = errors.New("no corium configuration in document")

// Parse reads a Corium configuration and applies defaults.
//
// Two document shapes are accepted, because the same configuration arrives by
// two different routes:
//
//   - embedded, under a `corium:` key in a cloud-config document, alongside
//     users, write_files and the rest;
//   - standalone, the configuration alone at the top level, which is what an
//     operator writes in /etc/corium/config.yaml or serves over PXE, where
//     wrapping it in a cloud-config would be ceremony for its own sake.
//
// The shapes are told apart by which key is present, so the rule is
// predictable: `corium:` means embedded, a top-level `role:` means standalone.
//
// Parse does not validate; call Validate separately so that callers can
// distinguish a malformed document from a well-formed but invalid one.
func Parse(data []byte) (*Config, error) {
	node, err := locate(data)
	if err != nil {
		return nil, err
	}

	cfg, err := decodeStrict(node)
	if err != nil {
		return nil, err
	}

	cfg.ApplyDefaults()

	return cfg, nil
}

// locate finds the Corium configuration within a document, whichever shape it
// takes.
func locate(data []byte) (*yaml.Node, error) {
	var root yaml.Node

	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing document: %w", err)
	}

	// An empty document decodes to a zero node.
	if root.IsZero() || len(root.Content) == 0 {
		return nil, ErrNoCoriumBlock
	}

	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, ErrNoCoriumBlock
	}

	// Mapping content alternates key, value, key, value.
	var standalone bool

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		switch mapping.Content[i].Value {
		case "corium":
			return mapping.Content[i+1], nil
		case "role":
			standalone = true
		}
	}

	if standalone {
		return mapping, nil
	}

	return nil, ErrNoCoriumBlock
}

// decodeStrict decodes the configuration, rejecting keys Corium does not know.
//
// Strictness is worth the round-trip through YAML: within our own schema a key
// we do not recognise is a typo, and silently ignoring it means an operator
// discovers their node booted without the setting they carefully wrote. It is
// applied here and not to the whole document, because at the top level of a
// cloud-config the unknown keys belong to cloud-init.
func decodeStrict(node *yaml.Node) (*Config, error) {
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("reading corium configuration: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing corium configuration: %w", err)
	}

	return &cfg, nil
}

// ParseFile reads and parses a configuration from disk.
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
