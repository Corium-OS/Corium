package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
)

// TestCheckExecuting covers the swap's last word: the binary systemd started,
// not the merged metadata. The two disagreed when the merge happened in the
// API's own mount namespace.
func TestCheckExecuting(t *testing.T) {
	requested, err := k0s.ParseVersion("v1.35.9+k0s.0")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		err     error
		drained bool
		want    string
	}{
		{name: "the merged binary"},
		{
			name: "another binary on a drained node", err: k0s.ErrStaleBinary, drained: true,
			want: "k0sworker.service: the service runs another k0s binary than the merged one; it stays cordoned",
		},
		{name: "another binary on an undrained node", err: k0s.ErrStaleBinary, want: "it was not drained"},
		{name: "the service is not running", err: errors.New("k0sworker.service is not running"), want: "is not running"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := runsMergedBinary
			runsMergedBinary = func(context.Context, config.Role) error { return test.err }

			t.Cleanup(func() { runsMergedBinary = previous })

			err := checkExecuting(context.Background(), config.RoleWorker, requested, test.drained)

			if test.want == "" {
				if err != nil {
					t.Fatalf("checkExecuting() error = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("checkExecuting() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}
