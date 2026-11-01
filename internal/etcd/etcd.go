// Package etcd reports this controller's own etcd member: whether it is
// healthy, and the membership it sees.
//
// It judges nothing about the other members. k0s binds etcd's client port to
// 127.0.0.1, so a member's health can only be read on its own node; whether the
// cluster as a whole is fit to lose one is for cctl to work out from every
// controller's answer. See ADR 4.
package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Corium-OS/Corium/internal/k0s"
)

// Storage values a node can report. Only StorageEtcd has a member to check.
const (
	StorageEtcd     = "etcd"
	StorageKine     = "kine"
	StorageExternal = "external"
)

// Paths k0s writes its etcd client credentials to.
const (
	PKIDir         = "/var/lib/k0s/pki"
	healthEndpoint = "https://127.0.0.1:2379/health"
)

// timeout bounds each local call; etcd answering slower than this is the fault.
const timeout = 10 * time.Second

// ErrNoControlPlane reports a worker, which runs no etcd.
var ErrNoControlPlane = errors.New("this node runs no control plane, so it has no etcd member")

// Member is one etcd member as k0s lists it.
type Member struct {
	Name    string `json:"name"`
	PeerURL string `json:"peerURL"`
}

// Status is what this node can say about its own etcd member.
type Status struct {
	// Storage is the control plane datastore. Every other field is empty
	// unless it is StorageEtcd.
	Storage string `json:"storage"`

	// Member is this node's member name.
	Member string `json:"member,omitempty"`

	// Healthy is etcd's own /health verdict, a linearizable read: true means
	// this member reached a quorum.
	Healthy bool   `json:"healthy"`
	Reason  string `json:"reason,omitempty"`

	// Members is the membership this member sees, sorted by name.
	Members []Member `json:"members,omitempty"`
}

// Runner executes a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager reads the local etcd member. Every field is for tests; zero values
// mean the real node.
type Manager struct {
	Run        Runner
	Probe      func(ctx context.Context) error
	Hostname   func() (string, error)
	ConfigPath string
}

// Status reports this node's member. An error means the node could not tell,
// not that the member is unhealthy.
func (m *Manager) Status(ctx context.Context) (*Status, error) {
	spec, err := m.storage()
	if err != nil {
		return nil, err
	}

	if spec.storage != StorageEtcd {
		return &Status{Storage: spec.storage}, nil
	}

	status := &Status{Storage: StorageEtcd, Member: spec.name}

	if status.Member == "" {
		if status.Member, err = m.hostname(); err != nil {
			return nil, fmt.Errorf("reading the hostname etcd names this member after: %w", err)
		}
	}

	// An unhealthy member is an answer, not a failure to give one.
	if members, err := m.healthyMembers(ctx); err != nil {
		status.Reason = err.Error()
	} else {
		status.Healthy = true
		status.Members = members
	}

	return status, nil
}

func (m *Manager) healthyMembers(ctx context.Context) ([]Member, error) {
	if err := m.probe(ctx); err != nil {
		return nil, err
	}

	return m.members(ctx)
}

type storageSpec struct {
	storage string
	name    string
}

// storage reads the rendered k0s.yaml, which carries any k0s.patch, rather
// than the corium: document.
func (m *Manager) storage() (storageSpec, error) {
	path := m.ConfigPath
	if path == "" {
		path = k0s.ConfigPath
	}

	raw, err := os.ReadFile(path) //nolint:gosec // G304: a fixed path, or a test's.
	if err != nil {
		return storageSpec{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var doc struct {
		Spec struct {
			Storage struct {
				Type string `yaml:"type"`
				Etcd struct {
					ExternalCluster any               `yaml:"externalCluster"`
					ExtraArgs       map[string]string `yaml:"extraArgs"`
				} `yaml:"etcd"`
			} `yaml:"storage"`
		} `yaml:"spec"`
	}

	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return storageSpec{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	storage := doc.Spec.Storage

	switch {
	case storage.Type != "" && storage.Type != StorageEtcd:
		return storageSpec{storage: storage.Type}, nil
	case storage.Etcd.ExternalCluster != nil:
		return storageSpec{storage: StorageExternal}, nil
	default:
		// k0s names the member --name when extraArgs sets it, the hostname otherwise.
		return storageSpec{storage: StorageEtcd, name: storage.Etcd.ExtraArgs["name"]}, nil
	}
}

func (m *Manager) hostname() (string, error) {
	if m.Hostname != nil {
		return m.Hostname()
	}

	return os.Hostname()
}

func (m *Manager) members(ctx context.Context) ([]Member, error) {
	raw, err := m.run(ctx, "k0s", "etcd", "member-list")
	if err != nil {
		return nil, fmt.Errorf("listing etcd members: %w", err)
	}

	var list struct {
		Members map[string]string `json:"members"`
	}

	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing the etcd member list: %w", err)
	}

	members := make([]Member, 0, len(list.Members))
	for name, peer := range list.Members {
		members = append(members, Member{Name: name, PeerURL: peer})
	}

	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

	return members, nil
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
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

func (m *Manager) probe(ctx context.Context) error {
	if m.Probe != nil {
		return m.Probe(ctx)
	}

	return probeHealth(ctx, PKIDir)
}

// probeHealth asks the local member's /health, with the client certificate k0s
// gives kube-apiserver.
func probeHealth(ctx context.Context, pki string) error {
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(pki, "apiserver-etcd-client.crt"),
		filepath.Join(pki, "apiserver-etcd-client.key"))
	if err != nil {
		return fmt.Errorf("loading the etcd client certificate: %w", err)
	}

	ca, err := os.ReadFile(filepath.Join(pki, "etcd", "ca.crt")) //nolint:gosec // G304: k0s's PKI.
	if err != nil {
		return fmt.Errorf("reading the etcd CA: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("the etcd CA holds no certificate")
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}},
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthEndpoint, nil)
	if err != nil {
		return fmt.Errorf("building the etcd health request: %w", err)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("reaching etcd: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	return parseHealth(response.StatusCode, response.Body)
}

// parseHealth reads etcd's {"health":"true","reason":""}, served with 503 when false.
func parseHealth(code int, body io.Reader) error {
	var health struct {
		Health string `json:"health"`
		Reason string `json:"reason"`
	}

	raw, err := io.ReadAll(io.LimitReader(body, 4<<10))
	if err != nil {
		return fmt.Errorf("reading etcd's health: %w", err)
	}

	if err := json.Unmarshal(raw, &health); err != nil {
		return fmt.Errorf("etcd answered %d with no health verdict", code)
	}

	if health.Health != "true" {
		if health.Reason == "" {
			health.Reason = "no reason given"
		}

		return fmt.Errorf("etcd reports itself unhealthy: %s", health.Reason)
	}

	return nil
}
