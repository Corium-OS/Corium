package bootstrap

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
)

// detectNodeIP finds the address this node should register with, excluding a
// virtual IP it may currently hold.
//
// Without this, a controller holding the VRRP virtual IP registers *that* as
// its node address, because the kubelet picks whatever it finds on the
// interface. The cluster then believes the node lives at an address that will
// move to a different machine at the next failover, and everything addressed to
// the node — logs, exec, port-forward, metrics — goes to the wrong one.
//
// The failure is delayed and confusing: it works perfectly until the first
// failover, which is usually an incident already.
func detectNodeIP(virtualIP string) (string, error) {
	excluded, err := virtualAddress(virtualIP)
	if err != nil {
		return "", err
	}

	// The address the kernel would use to reach the outside world, which is the
	// node's real address on a normally configured machine. No packet is sent:
	// a UDP "connection" only fixes a route.
	if addr, err := routedAddress(); err == nil && addr.IsValid() {
		if !excluded.IsValid() || addr != excluded {
			return addr.String(), nil
		}

		// Keepalived can leave the virtual IP as the address the default route
		// prefers, in which case the routed address is exactly the one we must
		// not register.
		slog.Debug("routed address is the virtual IP, falling back to interface scan")
	}

	return scanInterfaces(excluded)
}

// virtualAddress extracts the bare address from a CIDR-form virtual IP.
func virtualAddress(virtualIP string) (netip.Addr, error) {
	if virtualIP == "" {
		return netip.Addr{}, nil
	}

	prefix, err := netip.ParsePrefix(virtualIP)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parsing virtual IP %q: %w", virtualIP, err)
	}

	return prefix.Addr(), nil
}

// routedAddress reports the local address of the default route.
func routedAddress() (netip.Addr, error) {
	conn, err := net.Dial("udp", "203.0.113.1:9")
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("unexpected local address type %T", conn.LocalAddr())
	}

	addr, ok := netip.AddrFromSlice(local.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("unparseable local address %v", local.IP)
	}

	return addr.Unmap(), nil
}

// scanInterfaces picks the first usable address that is not the virtual IP.
func scanInterfaces(excluded netip.Addr) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, raw := range addrs {
			ipNet, ok := raw.(*net.IPNet)
			if !ok {
				continue
			}

			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}

			addr = addr.Unmap()

			switch {
			case !addr.Is4():
				continue
			case addr.IsLoopback() || addr.IsLinkLocalUnicast():
				continue
			case excluded.IsValid() && addr == excluded:
				continue
			}

			return addr.String(), nil
		}
	}

	return "", fmt.Errorf("no usable address found that is not the virtual IP %v", excluded)
}
