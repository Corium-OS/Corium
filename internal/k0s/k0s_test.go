package k0s

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/qjoly/corium/internal/config"
)

func render(t *testing.T, cfg *config.Config) map[string]any {
	t.Helper()
	cfg.ApplyDefaults()

	data, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v", err)
	}

	return out
}

func spec(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()

	s, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("rendered config has no spec: %v", doc)
	}

	return s
}

func TestRenderIsDeterministic(t *testing.T) {
	cfg := &config.Config{
		Role: config.RoleControllerWorker,
		Node: config.Node{Labels: map[string]string{
			"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
		}},
	}
	cfg.ApplyDefaults()

	first, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	// Map iteration order in Go is randomised per run, so a single repetition
	// is not enough to catch an ordering bug.
	for i := 0; i < 20; i++ {
		next, err := Render(cfg)
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}

		if string(next) != string(first) {
			t.Fatal("Render() is not deterministic across calls")
		}
	}
}

func TestRenderEndpointIsAlwaysInSANs(t *testing.T) {
	doc := render(t, &config.Config{
		Role:    config.RoleController,
		Cluster: config.Cluster{Endpoint: "k8s.example.com"},
		Join:    config.Join{Token: "x"},
	})

	api, ok := spec(t, doc)["api"].(map[string]any)
	if !ok {
		t.Fatal("rendered config has no spec.api")
	}

	sans, ok := api["sans"].([]any)
	if !ok {
		t.Fatalf("spec.api.sans = %v, want a list", api["sans"])
	}

	for _, san := range sans {
		if san == "k8s.example.com" {
			return
		}
	}

	t.Errorf("spec.api.sans = %v, want it to contain the endpoint", sans)
}

func TestRenderSingleNodeUsesKine(t *testing.T) {
	doc := render(t, &config.Config{Role: config.RoleSingle})

	storage, ok := spec(t, doc)["storage"].(map[string]any)
	if !ok {
		t.Fatal("rendered config has no spec.storage")
	}

	// "sqlite" is Corium's name for the operator's choice; k0s calls the
	// mechanism kine, and that is what must reach the file.
	if storage["type"] != "kine" {
		t.Errorf("spec.storage.type = %v, want kine", storage["type"])
	}
}

func TestPatchMergesRatherThanReplaces(t *testing.T) {
	doc := render(t, &config.Config{
		Role:    config.RoleControllerWorker,
		Cluster: config.Cluster{Endpoint: "10.0.0.10"},
		K0s: config.K0s{Patch: map[string]any{
			"spec": map[string]any{
				"api": map[string]any{
					"extraArgs": map[string]any{"audit-log-path": "/var/log/audit"},
				},
			},
		}},
	})

	api, ok := spec(t, doc)["api"].(map[string]any)
	if !ok {
		t.Fatal("rendered config has no spec.api")
	}

	if api["externalAddress"] != "10.0.0.10" {
		t.Errorf("the patch clobbered a sibling key: externalAddress = %v",
			api["externalAddress"])
	}

	if api["extraArgs"] == nil {
		t.Error("the patch did not apply: spec.api.extraArgs is missing")
	}
}

func TestPatchCanOverrideCoriumValues(t *testing.T) {
	// The escape hatch is only an escape hatch if it can override values
	// Corium considers load-bearing.
	doc := render(t, &config.Config{
		Role: config.RoleSingle,
		K0s: config.K0s{Patch: map[string]any{
			"spec": map[string]any{
				"telemetry": map[string]any{"enabled": true},
			},
		}},
	})

	telemetry, ok := spec(t, doc)["telemetry"].(map[string]any)
	if !ok {
		t.Fatal("rendered config has no spec.telemetry")
	}

	if telemetry["enabled"] != true {
		t.Errorf("spec.telemetry.enabled = %v, want the patched value true",
			telemetry["enabled"])
	}
}

func TestInstallArgs(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		hasToken bool
		want     []string
	}{
		{
			name: "single",
			cfg:  config.Config{Role: config.RoleSingle},
			want: []string{"install", "controller", "--config", ConfigPath, "--single"},
		},
		{
			name: "controller+worker drops the default taint",
			cfg:  config.Config{Role: config.RoleControllerWorker},
			want: []string{"install", "controller", "--config", ConfigPath,
				"--enable-worker", "--no-taints"},
		},
		{
			name:     "worker with a token",
			cfg:      config.Config{Role: config.RoleWorker},
			hasToken: true,
			want:     []string{"install", "worker", "--token-file", TokenPath},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InstallArgs(&tc.cfg, tc.hasToken)

			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("InstallArgs() =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}

func TestInstallArgsSortsLabels(t *testing.T) {
	cfg := &config.Config{
		Role: config.RoleWorker,
		Node: config.Node{Labels: map[string]string{"z": "1", "a": "2", "m": "3"}},
	}

	got := strings.Join(InstallArgs(cfg, true), " ")
	if !strings.Contains(got, "a=2,m=3,z=1") {
		t.Errorf("InstallArgs() = %q, want sorted labels", got)
	}
}

func TestServiceName(t *testing.T) {
	for role, want := range map[config.Role]string{
		config.RoleSingle:           "k0scontroller.service",
		config.RoleController:       "k0scontroller.service",
		config.RoleControllerWorker: "k0scontroller.service",
		config.RoleWorker:           "k0sworker.service",
	} {
		if got := ServiceName(role); got != want {
			t.Errorf("ServiceName(%q) = %q, want %q", role, got, want)
		}
	}
}
