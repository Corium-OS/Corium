package api

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
)

// consoleDevice is where a message meant for whoever is physically in front of
// the machine goes. Writing to it reaches every console the kernel was told
// about, including the serial port -- which is the one that matters for a node
// in a rack, and the one a cloud exposes as an instance's console log.
const consoleDevice = "/dev/console"

// announce tells the world what this node is waiting for.
//
// The banner goes to the console because its whole purpose is to be read by
// somebody who cannot yet log in -- that is what being unenrolled means -- and
// to stdout, which systemd files in the journal, because on a cloud instance
// the console log is often the only console there is.
//
// Publishing the pairing code this way is safe for the reason the whole scheme
// rests on: reading it requires access the machine's owner has and an attacker
// on the network does not.
func (s *Server) announce(identity tls.Certificate, address string) {
	fingerprint := Fingerprint(identity.Certificate[0])

	// The reachable address in both shapes. An operator grepping the journal
	// for a node's fingerprint is usually about to point a client at it, and
	// [::]:7443 is not an address to point anything at.
	reachable := reachableAddress(address)

	if !s.Unenrolled() {
		slog.Info("serving the management API",
			"address", reachable,
			"fingerprint", fingerprint,
			"clientAuth", "operator CA")

		return
	}

	slog.Warn("node is unenrolled and is not in a cluster",
		"address", reachable, "fingerprint", fingerprint)

	banner := s.enroller.Banner(reachable, fingerprint)

	fmt.Print(banner)
	writeConsole(banner)
}

// reachableAddress turns a listening address into one somebody could type.
//
// A daemon told to listen on every interface reports [::]:7443, which is true
// and useless. The port is what matters and is kept; the host is replaced with
// this machine's first routable address, and left alone when the listener was
// already bound to one.
func reachableAddress(listening string) string {
	host, port, err := net.SplitHostPort(listening)
	if err != nil {
		return listening
	}

	if host != "" && host != "::" && host != "0.0.0.0" {
		return listening
	}

	for _, ip := range localAddresses() {
		// Loopback would be just as unhelpful to somebody reading a console.
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil {
			continue
		}

		return net.JoinHostPort(ip.String(), port)
	}

	// Nothing routable to offer. The port alone still tells an operator what
	// to aim at once they know the node's address.
	return listening
}

// writeConsole is best effort. A container, a test, or a machine with no
// console must not take the daemon down with it, and stdout has already
// carried the same text to the journal.
func writeConsole(message string) {
	console, err := os.OpenFile(consoleDevice, os.O_WRONLY, 0)
	if err != nil {
		slog.Debug("no console to print the pairing code on", "error", err)

		return
	}

	defer func() { _ = console.Close() }()

	if _, err := console.WriteString(message); err != nil {
		slog.Debug("writing to the console", "error", err)
	}
}
