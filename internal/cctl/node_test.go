package cctl

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
)

// node is a real corium-apid server run in-process, so that these tests
// exercise the actual protocol rather than a description of it.
type node struct {
	store   *api.Store
	address string

	mu      sync.Mutex
	running *instance
}

type instance struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

// stop is idempotent, because both a restart and the test's cleanup want to
// call it and neither should have to know whether the other already did.
func (i *instance) stop(t *testing.T) {
	t.Helper()

	i.once.Do(func() {
		i.cancel()

		select {
		case err := <-i.done:
			if err != nil {
				t.Errorf("Serve() = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve() did not stop")
		}
	})
}

// startNode brings up an unenrolled node and returns its address and the
// pairing code its console would be showing.
func startNode(t *testing.T) (*node, string, string) {
	t.Helper()

	n := &node{store: api.NewStore(filepath.Join(t.TempDir(), "api")), address: "127.0.0.1:0"}

	code := n.start(t)

	t.Cleanup(func() {
		n.mu.Lock()
		defer n.mu.Unlock()

		n.running.stop(t)
	})

	return n, n.address, code
}

// start runs one instance and records the address it bound, so that a later
// restart can reuse it -- a restart that moved the port would not be one.
func (n *node) start(t *testing.T) string {
	t.Helper()

	server, err := api.NewServer(n.store, n.address)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	running := &instance{cancel: cancel, done: make(chan error, 1)}

	go func() { running.done <- server.Serve(ctx) }()

	select {
	case <-server.Ready():
	case err := <-running.done:
		cancel()
		t.Fatalf("Serve() returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Serve() never became ready")
	}

	n.mu.Lock()
	n.address = server.Addr()
	n.running = running
	n.mu.Unlock()

	return server.PairingCode()
}

// restart is what systemd does after a successful enrolment: stop the process
// and start it again, so it comes back up requiring client certificates.
func (n *node) restart(t *testing.T) {
	t.Helper()

	n.mu.Lock()
	previous := n.running
	n.mu.Unlock()

	previous.stop(t)

	if code := n.start(t); code != "" {
		t.Fatal("the node came back up unenrolled and offering a pairing code")
	}
}
