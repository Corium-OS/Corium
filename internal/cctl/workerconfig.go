package cctl

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/Corium-OS/Corium/internal/config"
	"gopkg.in/yaml.v3"
)

// RenderWorkerConfig turns a freshly minted join token into the corium: block a
// worker needs to join the cluster.
//
// It emits the block alone -- role, an optional node name and labels, and the
// inline token -- for pasting into a cloud-config, rather than a whole
// #cloud-config document. A worker's users, SSH keys and the rest are the
// operator's to add, and a generator that guessed at them would be wrong more
// often than right.
//
// The block is built from the real config.Config and validated before it is
// returned, so it cannot drift from the schema a node parses, and a generator
// that emitted something a node would reject is worse than an error here.
func RenderWorkerConfig(joinToken, name string, labels map[string]string) ([]byte, error) {
	if joinToken == "" {
		return nil, errors.New("no join token to render into a worker configuration")
	}

	cfg := config.Config{
		Role: config.RoleWorker,
		Join: config.Join{Token: joinToken},
	}

	if name != "" {
		cfg.Node.Name = name
	}

	if len(labels) > 0 {
		cfg.Node.Labels = labels
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("the generated configuration is invalid: %w", err)
	}

	// Wrapped under corium:, the key a cloud-config carries the block under.
	document := struct {
		Corium config.Config `yaml:"corium"`
	}{Corium: cfg}

	var buf bytes.Buffer

	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2) // Match the two-space indent every example uses.

	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encoding the worker configuration: %w", err)
	}

	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encoding the worker configuration: %w", err)
	}

	return buf.Bytes(), nil
}
