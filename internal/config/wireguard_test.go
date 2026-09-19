package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// wgKey returns a valid base64-encoded 32-byte key, built from a repeated byte
// so each fixture key is distinct without hand-counting base64.
func wgKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, wireGuardKeyBytes))
}

func TestValidateWireGuard(t *testing.T) {
	priv := wgKey(1)
	pub := wgKey(2)
	pub2 := wgKey(3)

	tests := []struct {
		name    string
		wg      []WireGuardInterface
		wantErr string
	}{
		{
			name:    "missing name",
			wg:      []WireGuardInterface{{Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv}},
			wantErr: "wireguard[0].name: required",
		},
		{
			name:    "invalid interface name",
			wg:      []WireGuardInterface{{Name: "wg 0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv}},
			wantErr: "must be lowercase",
		},
		{
			name:    "interface name too long",
			wg:      []WireGuardInterface{{Name: "wglonginterface0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv}},
			wantErr: "the limit is 15",
		},
		{
			name: "duplicate interface names",
			wg: []WireGuardInterface{
				{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv},
				{Name: "wg0", Address: WireGuardAddresses{"10.10.1.2/24"}, PrivateKey: priv},
			},
			wantErr: "used by more than one interface",
		},
		{
			name:    "missing address",
			wg:      []WireGuardInterface{{Name: "wg0", PrivateKey: priv}},
			wantErr: "wireguard[0].address: required",
		},
		{
			name:    "address without a prefix length",
			wg:      []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2"}, PrivateKey: priv}},
			wantErr: "address[0]",
		},
		{
			name: "second address in a dual-stack list is malformed",
			wg: []WireGuardInterface{{Name: "wg0",
				Address: WireGuardAddresses{"10.10.0.2/24", "not-an-address"}, PrivateKey: priv}},
			wantErr: "address[1]",
		},
		{
			name:    "listen port out of range",
			wg:      []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, ListenPort: 70000, PrivateKey: priv}},
			wantErr: "listenPort: 70000 is out of range",
		},
		{
			name:    "mtu out of range",
			wg:      []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, MTU: 100, PrivateKey: priv}},
			wantErr: "mtu: 100 is out of range",
		},
		{
			name:    "no private key",
			wg:      []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}}},
			wantErr: "privateKey: required",
		},
		{
			name: "both private key and privateKeyFrom",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"},
				PrivateKey: priv, PrivateKeyFrom: &SecretSource{File: "/run/k"}}},
			wantErr: "set either privateKey or privateKeyFrom, not both",
		},
		{
			name: "private key wrong length",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"},
				PrivateKey: base64.StdEncoding.EncodeToString([]byte("short"))}},
			wantErr: "must be a 32-byte key",
		},
		{
			name:    "private key not base64",
			wg:      []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: "not base64!!"}},
			wantErr: "must be a base64-encoded key",
		},
		{
			name: "two node addresses",
			wg: []WireGuardInterface{
				{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, NodeAddress: true, PrivateKey: priv},
				{Name: "wg1", Address: WireGuardAddresses{"10.20.0.2/24"}, NodeAddress: true, PrivateKey: priv},
			},
			wantErr: "more than one interface sets nodeAddress",
		},
		{
			name: "peer without a public key",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{AllowedIPs: []string{"10.10.0.0/24"}}}}},
			wantErr: "peers[0].publicKey: required",
		},
		{
			name: "peer public key wrong length",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{
					PublicKey:  base64.StdEncoding.EncodeToString([]byte("x")),
					AllowedIPs: []string{"10.10.0.0/24"},
				}}}},
			wantErr: "peers[0].publicKey: must be a 32-byte key",
		},
		{
			name: "duplicate peer keys",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{
					{PublicKey: pub, AllowedIPs: []string{"10.10.0.0/24"}},
					{PublicKey: pub, AllowedIPs: []string{"10.20.0.0/24"}},
				}}},
			wantErr: "used by more than one peer",
		},
		{
			name: "peer endpoint not host:port",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{
					PublicKey: pub, Endpoint: "example.com", AllowedIPs: []string{"10.10.0.0/24"},
				}}}},
			wantErr: "must be host:port",
		},
		{
			name: "peer without allowedIPs",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{PublicKey: pub}}}},
			wantErr: "allowedIPs: required",
		},
		{
			name: "peer allowedIPs not a CIDR",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{PublicKey: pub, AllowedIPs: []string{"10.10.0.0"}}}}},
			wantErr: "is not a valid CIDR",
		},
		{
			name: "overlapping allowedIPs across peers",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{
					{PublicKey: pub, AllowedIPs: []string{"10.10.0.0/24"}},
					{PublicKey: pub2, AllowedIPs: []string{"10.10.0.0/25"}},
				}}},
			wantErr: "overlaps another allowedIPs range",
		},
		{
			name: "keepalive out of range",
			wg: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.2/24"}, PrivateKey: priv,
				Peers: []WireGuardPeer{{
					PublicKey: pub, AllowedIPs: []string{"10.10.0.0/24"}, PersistentKeepalive: 99999,
				}}}},
			wantErr: "persistentKeepalive: 99999 is out of range",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Role: RoleSingle, WireGuard: tc.wg}
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want one containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateWireGuardAcceptsAGoodInterface(t *testing.T) {
	cfg := &Config{
		Role:    RoleControllerWorker,
		Cluster: Cluster{Endpoint: "10.10.0.1"},
		WireGuard: []WireGuardInterface{{
			Name:           "wg0",
			Address:        WireGuardAddresses{"10.10.0.1/24"},
			ListenPort:     51820,
			MTU:            1420,
			NodeAddress:    true,
			PrivateKeyFrom: &SecretSource{File: "/run/corium/wg0.key"},
			Peers: []WireGuardPeer{{
				PublicKey:           wgKey(9),
				Endpoint:            "controller.example:51820",
				AllowedIPs:          []string{"10.10.0.0/24"},
				PersistentKeepalive: 25,
			}},
		}},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateWireGuardAcceptsDualStack(t *testing.T) {
	cfg := &Config{
		Role: RoleSingle,
		WireGuard: []WireGuardInterface{{
			Name:       "wg0",
			Address:    WireGuardAddresses{"10.10.0.1/24", "fd7f:27ef:d6f1:9686::1/128"},
			PrivateKey: wgKey(1),
			Peers: []WireGuardPeer{{
				PublicKey:  wgKey(2),
				Endpoint:   "peer.example:51820",
				AllowedIPs: []string{"0.0.0.0/0", "::/0"},
			}},
		}},
	}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with dual-stack address and allowedIPs = %v, want nil", err)
	}
}

func TestWireGuardNodeAddress(t *testing.T) {
	cfg := &Config{WireGuard: []WireGuardInterface{
		{Name: "wg0", Address: WireGuardAddresses{"10.10.0.5/24"}},
		{Name: "wg1", Address: WireGuardAddresses{"10.20.0.5/16", "fd00::5/128"}, NodeAddress: true},
	}}

	// The first address of the node-address interface, with its prefix stripped.
	if got := cfg.WireGuardNodeAddress(); got != "10.20.0.5" {
		t.Errorf("WireGuardNodeAddress() = %q, want %q", got, "10.20.0.5")
	}

	none := &Config{WireGuard: []WireGuardInterface{{Name: "wg0", Address: WireGuardAddresses{"10.10.0.5/24"}}}}
	if got := none.WireGuardNodeAddress(); got != "" {
		t.Errorf("WireGuardNodeAddress() = %q, want empty", got)
	}
}

func TestWireGuardAddressUnmarshalsScalarOrList(t *testing.T) {
	scalar, err := Parse([]byte("role: single\nwireguard:\n  - name: wg0\n    address: 10.10.0.2/24\n    privateKey: " + wgKey(1) + "\n"))
	if err != nil {
		t.Fatalf("Parse(scalar address) error = %v", err)
	}

	if len(scalar.WireGuard) != 1 || len(scalar.WireGuard[0].Address) != 1 ||
		scalar.WireGuard[0].Address[0] != "10.10.0.2/24" {
		t.Errorf("scalar address parsed as %v, want a single-element list", scalar.WireGuard[0].Address)
	}

	list, err := Parse([]byte("role: single\nwireguard:\n  - name: wg0\n    address: [10.10.0.2/24, fd00::2/128]\n    privateKey: " + wgKey(1) + "\n"))
	if err != nil {
		t.Fatalf("Parse(list address) error = %v", err)
	}

	if len(list.WireGuard[0].Address) != 2 || list.WireGuard[0].Address[1] != "fd00::2/128" {
		t.Errorf("list address parsed as %v, want two elements", list.WireGuard[0].Address)
	}
}
