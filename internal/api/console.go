package api

import (
	"crypto/tls"
	"fmt"
	"log/slog"
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

	if !s.Unenrolled() {
		slog.Info("serving the management API",
			"address", address,
			"fingerprint", fingerprint,
			"clientAuth", "operator CA")

		return
	}

	slog.Warn("node is unenrolled and is not in a cluster",
		"address", address, "fingerprint", fingerprint)

	banner := s.enroller.Banner(address, fingerprint)

	fmt.Print(banner)
	writeConsole(banner)
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
