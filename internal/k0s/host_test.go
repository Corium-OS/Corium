package k0s

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

// recorder stands in for the commands this package runs on the host.
type recorder struct {
	calls   []string
	outputs map[string]string
}

func (r *recorder) run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))

	return nil
}

func (r *recorder) output(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)

	out, ok := r.outputs[line]
	if !ok {
		return nil, errors.New("unexpected command: " + line)
	}

	return []byte(out), nil
}

// TestMergeAsksSystemd guards the bug where corium-apid ran systemd-sysext
// itself: its private mount namespace kept the merge from reaching the host,
// so k0s restarted on the old binary while the API reported the new one.
func TestMergeAsksSystemd(t *testing.T) {
	rec := &recorder{}
	swapRunners(t, rec)

	if err := mergeExtensions(context.Background()); err != nil {
		t.Fatalf("mergeExtensions() error = %v", err)
	}

	want := "systemctl restart systemd-sysext.service"
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Errorf("ran %q, want only %q", rec.calls, want)
	}
}

// TestRunsMergedBinary asks whether the service's process executes the file
// now at /usr/bin/k0s. The failure it guards against is a service restarted on
// a binary that is no longer the merged one.
func TestRunsMergedBinary(t *testing.T) {
	dir := t.TempDir()
	merged := filepath.Join(dir, "k0s")
	old := filepath.Join(dir, "k0s-old")
	// What a Type=simple service's main process runs between fork and exec.
	forked := filepath.Join(dir, "systemd")

	for _, path := range []string{merged, old, forked} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	showPID := "systemctl show --property MainPID --value k0sworker.service"

	tests := []struct {
		name    string
		pid     string
		exes    []string // what the process runs on each look, the last one kept
		wantErr bool
	}{
		{name: "the merged binary", pid: "4242", exes: []string{merged}},
		{
			// systemctl start returns after fork, before exec: the first look
			// sees systemd, not k0s.
			name: "the merged binary, once the process has exec'd", pid: "4242",
			exes: []string{forked, forked, merged},
		},
		{name: "another binary", pid: "4242", exes: []string{old}, wantErr: true},
		{name: "the service is not running", pid: "0", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := &recorder{outputs: map[string]string{showPID: test.pid + "\n"}}
			swapRunners(t, rec)

			looks := 0

			previousExe, previousBinary := processExe, mergedBinary
			previousWait, previousPoll := execTimeout, execPoll
			processExe = func(pid string) string {
				if pid != "4242" {
					t.Errorf("asked for process %q", pid)
				}

				exe := test.exes[min(looks, len(test.exes)-1)]
				looks++

				return exe
			}
			mergedBinary = merged
			execTimeout, execPoll = 200*time.Millisecond, time.Millisecond

			t.Cleanup(func() {
				processExe, mergedBinary = previousExe, previousBinary
				execTimeout, execPoll = previousWait, previousPoll
			})

			err := RunsMergedBinary(context.Background(), config.RoleWorker)
			if (err != nil) != test.wantErr {
				t.Errorf("RunsMergedBinary() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

// swapRunners points the package at rec for one test. Not parallel: the
// runners are package state.
func swapRunners(t *testing.T, rec *recorder) {
	t.Helper()

	run, output := hostRun, hostOutput
	hostRun, hostOutput = rec.run, rec.output

	t.Cleanup(func() { hostRun, hostOutput = run, output })
}
