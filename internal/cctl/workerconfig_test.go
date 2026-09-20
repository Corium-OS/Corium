package cctl

import (
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
	"gopkg.in/yaml.v3"
)

// decodeWorker parses a rendered block back into a Config, asserting it round
// trips through the same schema a node parses.
func decodeWorker(t *testing.T, raw []byte) config.Config {
	t.Helper()

	var document struct {
		Corium config.Config `yaml:"corium"`
	}

	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the rendered block does not parse: %v\n%s", err, raw)
	}

	return document.Corium
}

func TestRenderWorkerConfigMinimal(t *testing.T) {
	raw, err := RenderWorkerConfig("a-join-token", "", nil)
	if err != nil {
		t.Fatalf("RenderWorkerConfig() error = %v", err)
	}

	cfg := decodeWorker(t, raw)

	if cfg.Role != config.RoleWorker {
		t.Errorf("role = %q, want worker", cfg.Role)
	}

	if cfg.Join.Token != "a-join-token" {
		t.Errorf("token = %q, want it inline", cfg.Join.Token)
	}

	if cfg.Node.Name != "" || len(cfg.Node.Labels) != 0 {
		t.Errorf("node was populated without being asked: %+v", cfg.Node)
	}

	// It is a corium: block for a cloud-config, at the two-space indent every
	// example uses.
	s := string(raw)
	if !strings.HasPrefix(s, "corium:\n  role: worker\n") {
		t.Errorf("block is not the expected shape:\n%s", s)
	}

	if !strings.Contains(s, "\n  join:\n    token: a-join-token\n") {
		t.Errorf("token is not nested under join at the expected indent:\n%s", s)
	}
}

func TestRenderWorkerConfigCarriesNameAndSortedLabels(t *testing.T) {
	raw, err := RenderWorkerConfig("tok", "worker-1", map[string]string{"b": "two", "a": "one"})
	if err != nil {
		t.Fatalf("RenderWorkerConfig() error = %v", err)
	}

	cfg := decodeWorker(t, raw)

	if cfg.Node.Name != "worker-1" {
		t.Errorf("name = %q, want worker-1", cfg.Node.Name)
	}

	if cfg.Node.Labels["a"] != "one" || cfg.Node.Labels["b"] != "two" {
		t.Errorf("labels = %v, want both carried through", cfg.Node.Labels)
	}

	// Deterministic output: identical input must render identically, so labels
	// are emitted in sorted key order.
	s := string(raw)
	if strings.Index(s, "a:") > strings.Index(s, "b:") {
		t.Errorf("labels are not sorted:\n%s", s)
	}
}

func TestRenderWorkerConfigRejectsNoToken(t *testing.T) {
	if _, err := RenderWorkerConfig("", "", nil); err == nil {
		t.Fatal("RenderWorkerConfig() error = nil, want one for an empty token")
	}
}

func TestRenderWorkerConfigRejectsAConfigANodeWouldReject(t *testing.T) {
	// A node name Kubernetes will not accept must fail here rather than produce a
	// block that boots a node into a validation error.
	if _, err := RenderWorkerConfig("tok", "Not_A_Valid_Name", nil); err == nil {
		t.Fatal("RenderWorkerConfig() error = nil, want a validation error")
	}
}
