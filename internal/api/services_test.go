package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/systemd"
)

// claimedNode starts a server that is already owned, with systemd stubbed.
func claimedNode(t *testing.T, run systemd.Runner) (*authority, string, *Store) {
	t.Helper()

	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	server.Supervise(&systemd.Manager{Run: run})

	return ca, serveOn(t, server), store
}

func replies(out string) systemd.Runner {
	return func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), nil
	}
}

func TestServicesAreListedWithWhatTheyAreFor(t *testing.T) {
	ca, address, store := claimedNode(t, replies("ActiveState=active\nSubState=running"))

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/services")
	if err != nil {
		t.Fatalf("GET /v1/services: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	body := decode(t, response.Body)

	services, ok := body["services"].([]any)
	if !ok || len(services) == 0 {
		t.Fatalf("body = %v, want a list of services", body)
	}

	first, _ := services[0].(map[string]any)
	if first["purpose"] == "" || first["purpose"] == nil {
		t.Error("a service is listed without saying what it is for")
	}
}

func TestRestartNeedsOperator(t *testing.T) {
	// Taking a node out of service for as long as k0s takes to come back is an
	// operator's call, not a reader's.
	ca, address, store := claimedNode(t, replies("ActiveState=active"))

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Post("https://"+address+"/v1/services/k0sworker.service/restart", "", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d as readonly, want 403", response.StatusCode)
	}
}

func TestRestartIsRefusedForUnitsThatRunOnce(t *testing.T) {
	// An operator certificate is not enough: re-running the bootstrap on a node
	// that has already joined a cluster is a data-loss bug, and the refusal is
	// about the unit rather than about the caller.
	ca, address, store := claimedNode(t, replies("ActiveState=active"))

	response, err := client(t, store, ca.issue(t, RoleAdmin)).
		Post("https://"+address+"/v1/services/corium-bootstrap.service/restart", "", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.StatusCode)
	}

	body := decode(t, response.Body)
	if message, _ := body["error"].(string); !strings.Contains(message, "runs once") {
		t.Errorf("error = %q, want it to say why the unit is not restartable", message)
	}
}

func TestRestartingSomethingThatIsNotAUnitIsANotFound(t *testing.T) {
	ca, address, store := claimedNode(t, replies("ActiveState=active"))

	response, err := client(t, store, ca.issue(t, RoleAdmin)).
		Post("https://"+address+"/v1/services/sshd.service/restart", "", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.StatusCode)
	}
}

const twoRecords = `{"__REALTIME_TIMESTAMP":"1789671887000000","_SYSTEMD_UNIT":"k0sworker.service","PRIORITY":"6","MESSAGE":"one"}
{"__REALTIME_TIMESTAMP":"1789671888000000","_SYSTEMD_UNIT":"k0sworker.service","PRIORITY":"3","MESSAGE":"two"}`

func TestLogsComeBackAsOneRecordPerLine(t *testing.T) {
	ca, address, store := claimedNode(t, replies(twoRecords))

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/logs?unit=k0sworker&lines=2")
	if err != nil {
		t.Fatalf("GET /v1/logs: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if got := response.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}

	scanner := bufio.NewScanner(response.Body)

	var messages []string

	for scanner.Scan() {
		var record systemd.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("line is not a record: %v (%s)", err, scanner.Text())
		}

		messages = append(messages, record.Message)
	}

	if len(messages) != 2 || messages[0] != "one" || messages[1] != "two" {
		t.Errorf("messages = %v, want [one two]", messages)
	}
}

func TestLogsRefuseAUnitTheAPIDoesNotKnow(t *testing.T) {
	ca, address, store := claimedNode(t, replies(""))

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/logs?unit=sshd.service")
	if err != nil {
		t.Fatalf("GET /v1/logs: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	// The check happens before anything is written, so this is still a status
	// code rather than a truncated stream.
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}
}

func TestAFollowedStreamOutlivesTheWriteTimeout(t *testing.T) {
	// The bug this guards: http.Server's WriteTimeout cuts a followed stream
	// off once the log goes quiet for longer than the timeout -- which only
	// ever happens in the case the feature exists for.
	// The handler asks for the deadline to be cleared and treats a refusal as
	// fatal, so a 200 here is the assertion: it means the server supported it.
	// A stream that merely started would prove nothing.
	ca, address, store := claimedNode(t, replies(twoRecords))

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).
		Get("https://" + address + "/v1/logs?follow=true")
	if err != nil {
		t.Fatalf("GET /v1/logs: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}
