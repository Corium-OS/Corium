package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/upgrade"
)

// upgradeNode starts a claimed node whose bootc and policy are stubbed.
func upgradeNode(t *testing.T, policy string, run upgrade.Runner) (*authority, string, *Store) {
	t.Helper()

	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode, SessionDir(t.TempDir()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatalf("writing policy: %v", err)
	}

	server.Upgrades(&upgrade.Manager{Run: run, PolicyPath: path})

	return ca, serveOn(t, server), store
}

// The policy the image ships: Corium's repository must be signed, everything
// else is accepted unsigned and is therefore refused by the staging check.
const shippedPolicy = `{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {"docker": {"ghcr.io/corium-os/corium": [{"type": "sigstoreSigned"}]}}
}`

const stagedStatus = `{"status":{"staged":{"image":{
  "image":{"image":"ghcr.io/corium-os/corium:0.2"},
  "version":"0.2.0","imageDigest":"sha256:bbbb"}},
  "rollback":{"image":{"image":{"image":"ghcr.io/corium-os/corium:0.1"},
  "imageDigest":"sha256:aaaa"}}}}`

func bootc(status string) upgrade.Runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "status" {
			return []byte(status), nil
		}

		return nil, nil
	}
}

func postRaw(t *testing.T, c *http.Client, url, body string) (int, map[string]any) {
	t.Helper()

	response, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}

	defer func() { _ = response.Body.Close() }()

	return response.StatusCode, decode(t, response.Body)
}

func TestStagingAnImageThePolicyWouldTakeUnsignedIsRefused(t *testing.T) {
	// The case from the issue thread: a Kubernetes node rebased onto a desktop
	// image. 403 rather than 400 -- the reference is well formed and the node
	// is refusing it on policy, which is a different thing to fix.
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(stagedStatus))

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/upgrade/stage",
		`{"image":"quay.io/fedora-ostree-desktops/silverblue:44"}`)

	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "signature") {
		t.Errorf("error = %q, want it to explain the policy", message)
	}
}

// stageStream posts to the staging route and reads the newline-delimited
// stream, separating the progress lines from the single terminal record. A
// refusal before the pull begins is not a stream, so it comes back as a status
// code and a decoded body instead.
func stageStream(t *testing.T, c *http.Client, url, body string) (int, []string, map[string]any) {
	t.Helper()

	response, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return response.StatusCode, nil, decode(t, response.Body)
	}

	var (
		progress []string
		terminal map[string]any
	)

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("stream line %q: %v", scanner.Text(), err)
		}

		if line, ok := event["progress"].(string); ok {
			progress = append(progress, line)

			continue
		}

		terminal = event
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the stream: %v", err)
	}

	return response.StatusCode, progress, terminal
}

func TestStagingStreamsProgressThenTheResult(t *testing.T) {
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(stagedStatus))

	status, progress, terminal := stageStream(t, client(t, store, ca.issue(t, RoleOperator)),
		"https://"+address+"/v1/upgrade/stage",
		`{"image":"ghcr.io/corium-os/corium:0.2"}`)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, terminal)
	}

	// The pull announces itself: on a real node bootc's lines follow, and even
	// with a stubbed runner this first one is what commits the stream and keeps
	// the operator from watching a silent terminal.
	if len(progress) == 0 {
		t.Errorf("no progress streamed, want at least the pull announcement")
	}

	staged, _ := terminal["staged"].(map[string]any)
	if staged["digest"] != "sha256:bbbb" {
		t.Errorf("terminal = %v, want the staged digest", terminal)
	}
}

func TestStagingNeedsOperatorAndApplyingNeedsAdmin(t *testing.T) {
	// Staging pulls an image and changes nothing else. Applying takes the node
	// out of service, so the two are not the same decision.
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(stagedStatus))

	readonly := client(t, store, ca.issue(t, RoleReadOnly))
	operator := client(t, store, ca.issue(t, RoleOperator))

	if status, _ := postRaw(t, readonly, "https://"+address+"/v1/upgrade/stage",
		`{"image":"ghcr.io/corium-os/corium:0.2"}`); status != http.StatusForbidden {
		t.Errorf("stage as readonly = %d, want 403", status)
	}

	if status, _ := postRaw(t, operator, "https://"+address+"/v1/upgrade/apply",
		"{}"); status != http.StatusForbidden {
		t.Errorf("apply as operator = %d, want 403", status)
	}

	if status, _ := postRaw(t, operator, "https://"+address+"/v1/upgrade/rollback",
		"{}"); status != http.StatusForbidden {
		t.Errorf("rollback as operator = %d, want 403", status)
	}
}

func TestApplyingIsAcceptedRatherThanCompleted(t *testing.T) {
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(stagedStatus))

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/upgrade/apply", "{}")

	// 202: the node has taken the job and is about to drain and reboot. It has
	// not finished, and it will not be here to say so.
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%v)", status, body)
	}
}

func TestApplyingWithNothingStagedIsAConflict(t *testing.T) {
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(`{"status":{}}`))

	status, _ := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/upgrade/apply", "{}")

	// Nothing is wrong with the request; the node simply has nothing to apply.
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
}

func TestRollbackOnANodeWithNowhereToGoIsAConflict(t *testing.T) {
	// A node that has only ever booted one image. Nothing is wrong with the
	// request, and reporting a server error would read like something broke.
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(`{"status":{}}`))

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/upgrade/rollback", "{}")

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%v)", status, body)
	}
}

func TestRollbackDoesNotClaimToHaveRebooted(t *testing.T) {
	ca, address, store := upgradeNode(t, shippedPolicy, bootc(stagedStatus))

	status, body := postRaw(t, client(t, store, ca.issue(t, RoleAdmin)),
		"https://"+address+"/v1/upgrade/rollback", "{}")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	// Rollback exists because somebody is already having a bad day; the reboot
	// stays theirs to schedule, and the reply says so.
	if note, _ := body["note"].(string); !strings.Contains(note, "reboot when you are ready") {
		t.Errorf("note = %q, want it to leave the reboot to the operator", note)
	}
}
