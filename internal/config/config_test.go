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
				Join: Join{TokenFrom: &SecretSource{URL: "http://example.com/t"}},
			},
			wantErr: "scheme must be https",
		},
		{
			name: "both token and tokenFrom",
			cfg: Config{
				Role: RoleWorker,
				Join: Join{Token: "x", TokenFrom: &SecretSource{URL: "https://e.com/t"}},
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

func TestValidateHA(t *testing.T) {
	base := func() Config {
		return Config{
			Role:    RoleController,
			Cluster: Cluster{Endpoint: "192.168.0.200"},
			Join:    Join{Token: "x"},
			HA: HA{
				Enabled:   true,
				VirtualIP: "192.168.0.200/24",
				AuthPass:  "s3cret",
			},
		}
	}

	t.Run("accepts a well-formed HA controller", func(t *testing.T) {
		cfg := base()
		cfg.ApplyDefaults()

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "single node cannot be HA",
			mutate:  func(c *Config) { c.Role = RoleSingle; c.Join = Join{} },
			wantErr: "one node by definition",
		},
		{
			name:    "workers cannot run CPLB",
			mutate:  func(c *Config) { c.Role = RoleWorker },
			wantErr: "only a controller can run control plane load balancing",
		},
		{
			name:    "virtual IP needs a prefix length",
			mutate:  func(c *Config) { c.HA.VirtualIP = "192.168.0.200" },
			wantErr: "must be an address with a prefix length",
		},
		{
			name:    "virtual IP is required",
			mutate:  func(c *Config) { c.HA.VirtualIP = "" },
			wantErr: "ha.virtualIP: required",
		},
		{
			// keepalived silently truncates to 8 characters, so a longer value
			// means two controllers can believe they share a password they do
			// not actually share.
			name:    "auth password longer than keepalived honours",
			mutate:  func(c *Config) { c.HA.AuthPass = "this-is-far-too-long" },
			wantErr: "keepalived uses only the first 8 characters",
		},
		{
			name:    "auth password is required",
			mutate:  func(c *Config) { c.HA.AuthPass = "" },
			wantErr: "ha.authPass: required",
		},
		{
			name: "auth password cannot be given twice",
			mutate: func(c *Config) {
				c.HA.AuthPassFrom = &SecretSource{URL: "https://e.com/p"}
			},
			wantErr: "not both",
		},
		{
			name:    "router ID out of range",
			mutate:  func(c *Config) { c.HA.VirtualRouterID = 300 },
			wantErr: "out of range",
		},
		{
			name:    "negative router ID",
			mutate:  func(c *Config) { c.HA.VirtualRouterID = -1 },
			wantErr: "out of range",
		},
		{
			name:    "unicast peers must be addresses",
			mutate:  func(c *Config) { c.HA.UnicastPeers = []string{"controller-2"} },
			wantErr: "is not an IP address",
		},
		{
			// Without an endpoint, clients would be told to use a single
			// controller's address and the virtual IP would buy nothing.
			name:    "endpoint is required",
			mutate:  func(c *Config) { c.Cluster.Endpoint = "" },
			wantErr: "cluster.endpoint: required when ha is enabled",
		},
		{
			name: "settings without enabled do nothing",
			mutate: func(c *Config) {
				c.HA = HA{VirtualIP: "192.168.0.200/24", AuthPass: "x"}
			},
			wantErr: "ha.enabled is false",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateNodeName(t *testing.T) {
	for name, wantErr := range map[string]string{
		"ctrl-1":                "",
		"corium-12db8c05":       "",
		"Ctrl-1":                "must be lowercase",
		"-leading":              "must be lowercase",
		"trailing-":             "must be lowercase",
		"under_score":           "must be lowercase",
		strings.Repeat("a", 64): "the limit is 63",
	} {
		cfg := Config{Role: RoleSingle, Node: Node{Name: name}}
		cfg.ApplyDefaults()

		err := cfg.Validate()

		if wantErr == "" {
			if err != nil {
				t.Errorf("Validate() with node.name %q = %v, want nil", name, err)
			}

			continue
		}

		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Validate() with node.name %q = %v, want it to contain %q", name, err, wantErr)
		}
	}
}

func TestVirtualRouterIDMayBeOmitted(t *testing.T) {
	// Zero means "let k0s assign one". Rejecting it would force every operator
	// to pick a number they have no reason to care about.
	cfg := Config{
		Role:    RoleController,
		Cluster: Cluster{Endpoint: "10.0.0.1"},
		Join:    Join{Token: "x"},
		HA: HA{
			Enabled:   true,
			VirtualIP: "10.0.0.1/24",
			AuthPass:  "pw12345",
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with no virtualRouterID = %v, want nil", err)
	}
}

func TestValidateUpgrades(t *testing.T) {
	valid := []UpgradePolicy{UpgradeNone, UpgradeDownload, UpgradeApply, ""}

	for _, policy := range valid {
		cfg := Config{Role: RoleSingle, Upgrades: Upgrades{Automatic: policy}}
		cfg.ApplyDefaults()

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with automatic %q = %v, want nil", policy, err)
		}
	}

	cfg := Config{Role: RoleSingle, Upgrades: Upgrades{Automatic: "yes"}}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "use none, download or apply") {
		t.Errorf("Validate() with automatic \"yes\" = %v, want a clear rejection", err)
	}
}

func TestUpgradesDefaultToOff(t *testing.T) {
	// A node must never reboot itself unless someone asked for it.
	cfg := &Config{Role: RoleSingle}
	cfg.ApplyDefaults()

	if cfg.Upgrades.Automatic != UpgradeNone {
		t.Errorf("Upgrades.Automatic = %q, want %q by default",
			cfg.Upgrades.Automatic, UpgradeNone)
	}
}

func TestScheduleWithoutAutomaticIsRejected(t *testing.T) {
	// Otherwise the setting silently does nothing.
	cfg := &Config{
		Role:     RoleSingle,
		Upgrades: Upgrades{Automatic: UpgradeNone, Schedule: "hourly"},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "nothing is scheduled") {
		t.Errorf("Validate() = %v, want it to flag a schedule that does nothing", err)
	}
}

func TestValidateRAID(t *testing.T) {
	base := func(arrays ...RAIDArray) Config {
		return Config{Role: RoleSingle, RAID: arrays}
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "array without a name",
			cfg:     base(RAIDArray{Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}}),
			wantErr: "raid[0].name: required",
		},
		{
			name: "unsupported level",
			cfg: base(RAIDArray{
				Name: "data", Level: 2, Devices: []string{"/dev/sdb", "/dev/sdc"},
			}),
			wantErr: "unsupported level 2",
		},
		{
			// mdadm would refuse this too, but it would refuse it at first boot
			// on a machine nobody is watching.
			name: "too few devices for the level",
			cfg: base(RAIDArray{
				Name: "data", Level: 5, Devices: []string{"/dev/sdb", "/dev/sdc"},
			}),
			wantErr: "RAID 5 needs at least 3 devices",
		},
		{
			name: "a mirror needs two devices",
			cfg: base(RAIDArray{
				Name: "data", Level: 1, Devices: []string{"/dev/sdb"},
			}),
			wantErr: "RAID 1 needs at least 2 devices",
		},
		{
			// A spare that can never be rebuilt into the array is a safety
			// margin someone thinks they have and does not.
			name: "spare on a stripe",
			cfg: base(RAIDArray{
				Name: "scratch", Level: 0,
				Devices: []string{"/dev/sdb", "/dev/sdc"},
				Spares:  []string{"/dev/sdd"},
			}),
			wantErr: "RAID 0 has no redundancy",
		},
		{
			name: "relative device path",
			cfg: base(RAIDArray{
				Name: "data", Level: 1, Devices: []string{"sdb", "/dev/sdc"},
			}),
			wantErr: "must be an absolute device path",
		},
		{
			// Building two arrays from one disk corrupts whichever is built
			// second, and does it silently.
			name: "device claimed by two arrays",
			cfg: base(
				RAIDArray{Name: "a", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}},
				RAIDArray{Name: "b", Level: 1, Devices: []string{"/dev/sdc", "/dev/sdd"}},
			),
			wantErr: `"/dev/sdc" is already claimed by raid[0]`,
		},
		{
			name: "duplicate array names",
			cfg: base(
				RAIDArray{Name: "data", Level: 1, Devices: []string{"/dev/sdb", "/dev/sdc"}},
				RAIDArray{Name: "data", Level: 1, Devices: []string{"/dev/sdd", "/dev/sde"}},
			),
			wantErr: "is used by more than one array",
		},
		{
			name: "unknown filesystem",
			cfg: base(RAIDArray{
				Name: "data", Level: 1, Filesystem: "btrfs",
				Devices: []string{"/dev/sdb", "/dev/sdc"},
			}),
			wantErr: `unknown value "btrfs"`,
		},
		{
			// Mounting an unformatted array is a wait for something that is
			// never going to appear.
			name: "mount point on an unformatted array",
			cfg: base(RAIDArray{
				Name: "raw", Level: 1, Filesystem: RAIDFilesystemNone,
				MountPoint: "/data",
				Devices:    []string{"/dev/sdb", "/dev/sdc"},
			}),
			wantErr: "there is nothing to mount",
		},
		{
			name: "relative mount point",
			cfg: base(RAIDArray{
				Name: "data", Level: 1, MountPoint: "data",
				Devices: []string{"/dev/sdb", "/dev/sdc"},
			}),
			wantErr: "must be an absolute path",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", test.wantErr)
			}

			if !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("Validate() = %q, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateAcceptsGoodRAID(t *testing.T) {
	configs := []Config{
		{Role: RoleSingle, RAID: []RAIDArray{{
			Name: "data", Level: 1,
			Devices:    []string{"/dev/disk/by-id/one", "/dev/disk/by-id/two"},
			MountPoint: "/var/lib/corium/data",
		}}},
		{Role: RoleSingle, RAID: []RAIDArray{{
			Name: "raw", Level: 10, Filesystem: RAIDFilesystemNone,
			Devices: []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"},
		}}},
		{Role: RoleSingle, RAID: []RAIDArray{{
			Name: "big", Level: 6, Filesystem: RAIDFilesystemXFS,
			Devices:    []string{"/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"},
			Spares:     []string{"/dev/sdf"},
			MountPoint: "/srv/data",
			Wipe:       true,
		}}},
	}

	for _, cfg := range configs {
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected a valid configuration: %v", err)
		}
	}
}
