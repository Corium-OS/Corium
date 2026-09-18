// Package kubeconfig hands over the credentials k0s minted for a cluster's
// administrator, pointed at an address that will keep working.
//
// This is the one Kubernetes-adjacent thing the management API does, and it is
// here because there is no other answer: the alternative is an operator with
// SSH on a controller running `cat` on a file, which is the shape of problem
// the API exists to remove. Everything else about Kubernetes stays `kubectl`'s
// job. See docs/adr/0004-management-api.md.
package kubeconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPort is the Kubernetes API server's, which k0s does not move.
const DefaultPort = "6443"

// fetchTimeout bounds asking k0s for the file. It reads local PKI and returns
// immediately; anything slower means k0s is not well.
const fetchTimeout = 30 * time.Second

// ErrNoControlPlane reports a node that has no administrator credentials to
// give.
//
// Only a controller has them locally. A worker holds kubelet credentials,
// which are not an administrator's and would not be what anybody asking for a
// kubeconfig wanted.
var ErrNoControlPlane = errors.New(
	"this node runs no control plane, so it has no cluster administrator credentials")

// Runner executes a command. It exists so this package can be tested without a
// k0s to ask.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager fetches a cluster's administrator kubeconfig from this node.
type Manager struct {
	// Run executes commands. Nil means actually execute them.
	Run Runner
}

// Admin returns the kubeconfig, with its server address rewritten to server.
//
// An empty server leaves whatever k0s wrote, which is this node's own address.
// That is right for a single node and wrong for a cluster with a virtual IP,
// which is why the caller passes one rather than this deciding.
func (m *Manager) Admin(ctx context.Context, server string) ([]byte, error) {
	raw, err := m.run(ctx, "k0s", "kubeconfig", "admin")
	if err != nil {
		return nil, fmt.Errorf("asking k0s for the admin kubeconfig: %w", err)
	}

	if server == "" {
		return raw, nil
	}

	return Retarget(raw, server)
}

// Retarget rewrites every cluster's server address.
//
// It parses rather than substitutes. A kubeconfig carries a certificate
// authority in base64, and any expression loose enough to catch the server
// line in one file is loose enough to corrupt that in another.
//
// Key order is not preserved, because YAML mappings decoded into Go maps do
// not carry one. A kubeconfig is read by tools rather than by people, so this
// costs nothing worth the machinery to avoid.
func Retarget(raw []byte, server string) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parsing the kubeconfig: %w", err)
	}

	clusters, ok := document["clusters"].([]any)
	if !ok || len(clusters) == 0 {
		return nil, errors.New("the kubeconfig names no clusters")
	}

	url := ServerURL(server)
	rewritten := 0

	for _, entry := range clusters {
		cluster, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		inner, ok := cluster["cluster"].(map[string]any)
		if !ok {
			continue
		}

		inner["server"] = url
		rewritten++
	}

	if rewritten == 0 {
		// Rather than returning a file that looks retargeted and is not.
		return nil, errors.New("the kubeconfig has no server address to rewrite")
	}

	out, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("re-encoding the kubeconfig: %w", err)
	}

	return out, nil
}

// ServerURL turns what somebody typed into what a kubeconfig wants.
//
// An address, a host and port, or a whole URL are all things an operator will
// reasonably pass, and all three mean the same thing here.
func ServerURL(server string) string {
	server = strings.TrimSpace(server)

	if strings.Contains(server, "://") {
		return server
	}

	// An IPv6 literal without brackets would otherwise have its last group
	// read as a port.
	if ip := net.ParseIP(server); ip != nil && ip.To4() == nil {
		return "https://" + net.JoinHostPort(server, DefaultPort)
	}

	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, DefaultPort)
	}

	return "https://" + server
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
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
