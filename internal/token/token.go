// Package token mints k0s join tokens on a controller, so an operator can add a
// node to a cluster without an SSH session running `k0s token create` by hand.
//
// It is the minting half of what `cctl worker-config` does: a token can only
// come from a node that runs the control plane, because that is where k0s and
// the cluster CA are. Turning the token into a node's configuration is cctl's
// job and stays there. See docs/adr/0004-management-api.md.
package token

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultExpiry is how long a minted token stays usable when the caller names no
// expiry. It is deliberately short: a join token is a credential, and an
// unexpired one lets a machine join the cluster.
const DefaultExpiry = time.Hour

// createTimeout bounds asking k0s to mint a token. It reads local PKI and
// returns at once; anything slower means k0s is not well.
const createTimeout = 30 * time.Second

// ErrNoControlPlane reports a node that cannot mint a join token.
//
// Only a controller can: a worker holds no cluster CA and has no k0s control
// plane to ask. Minting on a worker would fail obscurely, so it is refused with
// a reason instead.
var ErrNoControlPlane = errors.New(
	"this node runs no control plane, so it cannot mint a join token")

// Runner executes a command. It exists so this package can be tested without a
// k0s to ask.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager mints k0s join tokens from this node.
type Manager struct {
	// Run executes commands. Nil means actually execute them.
	Run Runner
}

// Create mints a join token for role, valid for expiry.
//
// role is "worker" or "controller"; expiry is a Go duration such as "1h". Both
// are validated by the caller, since they arrive over the API. The returned
// token is trimmed of the trailing newline k0s prints, so it can be embedded
// verbatim into a node's join.token.
func (m *Manager) Create(ctx context.Context, role, expiry string) ([]byte, error) {
	raw, err := m.run(ctx, "k0s", "token", "create", "--role="+role, "--expiry="+expiry)
	if err != nil {
		return nil, fmt.Errorf("asking k0s to mint a %s token: %w", role, err)
	}

	return []byte(strings.TrimSpace(string(raw))), nil
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}
