package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/k0s"
	"github.com/Corium-OS/Corium/internal/secret"
)

// wireGuardConfDir is where wg-quick reads interface configuration from. It is
// the only place wg-quick looks, so the file has to live there -- the same kind
// of deliberate /etc exception the sshd drop-in is. A variable so tests can
// point it at a temporary directory.
var wireGuardConfDir = "/etc/wireguard"

// applyWireGuard brings every declared WireGuard interface up before k0s.
//
// This runs before k0s is installed, and the ordering is the feature. A cluster
// running over the overlay needs the interface up before the kubelet decides
// which address to register and before k0s reaches the control plane it joins;
// an interface brought up afterwards is one k0s has already registered around.
// The interface is also made a boot-time artefact -- an enabled wg-quick unit --
// so it returns on every later boot, since corium-agent runs only on the first.
func applyWireGuard(ctx context.Context, cfg *config.Config) error {
	if len(cfg.WireGuard) == 0 {
		return nil
	}

	for i := range cfg.WireGuard {
		if err := applyInterface(ctx, &cfg.WireGuard[i]); err != nil {
			return fmt.Errorf("wireguard %q: %w", cfg.WireGuard[i].Name, err)
		}
	}

	return requireInterfacesForK0s(cfg)
}

// applyInterface resolves an interface's secrets, writes its configuration, and
// brings it up.
func applyInterface(ctx context.Context, iface *config.WireGuardInterface) error {
	privateKey, err := resolveWireGuardKey(ctx, iface.PrivateKey, iface.PrivateKeyFrom,
		fmt.Sprintf("wireguard %q private key", iface.Name))
	if err != nil {
		return err
	}

	presharedKeys := make([]string, len(iface.Peers))

	for j := range iface.Peers {
		psk, err := resolveWireGuardKey(ctx, iface.Peers[j].PresharedKey, iface.Peers[j].PresharedKeyFrom,
			fmt.Sprintf("wireguard %q peer %d preshared key", iface.Name, j))
		if err != nil {
			return err
		}

		presharedKeys[j] = psk
	}

	conf := renderWireGuardConf(iface, privateKey, presharedKeys)

	path := filepath.Join(wireGuardConfDir, iface.Name+".conf")

	// 0600: the file carries the private key. writeFile creates the parent with
	// a restrictive mode too. The key is deliberately never logged.
	if err := writeFile(path, []byte(conf), 0o600); err != nil {
		return err
	}

	slog.Info("wrote wireguard configuration",
		"interface", iface.Name, "path", path,
		"address", iface.Address, "peers", len(iface.Peers))

	unit := wireGuardUnit(iface.Name)

	// enable, so the interface comes back on every later boot, not just this one.
	if err := run(ctx, "systemctl", "enable", unit); err != nil {
		return err
	}

	// restart rather than start, so a second bootstrap applies a changed
	// configuration instead of leaving the old interface in place.
	if err := run(ctx, "systemctl", "restart", unit); err != nil {
		return err
	}

	slog.Info("wireguard interface is up", "interface", iface.Name, "unit", unit)

	return nil
}

// resolveWireGuardKey produces a key given inline or by reference, and never
// logs it: a private or preshared key is a secret, like a join token.
func resolveWireGuardKey(ctx context.Context, inline string, from *config.SecretSource, what string) (string, error) {
	switch {
	case inline != "":
		return strings.TrimSpace(inline), nil
	case from == nil:
		return "", nil
	}

	return secret.Resolve(ctx, from, what)
}

// renderWireGuardConf renders a wg-quick configuration file.
//
// The layout is deterministic -- peers in declared order, fields in a fixed
// order -- so re-rendering an unchanged interface produces a byte-identical file.
func renderWireGuardConf(iface *config.WireGuardInterface, privateKey string, presharedKeys []string) string {
	var b strings.Builder

	b.WriteString("# Written by corium-agent from corium.wireguard. Do not edit.\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(iface.Address, ", "))

	if iface.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", iface.ListenPort)
	}

	if iface.MTU != 0 {
		fmt.Fprintf(&b, "MTU = %d\n", iface.MTU)
	}

	if privateKey != "" {
		fmt.Fprintf(&b, "PrivateKey = %s\n", privateKey)
	}

	for j := range iface.Peers {
		peer := &iface.Peers[j]

		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", peer.PublicKey)

		if j < len(presharedKeys) && presharedKeys[j] != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", presharedKeys[j])
		}

		if peer.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", peer.Endpoint)
		}

		if len(peer.AllowedIPs) > 0 {
			fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(peer.AllowedIPs, ", "))
		}

		if peer.PersistentKeepalive != 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", peer.PersistentKeepalive)
		}
	}

	return b.String()
}

// wireGuardUnit is the wg-quick template instance for an interface.
func wireGuardUnit(name string) string {
	return "wg-quick@" + name + ".service"
}

// requireInterfacesForK0s stops k0s starting before its overlay is up.
//
// k0s registers this node over the overlay, so a service that starts before
// wg-quick has brought the interface up would register at the wrong address, or
// fail to reach the control plane it is joining. The drop-in orders the k0s unit
// after every declared interface and requires them, so a failed interface stops
// the service loudly rather than producing a node that half-joins -- the same
// mechanism a RAID mount uses.
func requireInterfacesForK0s(cfg *config.Config) error {
	unit := k0s.ServiceName(cfg.Role)
	dir := filepath.Join("/etc/systemd/system", unit+".d")

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // systemd unit drop-in directories are 0755
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	content := "# Written by corium-agent from corium.wireguard.\n" +
		"#\n" +
		"# k0s registers this node over the overlay, so it must not start until the\n" +
		"# WireGuard interface carrying its address is up. Requires, not just After,\n" +
		"# so a node whose overlay failed does not half-join at the wrong address.\n" +
		"[Unit]\n"

	for i := range cfg.WireGuard {
		unit := wireGuardUnit(cfg.WireGuard[i].Name)
		content += "After=" + unit + "\n"
		content += "Requires=" + unit + "\n"
	}

	path := filepath.Join(dir, "10-corium-wireguard.conf")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // unit drop-ins are world-readable
		return fmt.Errorf("writing %s: %w", path, err)
	}

	slog.Info("k0s will wait for the wireguard interfaces",
		"unit", unit, "interfaces", len(cfg.WireGuard))

	return nil
}
