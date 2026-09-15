package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	return path
}

func TestFileSource(t *testing.T) {
	path := writeFile(t, "config.yaml", "role: single\n")

	data, err := File(path).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if string(data) != "role: single\n" {
		t.Errorf("Load() = %q", data)
	}
}

func TestFileSourceMissing(t *testing.T) {
	_, err := File("/nonexistent/corium.yaml").Load(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestFileSourceEmptyIsAbsent(t *testing.T) {
	// An empty file is an absent answer, not an empty one: treating it as a
	// configuration would boot a node with no role.
	path := writeFile(t, "empty.yaml", "")

	_, err := File(path).Load(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestParseCmdline(t *testing.T) {
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{"absent", "ro quiet console=ttyS0", ""},
		{"url", "ro corium.config=https://e.com/n.yaml quiet", "https://e.com/n.yaml"},
		{"path", "corium.config=/run/config.yaml", "/run/config.yaml"},
		{"quoted", `corium.config="/run/a b.yaml"`, "/run/a b.yaml"},
		// The kernel lets a later value override an earlier one; matching that
		// means a value appended at boot beats one baked into the bootloader.
		{"last wins", "corium.config=/a.yaml corium.config=/b.yaml", "/b.yaml"},
		{"not a prefix match", "notcorium.config=/a.yaml", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCmdline(tc.cmdline); got != tc.want {
				t.Errorf("parseCmdline(%q) = %q, want %q", tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestKernelCmdlineFromFile(t *testing.T) {
	target := writeFile(t, "node.yaml", "role: worker\n")
	cmdline := writeFile(t, "cmdline", "ro quiet "+CmdlineKey+"="+target+"\n")

	original := cmdlinePath
	cmdlinePath = cmdline
	t.Cleanup(func() { cmdlinePath = original })

	data, err := KernelCmdline().Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if string(data) != "role: worker\n" {
		t.Errorf("Load() = %q", data)
	}
}

func TestKernelCmdlineFromURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("role: controller\n"))
		}))
	defer server.Close()

	cmdline := writeFile(t, "cmdline", CmdlineKey+"="+server.URL+"/n.yaml")

	original := cmdlinePath
	cmdlinePath = cmdline
	t.Cleanup(func() { cmdlinePath = original })

	data, err := KernelCmdline().Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if string(data) != "role: controller\n" {
		t.Errorf("Load() = %q", data)
	}
}

// stubSource lets a test control exactly what a source does.
type stubSource struct {
	name string
	data string
	err  error
}

func (s stubSource) Name() string { return s.name }

func (s stubSource) Load(context.Context) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}

	return []byte(s.data), nil
}

func TestResolvePrefersEarlierSources(t *testing.T) {
	result, err := Resolve(context.Background(), []Source{
		stubSource{name: "absent", err: ErrNotFound},
		stubSource{name: "first", data: "role: single"},
		stubSource{name: "second", data: "role: worker"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if result.Source != "first" {
		t.Errorf("Resolve() picked %q, want the earliest available source", result.Source)
	}
}

func TestResolveStopsOnRealError(t *testing.T) {
	// A source that fails for a reason other than absence must not be skipped.
	// Falling through to a baked-in default when the operator's intent is
	// merely unreachable is how a node silently joins the wrong cluster.
	boom := errors.New("connection refused")

	_, err := Resolve(context.Background(), []Source{
		stubSource{name: "broken", err: boom},
		stubSource{name: "fallback", data: "role: single"},
	})

	if !errors.Is(err, boom) {
		t.Errorf("Resolve() error = %v, want it to surface the real failure", err)
	}
}

func TestResolveExhausted(t *testing.T) {
	_, err := Resolve(context.Background(), []Source{
		stubSource{name: "a", err: ErrNotFound},
		stubSource{name: "b", err: ErrNotFound},
	})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve() error = %v, want ErrNotFound", err)
	}
}

func TestDefaultChainOrder(t *testing.T) {
	// The order encodes a decision about who wins a disagreement; assert it so
	// that reordering is a deliberate act with a failing test attached.
	want := []string{
		"/etc/corium/config.yaml",
		"cloud-init",
		"kernel cmdline",
		"/usr/share/corium/config.yaml",
	}

	chain := Default()
	if len(chain) != len(want) {
		t.Fatalf("Default() has %d sources, want %d", len(chain), len(want))
	}

	for i, name := range want {
		if chain[i].Name() != name {
			t.Errorf("Default()[%d] = %q, want %q", i, chain[i].Name(), name)
		}
	}
}
