package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// machineIDPath holds an identifier systemd generates on first boot. It is a
// variable so tests can point it elsewhere.
var machineIDPath = "/etc/machine-id"

// hostnamePrefix prefixes generated names, so that a node is recognisable as
// Corium's in a node list without reading its labels.
const hostnamePrefix = "corium-"

// hostnameSuffixLength is how much of the machine ID ends up in the name. Eight
// hex characters is 32 bits: enough that a collision within one cluster is not
// a practical concern, short enough that the name stays readable.
const hostnameSuffixLength = 8

// genericHostnames are the names an unconfigured image boots with. They carry
// no identity, and in a cluster every node would answer to the same one.
var genericHostnames = map[string]bool{
	"":                      true,
	"fedora":                true,
	"localhost":             true,
	"localhost.localdomain": true,
}

// ensureHostname gives the node a name that is unique within its cluster.
//
// This matters more than it looks. Kubernetes identifies a node by its
// hostname, so two nodes sharing one do not collide loudly — they take turns
// overwriting each other's Node object, and etcd ends up with members it cannot
// tell apart. The cluster looks like it is working and is quietly broken.
//
// The generated name is derived from the machine ID rather than being random,
// because a name that changed on reboot would register a new node every time
// and leave the old one behind as a ghost. Derived means stable.
//
// Precedence:
//  1. An explicit node.name, if the operator set one.
//  2. The current hostname, if something already set a real one — cloud-init's
//     hostname module, DHCP, or an operator.
//  3. A name derived from the machine ID.
func ensureHostname(ctx context.Context, configured string) (string, error) {
	if configured != "" {
		return configured, applyHostname(ctx, configured)
	}

	current, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("reading hostname: %w", err)
	}

	if !genericHostnames[strings.ToLower(current)] {
		slog.Info("keeping the hostname already set", "hostname", current)

		return current, nil
	}

	generated, err := deriveHostname()
	if err != nil {
		return "", err
	}

	slog.Info("hostname was generic, deriving a stable one",
		"was", current, "now", generated)

	return generated, applyHostname(ctx, generated)
}

// deriveHostname builds a stable name from the machine ID.
func deriveHostname() (string, error) {
	raw, err := os.ReadFile(machineIDPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", machineIDPath, err)
	}

	id := strings.TrimSpace(string(raw))
	if len(id) < hostnameSuffixLength {
		// Without a machine ID there is no source of identity to derive from,
		// and inventing a random one would produce a node that renames itself
		// on every boot. Better to stop and say so.
		return "", errors.New(
			"cannot derive a hostname: machine ID is missing or too short; set node.name explicitly")
	}

	return hostnamePrefix + id[:hostnameSuffixLength], nil
}

// applyHostname sets the hostname for this boot and for subsequent ones.
func applyHostname(ctx context.Context, name string) error {
	// hostnamectl writes /etc/hostname and applies it without a reboot, which
	// matters because k0s reads the hostname when it registers the node and
	// must see the final value.
	cmd := exec.CommandContext(ctx, "hostnamectl", "set-hostname", name)

	if output, err := cmd.CombinedOutput(); err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("setting hostname to %q: %w: %s", name, err, trimmed)
		}

		return fmt.Errorf("setting hostname to %q: %w", name, err)
	}

	return nil
}
