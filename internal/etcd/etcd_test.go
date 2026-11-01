package etcd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const memberList = `{"members":{"cp-2":"https://10.0.0.2:2380","cp-1":"https://10.0.0.1:2380"}}`

func manager(t *testing.T, k0sYAML string, probe error, list string, listErr error) *Manager {
	t.Helper()

	path := filepath.Join(t.TempDir(), "k0s.yaml")
	if err := os.WriteFile(path, []byte(k0sYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	return &Manager{
		ConfigPath: path,
		Hostname:   func() (string, error) { return "cp-1", nil },
		Probe:      func(context.Context) error { return probe },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if got := name + " " + strings.Join(args, " "); got != "k0s etcd member-list" {
				t.Errorf("ran %q", got)
			}

			return []byte(list), listErr
		},
	}
}

func TestStatus(t *testing.T) {
	members := []Member{
		{Name: "cp-1", PeerURL: "https://10.0.0.1:2380"},
		{Name: "cp-2", PeerURL: "https://10.0.0.2:2380"},
	}

	tests := []struct {
		name    string
		k0sYAML string
		probe   error
		listErr error
		want    Status
	}{
		{
			name:    "healthy member",
			k0sYAML: "spec:\n  storage:\n    type: etcd\n",
			want:    Status{Storage: StorageEtcd, Member: "cp-1", Healthy: true, Members: members},
		},
		{
			name:    "no type is etcd, as in k0s",
			k0sYAML: "spec: {}\n",
			want:    Status{Storage: StorageEtcd, Member: "cp-1", Healthy: true, Members: members},
		},
		{
			name:    "name from extraArgs",
			k0sYAML: "spec:\n  storage:\n    type: etcd\n    etcd:\n      extraArgs:\n        name: custom\n",
			want:    Status{Storage: StorageEtcd, Member: "custom", Healthy: true, Members: members},
		},
		{
			name:    "kine has no member",
			k0sYAML: "spec:\n  storage:\n    type: kine\n",
			want:    Status{Storage: StorageKine},
		},
		{
			name:    "external etcd is not this node's",
			k0sYAML: "spec:\n  storage:\n    type: etcd\n    etcd:\n      externalCluster:\n        endpoints: [https://e:2379]\n",
			want:    Status{Storage: StorageExternal},
		},
		{
			name:    "unhealthy probe",
			k0sYAML: "spec:\n  storage:\n    type: etcd\n",
			probe:   errors.New("etcd reports itself unhealthy: no leader"),
			want:    Status{Storage: StorageEtcd, Member: "cp-1", Reason: "etcd reports itself unhealthy: no leader"},
		},
		{
			name:    "member list fails",
			k0sYAML: "spec:\n  storage:\n    type: etcd\n",
			listErr: errors.New("context deadline exceeded"),
			want:    Status{Storage: StorageEtcd, Member: "cp-1", Reason: "listing etcd members: context deadline exceeded"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := manager(t, test.k0sYAML, test.probe, memberList, test.listErr).Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}

			if !reflect.DeepEqual(*got, test.want) {
				t.Errorf("Status() = %+v, want %+v", *got, test.want)
			}
		})
	}
}

func TestStatusWithoutK0sConfig(t *testing.T) {
	m := &Manager{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")}

	if _, err := m.Status(t.Context()); err == nil {
		t.Error("Status() = nil error, want one for a missing k0s.yaml")
	}
}

func TestParseHealth(t *testing.T) {
	tests := []struct {
		code    int
		body    string
		wantErr string
	}{
		{200, `{"health":"true","reason":""}`, ""},
		{503, `{"health":"false","reason":"RAFT NO LEADER"}`, "RAFT NO LEADER"},
		{503, `{"health":"false"}`, "no reason given"},
		{502, `bad gateway`, "no health verdict"},
	}

	for _, test := range tests {
		err := parseHealth(test.code, strings.NewReader(test.body))

		switch {
		case test.wantErr == "" && err != nil:
			t.Errorf("parseHealth(%s) = %v, want nil", test.body, err)
		case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
			t.Errorf("parseHealth(%s) = %v, want %q", test.body, err, test.wantErr)
		}
	}
}
