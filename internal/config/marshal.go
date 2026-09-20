package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Document renders a configuration back to a standalone YAML document -- the
// top-level shape, with `role:` at its root, that Parse accepts.
//
// It exists so a node can record the configuration it actually applied and, on
// a later apply, diff against what it is running rather than against whatever
// an operator may since have hand-edited into /etc/corium/config.yaml. The
// round trip is deliberate: Parse fills defaults, so a recorded document and a
// freshly parsed one compare on equal terms.
func (c *Config) Document() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encoding configuration: %w", err)
	}

	return data, nil
}
