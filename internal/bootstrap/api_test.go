package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func enabled(v bool) *bool { return &v }

func TestGateOnEnrolment(t *testing.T) {
	tests := []struct {
		name    string
		api     config.API
		wantErr error
	}{
		{
			// The overwhelming majority of nodes, including every one
			// provisioned before the API was designed.
			name: "no api block proceeds",
			api:  config.API{},
		},
		{
			name: "an explicit refusal proceeds",
			api:  config.API{Enabled: enabled(false)},
		},
		{
			name: "a configured CA proceeds",
			api:  config.API{OperatorCA: "-----BEGIN CERTIFICATE-----"},
		},
		{
			name:    "maintenance mode stops",
			api:     config.API{Enabled: enabled(true)},
			wantErr: errUnclaimed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := gateOnEnrolment(&config.Config{Role: config.RoleWorker, API: tc.api})

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("gateOnEnrolment() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestUnclaimedNodeStopsBeforeMutatingAnything(t *testing.T) {
	// The ordering is the point of the gate rather than a detail: a node that
	// is going to refuse to join must not have renamed itself or assembled a
	// RAID array on the way to refusing.
	doc := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(doc, []byte(`#cloud-config
corium:
  role: worker
  join:
    token: placeholder
  api:
    enabled: true
`), 0o600); err != nil {
		t.Fatalf("writing document: %v", err)
	}

	err := Run(t.Context(), Options{ConfigPath: doc, DryRun: true})
	if !errors.Is(err, errUnclaimed) {
		t.Fatalf("Run() = %v, want %v", err, errUnclaimed)
	}
}
