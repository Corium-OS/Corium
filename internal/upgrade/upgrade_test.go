package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The policy the image actually ships: a cosign signature is required for
// Corium's own repository, and everything else is accepted unsigned.
const shippedPolicy = `{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {
    "docker": {
      "ghcr.io/corium-os/corium": [
        {"type": "sigstoreSigned", "keyPath": "/usr/share/corium/cosign.pub"}
      ]
    },
    "docker-daemon": {"": [{"type": "insecureAcceptAnything"}]}
  }
}`

func policyFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing policy: %v", err)
	}

	return path
}

func TestPolicyAcceptsWhatItRequiresASignatureFor(t *testing.T) {
	path := policyFile(t, shippedPolicy)

	for _, image := range []string{
		"ghcr.io/corium-os/corium:0.2",
		"ghcr.io/corium-os/corium:latest",
		"ghcr.io/corium-os/corium@sha256:" + strings.Repeat("a", 64),
	} {
		if err := checkPolicy(path, image); err != nil {
			t.Errorf("checkPolicy(%q) = %v, want nil", image, err)
		}
	}
}

func TestPolicyRefusesAnImageItWouldTakeUnsigned(t *testing.T) {
	path := policyFile(t, shippedPolicy)

	// The case this exists for, from the issue thread: a typo, or somebody
	// meaning well, rebasing a Kubernetes node onto a desktop image. It falls
	// through to the default, which accepts anything, so it is refused here.
	for _, image := range []string{
		"quay.io/fedora-ostree-desktops/silverblue:44",
		"ghcr.io/corium-os/something-else:1",
		"ghcr.io/someone/corium:0.2",
		"docker.io/library/nginx:latest",
	} {
		if err := checkPolicy(path, image); !errors.Is(err, ErrUnsigned) {
			t.Errorf("checkPolicy(%q) = %v, want %v", image, err, ErrUnsigned)
		}
	}
}

func TestPolicyScopesResolveMostSpecificFirst(t *testing.T) {
	// containers-policy(5) resolves a reference by narrowing scope. Getting
	// this wrong in the permissive direction would accept an image the node is
	// about to refuse to pull, moving a clear refusal here into a confusing
	// failure mid-upgrade.
	path := policyFile(t, `{
	  "default": [{"type": "insecureAcceptAnything"}],
	  "transports": {"docker": {
	    "registry.example.com": [{"type": "sigstoreSigned"}],
	    "registry.example.com/open": [{"type": "insecureAcceptAnything"}],
	    "registry.example.com/open/signed:1": [{"type": "sigstoreSigned"}]
	  }}
	}`)

	tests := map[string]bool{
		// Covered by the registry-wide rule.
		"registry.example.com/team/app:1": true,
		// The narrower namespace rule wins, and it requires nothing.
		"registry.example.com/open/app:1": false,
		// The exact reference is narrower still.
		"registry.example.com/open/signed:1": true,
		// ...but only for that tag.
		"registry.example.com/open/signed:2": false,
	}

	for image, want := range tests {
		err := checkPolicy(path, image)
		if got := err == nil; got != want {
			t.Errorf("checkPolicy(%q) accepted = %v, want %v (%v)", image, got, want, err)
		}
	}
}

func TestPolicyUsesTheTransportCatchAll(t *testing.T) {
	path := policyFile(t, `{
	  "default": [{"type": "insecureAcceptAnything"}],
	  "transports": {"docker": {"": [{"type": "signedBy", "keyPath": "/k"}]}}
	}`)

	if err := checkPolicy(path, "anywhere.example.com/x/y:1"); err != nil {
		t.Errorf("checkPolicy() = %v, want the transport default to apply", err)
	}
}

func TestPolicyThatCannotBeReadRefuses(t *testing.T) {
	// A missing or broken policy must not read as permission. The node is
	// about to replace its own operating system.
	if err := checkPolicy(filepath.Join(t.TempDir(), "absent.json"), "x/y:1"); err == nil {
		t.Error("a missing policy was accepted")
	}

	if err := checkPolicy(policyFile(t, "{not json"), "x/y:1"); err == nil {
		t.Error("an unparseable policy was accepted")
	}
}

func TestReferencesAreRefusedBeforeReachingBootc(t *testing.T) {
	var ran bool

	manager := &Manager{
		PolicyPath: policyFile(t, shippedPolicy),
		Run: func(context.Context, string, ...string) ([]byte, error) {
			ran = true

			return nil, nil
		},
	}

	for _, image := range []string{
		"",
		"ghcr.io/corium-os/corium:0.2 --apply",
		"ghcr.io/corium-os/corium:0.2; reboot",
		"-rf",
		"ghcr.io/corium-os/corium:0.2\nbootc rollback",
		"UPPERCASE.io/corium/corium:1",
		"nogregistry",
	} {
		if _, err := manager.Stage(t.Context(), image, nil); !errors.Is(err, ErrBadReference) {
			t.Errorf("Stage(%q) = %v, want %v", image, err, ErrBadReference)
		}
	}

	if ran {
		t.Error("a refused reference still reached a command")
	}
}

// bootcStatus is what the node says after staging something.
const bootcStatus = `{"status":{"staged":{"image":{
  "image":{"image":"ghcr.io/corium-os/corium:0.2"},
  "version":"0.2.0","imageDigest":"sha256:bbbb"}}}}`

type recorder struct {
	calls  [][]string
	status string
}

func (r *recorder) runner() Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		r.calls = append(r.calls, append([]string{name}, args...))

		if name == "bootc" && len(args) > 0 && args[0] == "status" {
			return []byte(r.status), nil
		}

		return nil, nil
	}
}

func TestStageDoesNotReboot(t *testing.T) {
	commands := &recorder{status: bootcStatus}
	manager := &Manager{PolicyPath: policyFile(t, shippedPolicy), Run: commands.runner()}

	var progress []string

	staged, err := manager.Stage(t.Context(), "ghcr.io/corium-os/corium:0.2",
		func(line string) { progress = append(progress, line) })
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}

	if staged.Digest != "sha256:bbbb" {
		t.Errorf("staged = %+v, want the digest read back", staged)
	}

	// The pull announces itself before it starts, which is what commits the
	// response to a stream on a real node. A stubbed runner cannot stream
	// bootc's own lines, but this one must still arrive.
	if len(progress) == 0 || !strings.Contains(progress[0], "ghcr.io/corium-os/corium:0.2") {
		t.Errorf("progress = %v, want it to announce the pull", progress)
	}

	// Staging is the absence of --apply, not the presence of --apply=false:
	// bootc takes it as a bare boolean and rejects a value, which is how an
	// explicit `--apply=false` failed on every real node while passing a test
	// that only checked the string was there. Assert the flag is absent.
	var switched bool

	for _, call := range commands.calls {
		joined := strings.Join(call, " ")

		if len(call) > 1 && call[1] == "switch" {
			switched = true

			if strings.Contains(joined, "--apply") {
				t.Errorf("switch called as %v, which would reboot the node", call)
			}
		}

		if strings.Contains(joined, "reboot") {
			t.Errorf("staging ran %v", call)
		}
	}

	if !switched {
		t.Error("nothing was staged")
	}
}

func TestStagingSomethingAlreadyRunningSaysSo(t *testing.T) {
	// bootc exits zero and stages nothing when the node is already on that
	// digest. Reporting success would leave the operator wondering what the
	// next reboot does.
	commands := &recorder{status: `{"status":{}}`}
	manager := &Manager{PolicyPath: policyFile(t, shippedPolicy), Run: commands.runner()}

	_, err := manager.Stage(t.Context(), "ghcr.io/corium-os/corium:0.2", nil)
	if !errors.Is(err, ErrNothingStaged) {
		t.Fatalf("Stage() = %v, want %v", err, ErrNothingStaged)
	}
}

func TestRunStreamingForwardsLinesAsTheyArrive(t *testing.T) {
	// The real staging path, which the stubbed Runner never exercises: bootc's
	// output is streamed line by line rather than collected. Stood in for by a
	// shell, since a real bootc is not something a unit test has.
	var lines []string

	err := runStreaming(t.Context(), func(line string) { lines = append(lines, line) },
		"sh", "-c", "echo pulling; echo 'layer 1/3' 1>&2; echo done")
	if err != nil {
		t.Fatalf("runStreaming() = %v", err)
	}

	// Both streams are forwarded: bootc writes progress to stdout and stderr
	// both, and an operator wants all of it.
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"pulling", "layer 1/3", "done"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lines = %v, missing %q", lines, want)
		}
	}
}

func TestRunStreamingFailsWithTheLastLineItSaw(t *testing.T) {
	// On failure the last line bootc printed is the error, because that is where
	// bootc says what went wrong. A bare "exit status 1" would throw that away.
	err := runStreaming(t.Context(), nil,
		"sh", "-c", "echo working; echo 'error: manifest unknown'; exit 1")
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("runStreaming() = %v, want the last line as the error", err)
	}
}

func TestApplyGoesThroughTheUnitThatKnowsHowToDrain(t *testing.T) {
	commands := &recorder{status: bootcStatus}

	if err := (&Manager{Run: commands.runner()}).Apply(t.Context()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	joined := strings.Join(commands.calls[len(commands.calls)-1], " ")

	// Not a reboot, and not a reimplementation of the drain: the unit already
	// knows that a refused eviction must cancel the upgrade rather than force
	// it, and that a staged deployment has to be unlocked first.
	if !strings.Contains(joined, ApplyUnit) {
		t.Errorf("Apply() ran %q, want it to start %s", joined, ApplyUnit)
	}

	if !strings.Contains(joined, "--no-block") {
		t.Errorf("Apply() ran %q, want --no-block; the node is about to go away", joined)
	}
}

func TestApplyWithNothingStagedIsRefused(t *testing.T) {
	commands := &recorder{status: `{"status":{}}`}

	if err := (&Manager{Run: commands.runner()}).Apply(t.Context()); !errors.Is(err, ErrNothingStaged) {
		t.Fatalf("Apply() = %v, want %v", err, ErrNothingStaged)
	}

	for _, call := range commands.calls {
		if strings.Contains(strings.Join(call, " "), ApplyUnit) {
			t.Error("a refused apply still started the unit")
		}
	}
}

// A node that has upgraded at least once, and so has somewhere to go back to.
const withRollback = `{"status":{"rollback":{"image":{
  "image":{"image":"ghcr.io/corium-os/corium:0.1"},"imageDigest":"sha256:aaaa"}}}}`

func TestRollbackNeedsSomewhereToGoBackTo(t *testing.T) {
	// A node that has only ever booted one image is in this state on purpose.
	// Surfacing bootc's own refusal as a failure of the API would read like
	// something is broken when nothing is.
	commands := &recorder{status: `{"status":{}}`}

	if err := (&Manager{Run: commands.runner()}).Rollback(t.Context()); !errors.Is(err, ErrNoRollback) {
		t.Fatalf("Rollback() = %v, want %v", err, ErrNoRollback)
	}

	for _, call := range commands.calls {
		if strings.Contains(strings.Join(call, " "), "rollback") {
			t.Error("a refused rollback still ran bootc rollback")
		}
	}
}

func TestRollbackDoesNotReboot(t *testing.T) {
	// Rollback exists because somebody is already having a bad day. Taking the
	// node out of service at a moment they did not choose would not help.
	commands := &recorder{status: withRollback}

	if err := (&Manager{Run: commands.runner()}).Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	for _, call := range commands.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "reboot") || strings.Contains(joined, ApplyUnit) {
			t.Errorf("Rollback() ran %q", joined)
		}
	}
}
