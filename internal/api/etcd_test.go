package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Corium-OS/Corium/internal/etcd"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

func etcdNode(t *testing.T, state string) (*authority, string, *Store) {
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

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib/corium"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, nodeinfo.StateFile), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	server.Inspect(&nodeinfo.Inspector{Root: root, Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return nil, os.ErrNotExist
	}})

	k0sYAML := filepath.Join(t.TempDir(), "k0s.yaml")
	if err := os.WriteFile(k0sYAML, []byte("spec:\n  storage:\n    type: etcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server.Etcd(&etcd.Manager{
		ConfigPath: k0sYAML,
		Hostname:   func() (string, error) { return "cp-1", nil },
		Probe:      func(context.Context) error { return nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"members":{"cp-1":"https://10.0.0.1:2380"}}`), nil
		},
	})

	return ca, serveOn(t, server), store
}

func TestEtcdIsAReadForAController(t *testing.T) {
	ca, address, store := etcdNode(t, `{"role":"controller"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleReadOnly)), "https://"+address+"/v1/etcd")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}

	var got etcd.Status
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("parsing %s: %v", body, err)
	}

	if !got.Healthy || got.Member != "cp-1" || len(got.Members) != 1 {
		t.Errorf("status = %+v", got)
	}
}

func TestAWorkerHasNoEtcdMember(t *testing.T) {
	ca, address, store := etcdNode(t, `{"role":"worker"}`)

	status, body := fetch(t, client(t, store, ca.issue(t, RoleReadOnly)), "https://"+address+"/v1/etcd")
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", status, body)
	}
}
