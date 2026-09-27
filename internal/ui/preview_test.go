package ui_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
	"github.com/Corium-OS/Corium/internal/ui"
)

type previewNode struct {
	node nodeinfo.Node

	// down makes the connector fail with the message a real unreachable node
	// produces. Closing an httptest server would do it too, but its error
	// names the ephemeral port it was listening on, which is confusing in a
	// picture of a fleet.
	down string
}

func (p *previewNode) client(t *testing.T) *cctl.Client {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(p.node)
	})

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "role": string(api.RoleAdmin)})
	})

	mux.HandleFunc("GET /v1/services", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"services": []systemd.Status{
			{Unit: systemd.Unit{Name: "k0scontroller.service", Restartable: true}, Active: "active", Sub: "running"},
			{Unit: systemd.Unit{Name: "corium-apid.service", Restartable: true}, Active: "active", Sub: "running"},
			{Unit: systemd.Unit{Name: "corium-bootstrap.service"}, Active: "inactive", Sub: "dead"},
			{Unit: systemd.Unit{Name: "greenboot-healthcheck.service"}, Active: "failed", Sub: "failed"},
		}})
	})

	mux.HandleFunc("GET /v1/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")

		encoder := json.NewEncoder(w)
		for _, record := range []systemd.Record{
			{Time: time.Now().Add(-9 * time.Minute), Unit: "k0scontroller.service", Priority: 6, Message: "Starting kube-router"},
			{Time: time.Now().Add(-8 * time.Minute), Unit: "k0scontroller.service", Priority: 4, Message: "node not ready: waiting for CNI"},
			{Time: time.Now().Add(-7 * time.Minute), Unit: "k0scontroller.service", Priority: 6, Message: "joined etcd member list"},
			{Time: time.Now().Add(-6 * time.Minute), Unit: "corium-apid.service", Priority: 3, Message: "refused a request: client certificate has no role"},
			{Time: time.Now().Add(-5 * time.Minute), Unit: "corium-apid.service", Priority: 6, Message: "listening on :7443"},
		} {
			_ = encoder.Encode(record)
		}
	})

	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	return cctl.Dial(strings.TrimPrefix(server.URL, "https://"), api.Fingerprint(server.Certificate().Raw))
}

// TestPreview serves the dashboard against a made-up fleet so the page can be
// looked at. It is skipped unless CORIUM_UI_PREVIEW is set, and it is a
// development aid rather than an assertion.
func TestPreview(t *testing.T) {
	listen := os.Getenv("CORIUM_UI_PREVIEW")
	if listen == "" {
		t.Skip("set CORIUM_UI_PREVIEW=127.0.0.1:7592 to serve the dashboard")
	}

	const image = "ghcr.io/corium-os/corium:0.3"
	const digest = "sha256:bd67161f2c4a8e90d1b3"

	controller := func(name string, uptime int64) *previewNode {
		return &previewNode{node: nodeinfo.Node{
			Hostname: name, MachineID: "5f2c1b9e4a7d43c8b1e6f0a2d9c37e41",
			Bootstrapped: true, Role: "controller", Cluster: "apitest",
			OS: nodeinfo.OS{Name: "Fedora Linux 44 (Forty Four)", Kernel: "7.2.5-200.fc44.x86_64",
				Booted: &nodeinfo.Deployment{Image: image, Digest: digest, Version: "0.3.6"}},
			Kubernetes: nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0", Service: "k0scontroller.service", Active: true},
			Health:     nodeinfo.Health{Greenboot: "passed", UptimeSeconds: uptime},
			Management: nodeinfo.Management{ClaimedBy: "pairing-code"},
		}}
	}

	worker := func(name string, uptime int64) *previewNode {
		return &previewNode{node: nodeinfo.Node{
			Hostname: name, Bootstrapped: true, Role: "worker", Cluster: "apitest",
			OS: nodeinfo.OS{Name: "Fedora Linux 44 (Forty Four)", Kernel: "7.2.5-200.fc44.x86_64",
				Booted: &nodeinfo.Deployment{Image: image, Digest: digest, Version: "0.3.6"}},
			Kubernetes: nodeinfo.Kubernetes{Version: "v1.36.4+k0s.0", Service: "k0sworker.service", Active: true},
			Health:     nodeinfo.Health{Greenboot: "passed", UptimeSeconds: uptime},
			Management: nodeinfo.Management{ClaimedBy: "configuration"},
		}}
	}

	// Three states worth seeing: healthy, an image staged and waiting for a
	// reboot, and a machine that did not answer.
	staged := worker("corium-w2", 86_400)
	staged.node.OS.Staged = &nodeinfo.Deployment{
		Image: "ghcr.io/corium-os/corium:0.4", Digest: "sha256:9ac41e77b0d2f5386c1a",
	}

	fleet := map[string]*previewNode{
		"192.168.1.51:7443": controller("corium-c1", 711),
		"192.168.1.52:7443": controller("corium-c2", 259_140),
		"192.168.1.53:7443": controller("corium-c3", 259_100),
		"192.168.1.54:7443": worker("corium-w1", 259_201),
		"192.168.1.55:7443": staged,
		"192.168.1.56:7443": {down: "dial tcp 192.168.1.56:7443: connect: no route to host"},
	}

	addresses := make([]string, 0, len(fleet))
	for address := range fleet {
		addresses = append(addresses, address)
	}

	sort.Strings(addresses)

	server, err := ui.NewServer(listen, addresses, func(address string) (*cctl.Client, error) {
		if reason := fleet[address].down; reason != "" {
			return nil, errors.New(reason)
		}

		return fleet[address].client(t), nil
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	// Printed rather than logged: `go test` holds a test's log until it ends,
	// and this one does not end.
	fmt.Println(server.URL())

	//nolint:gosec // a development preview, bound to whatever the operator asked for
	if err := http.ListenAndServe(listen, server.Handler()); err != nil {
		t.Fatalf("serving: %v", err)
	}
}
