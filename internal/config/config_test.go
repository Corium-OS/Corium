package config

import (
	"errors"
	"strings"
	"testing"
)

func TestParseKeepsCloudInitKeys(t *testing.T) {
	// The regression this guards: strict decoding applied to the whole
	// document rejects ordinary cloud-config keys, which would make Corium
	// incompatible with the mechanism it is built on.
	doc := []byte(`#cloud-config
corium:
  role: single
users:
  - name: core
    groups: [wheel]
write_files:
  - path: /etc/hosts
runcmd:
  - [ echo, hello ]
`)

	cfg, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	if cfg.Role != RoleSingle {
		t.Errorf("Role = %q, want %q", cfg.Role, RoleSingle)
	}
}

func TestParseRejectsUnknownCoriumKeys(t *testing.T) {
	// Inside our own block, an unrecognised key is a typo. Ignoring it means an
	// operator's setting silently never takes effect.
	doc := []byte(`#cloud-config
corium:
  role: single
  rôle: single
`)

	if _, err := Parse(doc); err == nil {
		t.Fatal("Parse() error = nil, want an error for an unknown key")
	}
}

func TestParseWithoutCoriumBlock(t *testing.T) {
	doc := []byte("#cloud-config\nusers: []\n")

	_, err := Parse(doc)
	if err == nil || !strings.Contains(err.Error(), ErrNoCoriumBlock.Error()) {
		t.Fatalf("Parse() error = %v, want %v", err, ErrNoCoriumBlock)
	}
}

func TestApplyDefaults(t *testing.T) {
	tests := []struct {
		name        string
		role        Role
		wantStorage StorageType
	}{
		{"single node uses sqlite", RoleSingle, StorageSQLite},
		{"controller uses etcd", RoleController, StorageEtcd},
		{"controller+worker uses etcd", RoleControllerWorker, StorageEtcd},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Role: tc.role}
			cfg.ApplyDefaults()

			if cfg.Storage.Type != tc.wantStorage {
				t.Errorf("Storage.Type = %q, want %q", cfg.Storage.Type, tc.wantStorage)
			}

			if cfg.Network.PodCIDR != DefaultPodCIDR {
				t.Errorf("PodCIDR = %q, want %q", cfg.Network.PodCIDR, DefaultPodCIDR)
			}
		})
	}
}

func TestApplyDefaultsIsIdempotent(t *testing.T) {
	cfg := &Config{Role: RoleSingle, Cluster: Cluster{Name: "kept"}}
	cfg.ApplyDefaults()
	first := *cfg
	cfg.ApplyDefaults()

	if cfg.Cluster.Name != first.Cluster.Name || cfg.Network.PodCIDR != first.Network.PodCIDR {
		t.Error("ApplyDefaults() is not idempotent")
	}

	if cfg.Cluster.Name != "kept" {
		t.Errorf("ApplyDefaults() overwrote an explicit value: %q", cfg.Cluster.Name)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing role",
			cfg:     Config{},
			wantErr: "role: required",
		},
		{
			name:    "unknown role",
			cfg:     Config{Role: "master"},
			wantErr: `unknown value "master"`,
		},
		{
			name:    "worker without a join token",
			cfg:     Config{Role: RoleWorker},
			wantErr: "join: required for role worker",
		},
		{
			name: "join token over plain http",
			cfg: Config{
				Role: RoleWorker,
				Join: Join{TokenFrom: &TokenSource{URL: "http://example.com/t"}},
			},
			wantErr: "scheme must be https",
		},
		{
			name: "both token and tokenFrom",
			cfg: Config{
				Role: RoleWorker,
				Join: Join{Token: "x", TokenFrom: &TokenSource{URL: "https://e.com/t"}},
			},
			wantErr: "not both",
		},
		{
			name: "overlapping CIDRs",
			cfg: Config{
				Role:    RoleSingle,
				Network: Network{PodCIDR: "10.0.0.0/8", ServiceCIDR: "10.96.0.0/12"},
			},
			wantErr: "overlaps",
		},
		{
			name: "sqlite with multiple controllers",
			cfg: Config{
				Role:    RoleController,
				Storage: Storage{Type: StorageSQLite},
				Join:    Join{Token: "x"},
			},
			wantErr: "cannot back a multi-controller cluster",
		},
		{
			name: "addon on a worker",
			cfg: Config{
				Role:   RoleWorker,
				Join:   Join{Token: "x"},
				Addons: []Addon{{Name: "a", Chart: "r/c"}},
			},
			wantErr: "only a controller installs add-ons",
		},
		{
			name: "addon with an undeclared repository",
			cfg: Config{
				Role:   RoleSingle,
				Addons: []Addon{{Name: "a", Chart: "ghost/chart"}},
			},
			wantErr: `repository "ghost" is not declared`,
		},
		{
			name: "invalid taint effect",
			cfg: Config{
				Role: RoleSingle,
				Node: Node{Taints: []Taint{{Key: "k", Effect: "Nope"}}},
			},
			wantErr: "must be NoSchedule",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want one containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAcceptsGoodConfigs(t *testing.T) {
	for _, role := range []Role{RoleSingle, RoleControllerWorker} {
		cfg := &Config{Role: role}
		cfg.ApplyDefaults()

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() for role %q = %v, want nil", role, err)
		}
	}
}

func TestParseStandaloneDocument(t *testing.T) {
	// The shape an operator writes in /etc/corium/config.yaml or serves over
	// PXE, where wrapping the configuration in a cloud-config would be ceremony
	// for its own sake.
	doc := []byte(`role: worker
cluster:
  name: edge
join:
  token: abc123
`)

	cfg, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	if cfg.Role != RoleWorker {
		t.Errorf("Role = %q, want %q", cfg.Role, RoleWorker)
	}

	if cfg.Cluster.Name != "edge" {
		t.Errorf("Cluster.Name = %q, want edge", cfg.Cluster.Name)
	}
}

func TestParsePrefersEmbeddedBlock(t *testing.T) {
	// A document with both shapes is ambiguous. The corium block wins, because
	// a top-level role: in a cloud-config is far more likely to be some other
	// tool's key than a Corium configuration.
	doc := []byte(`#cloud-config
role: worker
corium:
  role: single
`)

	cfg, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Role != RoleSingle {
		t.Errorf("Role = %q, want the corium block's value %q", cfg.Role, RoleSingle)
	}
}

func TestParseIgnoresUnrelatedDocuments(t *testing.T) {
	// A plain cloud-config with no Corium configuration must not be mistaken
	// for one; the node is simply not a Kubernetes node.
	for _, doc := range []string{
		"#cloud-config\nusers: []\n",
		"",
		"#cloud-config\nwrite_files:\n  - path: /etc/hosts\n",
	} {
		if _, err := Parse([]byte(doc)); !errors.Is(err, ErrNoCoriumBlock) {
			t.Errorf("Parse(%q) error = %v, want ErrNoCoriumBlock", doc, err)
		}
	}
}
