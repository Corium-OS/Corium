package config

import (
	"strings"
	"testing"
)

func TestValidateBackup(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		backup  Backup
		wantErr string
	}{
		{
			// The one that matters: `k0s backup` refuses to run anywhere but a
			// controller, so this would be a timer that fails on every tick
			// while an operator believes the node is backed up.
			name:    "a worker has no control plane to snapshot",
			role:    RoleWorker,
			backup:  Backup{Enabled: true},
			wantErr: "runs no control plane",
		},
		{
			name:    "a configured block that is switched off",
			role:    RoleSingle,
			backup:  Backup{Path: "/srv/backups"},
			wantErr: "backup.enabled is false",
		},
		{
			name:    "a relative target path",
			role:    RoleSingle,
			backup:  Backup{Enabled: true, Path: "backups"},
			wantErr: "must be an absolute path",
		},
		{
			// The path is written into a quoted systemd Environment= line.
			name:    "a target path that could break out of the drop-in",
			role:    RoleSingle,
			backup:  Backup{Enabled: true, Path: "/srv/\"backups"},
			wantErr: "must not contain a quote",
		},
		{
			// The mistake people actually make: writing cron where systemd
			// wants a calendar expression.
			name:    "a cron expression",
			role:    RoleSingle,
			backup:  Backup{Enabled: true, Schedule: "0 3 * * *"},
			wantErr: "is not a systemd OnCalendar expression",
		},
		{
			name:    "prose instead of a schedule",
			role:    RoleSingle,
			backup:  Backup{Enabled: true, Schedule: "every night at 3"},
			wantErr: "is not a systemd OnCalendar expression",
		},
		{
			name:    "a negative retention count",
			role:    RoleControllerWorker,
			backup:  Backup{Enabled: true, Keep: -1},
			wantErr: "must not be negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Role: tc.role, Backup: tc.backup}
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

// TestValidateBackupReportsEveryProblemAtOnce checks the contract the rest of
// the file keeps: an operator iterating through a reboot cycle should not
// discover their mistakes one boot at a time.
func TestValidateBackupReportsEveryProblemAtOnce(t *testing.T) {
	cfg := &Config{
		Role:   RoleWorker,
		Backup: Backup{Enabled: true, Path: "relative", Schedule: "0 3 * * *", Keep: -3},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want several problems")
	}

	for _, want := range []string{
		"runs no control plane",
		"must be an absolute path",
		"is not a systemd OnCalendar expression",
		"must not be negative",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestValidateBackupAcceptsAGoodBlock(t *testing.T) {
	for _, backup := range []Backup{
		{Enabled: true},
		{Enabled: true, Schedule: "*-*-* 03:00:00", Path: "/mnt/backups", Keep: 30},
		{Enabled: true, Schedule: "Mon,Fri"},
		{Enabled: true, Schedule: "weekly"},
		{Enabled: true, Schedule: "Mon *-*-* 02:30:00"},
	} {
		cfg := &Config{Role: RoleController, Backup: backup}
		cfg.ApplyDefaults()

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with %+v = %v, want nil", backup, err)
		}
	}
}

// TestBackupDefaultsOnlyApplyWhenEnabled is what lets validation tell a block
// somebody wrote from one nobody did: if ApplyDefaults filled these in
// unconditionally, every worker in the fleet would carry a backup block and be
// rejected for it.
func TestBackupDefaultsOnlyApplyWhenEnabled(t *testing.T) {
	cfg := &Config{Role: RoleWorker, Join: Join{Token: "a-token"}}
	cfg.ApplyDefaults()

	if cfg.Backup != (Backup{}) {
		t.Errorf("ApplyDefaults() left backup = %+v, want the zero value", cfg.Backup)
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for a worker with no backup block", err)
	}

	enabled := &Config{Role: RoleSingle, Backup: Backup{Enabled: true}}
	enabled.ApplyDefaults()

	want := Backup{
		Enabled:  true,
		Schedule: DefaultBackupSchedule,
		Path:     DefaultBackupPath,
		Keep:     DefaultBackupKeep,
	}

	if enabled.Backup != want {
		t.Errorf("ApplyDefaults() = %+v, want %+v", enabled.Backup, want)
	}
}

// TestBackupIsImmutableDayTwo pins ADR 8: the safe day-two subset grows through
// an ADR, not by a field quietly turning out to be reconcilable.
func TestBackupIsImmutableDayTwo(t *testing.T) {
	old := &Config{Role: RoleSingle}
	next := &Config{Role: RoleSingle, Backup: Backup{Enabled: true}}

	plan := PlanReconcile(old, next)

	found := false

	for _, field := range plan.Immutable {
		if field == "backup" {
			found = true
		}
	}

	if !found {
		t.Errorf("PlanReconcile() immutable = %v, want it to name backup", plan.Immutable)
	}
}
