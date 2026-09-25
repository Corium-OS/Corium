package config

import (
	"strings"
	"testing"
)

func TestValidateManifests(t *testing.T) {
	tests := []struct {
		name      string
		role      Role
		manifests []ManifestStack
		wantErr   string
	}{
		{
			name: "a worker cannot apply manifests",
			role: RoleWorker,
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "pool.yaml", Content: "kind: IPAddressPool\n"}},
			}},
			wantErr: "only a controller applies bundled manifests",
		},
		{
			name:      "missing stack name",
			manifests: []ManifestStack{{Files: []ManifestFile{{Name: "a.yaml", Content: "kind: X\n"}}}},
			wantErr:   "manifests[0].name: required",
		},
		{
			name: "stack name with a slash",
			manifests: []ManifestStack{{
				Name:  "metallb/pools",
				Files: []ManifestFile{{Name: "a.yaml", Content: "kind: X\n"}},
			}},
			wantErr: "must be a plain directory name",
		},
		{
			// The deployer does not descend into nested directories, so a name
			// that climbs out of the stack does not land somewhere else useful.
			name: "stack name that escapes the directory",
			manifests: []ManifestStack{{
				Name:  "..",
				Files: []ManifestFile{{Name: "a.yaml", Content: "kind: X\n"}},
			}},
			wantErr: "must be a plain directory name",
		},
		{
			name: "hidden stack name",
			manifests: []ManifestStack{{
				Name:  ".metallb",
				Files: []ManifestFile{{Name: "a.yaml", Content: "kind: X\n"}},
			}},
			wantErr: "must be a plain directory name",
		},
		{
			name: "duplicate stack names",
			manifests: []ManifestStack{
				{Name: "metallb", Files: []ManifestFile{{Name: "a.yaml", Content: "kind: X\n"}}},
				{Name: "metallb", Files: []ManifestFile{{Name: "b.yaml", Content: "kind: Y\n"}}},
			},
			wantErr: "used by more than one stack",
		},
		{
			name:      "stack with no files",
			manifests: []ManifestStack{{Name: "metallb"}},
			wantErr:   "at least one file is required",
		},
		{
			name: "missing file name",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Content: "kind: X\n"}},
			}},
			wantErr: "manifests[0].files[0].name: required",
		},
		{
			name: "file name with a slash",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "sub/pool.yaml", Content: "kind: X\n"}},
			}},
			wantErr: "must be a plain file name",
		},
		{
			// k0s reads .yaml and no other extension, so .yml is the spelling
			// that fails without ever saying so.
			name: "yml is not read by the deployer",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "pool.yml", Content: "kind: X\n"}},
			}},
			wantErr: "must end in .yaml",
		},
		{
			name: "file without an extension",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "pool", Content: "kind: X\n"}},
			}},
			wantErr: "must end in .yaml",
		},
		{
			name: "duplicate file names in one stack",
			manifests: []ManifestStack{{
				Name: "metallb",
				Files: []ManifestFile{
					{Name: "pool.yaml", Content: "kind: X\n"},
					{Name: "pool.yaml", Content: "kind: Y\n"},
				},
			}},
			wantErr: "is declared more than once in this stack",
		},
		{
			name: "empty content",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "pool.yaml", Content: "  \n"}},
			}},
			wantErr: "manifests[0].files[0].content: required",
		},
		{
			name: "content that is not YAML",
			manifests: []ManifestStack{{
				Name:  "metallb",
				Files: []ManifestFile{{Name: "pool.yaml", Content: "kind: [unterminated\n"}},
			}},
			wantErr: "not valid YAML",
		},
		{
			name: "content whose second document is not YAML",
			manifests: []ManifestStack{{
				Name: "metallb",
				Files: []ManifestFile{{
					Name:    "pool.yaml",
					Content: "kind: IPAddressPool\n---\nkind: \"unterminated\n",
				}},
			}},
			wantErr: "not valid YAML",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			role := tc.role
			if role == "" {
				role = RoleSingle
			}

			cfg := &Config{Role: role, Manifests: tc.manifests}
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

func TestValidateManifestsAcceptsAGoodStack(t *testing.T) {
	cfg := &Config{
		Role: RoleControllerWorker,
		Manifests: []ManifestStack{{
			Name: "metallb-config",
			Files: []ManifestFile{
				{
					// Several documents in one file, the way kubectl accepts
					// them: a single-document decode would reject this.
					Name: "pools.yaml",
					Content: "apiVersion: metallb.io/v1beta1\n" +
						"kind: IPAddressPool\n" +
						"metadata:\n  name: default\n  namespace: metallb-system\n" +
						"spec:\n  addresses:\n    - 192.0.2.10-192.0.2.20\n" +
						"---\n" +
						"apiVersion: metallb.io/v1beta1\n" +
						"kind: L2Advertisement\n" +
						"metadata:\n  name: default\n  namespace: metallb-system\n",
				},
				{Name: "storageclass.yaml", Content: "kind: StorageClass\n"},
			},
		}},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// Two stacks may repeat a file name: they are separate directories, and the
// deployer treats each as its own stack.
func TestValidateManifestsAllowsTheSameFileNameInTwoStacks(t *testing.T) {
	cfg := &Config{
		Role: RoleSingle,
		Manifests: []ManifestStack{
			{Name: "one", Files: []ManifestFile{{Name: "config.yaml", Content: "kind: X\n"}}},
			{Name: "two", Files: []ManifestFile{{Name: "config.yaml", Content: "kind: Y\n"}}},
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// Every problem at once, so an operator iterating through a reboot cycle does
// not discover them one boot at a time.
func TestValidateManifestsReportsEveryProblem(t *testing.T) {
	cfg := &Config{
		Role: RoleSingle,
		Manifests: []ManifestStack{{
			Name: "../escape",
			Files: []ManifestFile{
				{Name: "a.yml", Content: "kind: X\n"},
				{Name: "b.yaml", Content: "kind: [oops\n"},
			},
		}},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want three problems")
	}

	for _, want := range []string{"plain directory name", "must end in .yaml", "not valid YAML"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}
