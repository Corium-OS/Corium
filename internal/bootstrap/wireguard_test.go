package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestRenderWireGuardConf(t *testing.T) {
	t.Parallel()

	iface := &config.WireGuardInterface{
		Name:       "wg0",
		Address:    "10.10.0.2/24",
		ListenPort: 51820,
		MTU:        1420,
		Peers: []config.WireGuardPeer{{
			PublicKey:           "PUBKEYAAA",
			Endpoint:            "gw.example:51820",
			AllowedIPs:          []string{"10.10.0.0/24", "10.20.0.0/16"},
			PersistentKeepalive: 25,
		}},
	}

	conf := renderWireGuardConf(iface, "PRIVKEYAAA", []string{"PSKAAA"})

	for _, want := range []string{
		"[Interface]",
		"Address = 10.10.0.2/24",
		"ListenPort = 51820",
		"MTU = 1420",
		"PrivateKey = PRIVKEYAAA",
		"[Peer]",
		"PublicKey = PUBKEYAAA",
		"PresharedKey = PSKAAA",
		"Endpoint = gw.example:51820",
		"AllowedIPs = 10.10.0.0/24, 10.20.0.0/16",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("rendered conf missing %q:\n%s", want, conf)
		}
	}
}

func TestRenderWireGuardConfIsDeterministic(t *testing.T) {
	t.Parallel()

	iface := &config.WireGuardInterface{
		Name:    "wg0",
		Address: "10.10.0.2/24",
		Peers: []config.WireGuardPeer{
			{PublicKey: "A", AllowedIPs: []string{"10.10.0.0/24"}},
			{PublicKey: "B", AllowedIPs: []string{"10.20.0.0/24"}},
		},
	}

	first := renderWireGuardConf(iface, "K", []string{"", ""})

	for i := 0; i < 10; i++ {
		if renderWireGuardConf(iface, "K", []string{"", ""}) != first {
			t.Fatal("renderWireGuardConf() is not deterministic")
		}
	}
}

func TestRenderWireGuardConfOmitsAbsentOptionals(t *testing.T) {
	t.Parallel()

	iface := &config.WireGuardInterface{
		Name:    "wg0",
		Address: "10.10.0.2/24",
		Peers:   []config.WireGuardPeer{{PublicKey: "A", AllowedIPs: []string{"10.10.0.0/24"}}},
	}

	conf := renderWireGuardConf(iface, "K", []string{""})

	for _, absent := range []string{"PresharedKey", "ListenPort", "MTU", "Endpoint", "PersistentKeepalive"} {
		if strings.Contains(conf, absent) {
			t.Errorf("rendered conf includes %q although it was not set:\n%s", absent, conf)
		}
	}
}

func TestResolveWireGuardKeyInline(t *testing.T) {
	t.Parallel()

	got, err := resolveWireGuardKey(context.Background(), "  KEY  ", nil, "test key")
	if err != nil {
		t.Fatalf("resolveWireGuardKey() error = %v", err)
	}

	if got != "KEY" {
		t.Errorf("resolveWireGuardKey() = %q, want the trimmed inline key", got)
	}

	empty, err := resolveWireGuardKey(context.Background(), "", nil, "test key")
	if err != nil || empty != "" {
		t.Errorf("resolveWireGuardKey() = %q, %v; want empty and no error", empty, err)
	}
}
