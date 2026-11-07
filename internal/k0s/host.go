package k0s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

// hostRun and hostOutput run commands on the host. Variables so tests can
// record what would have run.
var (
	hostRun    = run
	hostOutput = commandOutput
)

// mergeExtensions merges what /var/lib/extensions holds, through systemd.
//
// Running systemd-sysext directly would merge in the caller's mount namespace.
// corium-apid has its own (PrivateTmp=), so the new /usr reached the API and
// not the k0s service started by systemd. Restarting the unit makes systemd do
// the merge on the host; it propagates into every service from there.
func mergeExtensions(ctx context.Context) error {
	if err := hostRun(ctx, "systemctl", "restart", "systemd-sysext.service"); err != nil {
		return fmt.Errorf("merging the extension: %w", err)
	}

	return nil
}

// processExe and mergedBinary are the two files RunsMergedBinary compares.
// Variables so tests can use their own.
var (
	processExe   = func(pid string) string { return "/proc/" + pid + "/exe" }
	mergedBinary = Binary
)

// execTimeout bounds how long the service's process may take to exec k0s, and
// execPoll how often it is looked at. Variables so tests run fast.
var (
	execTimeout = 30 * time.Second
	execPoll    = 200 * time.Millisecond
)

// ErrStaleBinary reports a k0s service that runs another file than the merged
// /usr/bin/k0s.
var ErrStaleBinary = errors.New("the service runs another k0s binary than the merged one")

// RunsMergedBinary reports whether the service's main process executes the
// file now at /usr/bin/k0s.
//
// The extension-release file says what is merged, not what started. The two
// disagreed when the merge happened in the wrong namespace. Comparing the files
// rather than running them answers without executing anything.
//
// It waits for the process to exec: the k0s units are Type=simple, so
// systemctl start returns while the main process still runs systemd's code.
func RunsMergedBinary(ctx context.Context, role config.Role) error {
	deadline := time.Now().Add(execTimeout)

	for {
		err := runsMergedBinary(ctx, role)
		if err == nil || !errors.Is(err, ErrStaleBinary) || time.Now().After(deadline) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(execPoll):
		}
	}
}

func runsMergedBinary(ctx context.Context, role config.Role) error {
	service := ServiceName(role)

	out, err := hostOutput(ctx, "systemctl", "show", "--property", "MainPID", "--value", service)
	if err != nil {
		return fmt.Errorf("finding the %s process: %w", service, err)
	}

	pid := strings.TrimSpace(string(out))
	if pid == "" || pid == "0" {
		return fmt.Errorf("%s is not running", service)
	}

	running, err := os.Stat(processExe(pid))
	if err != nil {
		return fmt.Errorf("reading the %s executable: %w", service, err)
	}

	merged, err := os.Stat(mergedBinary)
	if err != nil {
		return fmt.Errorf("reading %s: %w", mergedBinary, err)
	}

	if !os.SameFile(running, merged) {
		return ErrStaleBinary
	}

	return nil
}

// commandOutput runs a command and returns its standard output.
func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s %s: %w: %s",
				name, strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return out, nil
}
