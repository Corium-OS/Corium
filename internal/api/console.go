package api

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// consoleDevice is where a message meant for whoever is physically in front of
// the machine goes. Writing to it reaches every console the kernel was told
// about, including the serial port -- which is the one that matters for a node
// in a rack, and the one a cloud exposes as an instance's console log.
const consoleDevice = "/dev/console"

// issueFile is what puts the pairing code on a screen.
//
// A Corium node boots with `console=tty0 console=ttyS0`, and a userspace write
// to /dev/console reaches the *last* of those only -- so the banner below goes
// to the serial port, and the graphical console a hypervisor shows in its own
// UI never saw it.
//
// Writing to every console was tried and removed: every getty renders the
// issue, so the screen showed the banner twice, once from the direct write and
// once from the prompt. One copy in the boot stream and one at every login
// prompt is the right pair.

// The one-shot write scrolls away behind whatever boots afterwards, and on a
// screen nobody can log in to -- an unclaimed node has no users unless its
// configuration made some -- that leaves an operator with a login prompt and
// no code. agetty reprints the issue every time it draws that prompt, so this
// is the copy that stays.
//
// /run rather than /etc: the code is minted per boot and must not outlive it.
// agetty reads /etc/issue.d and /run/issue.d, needs the .issue suffix, and
// only looks at either if /etc/issue exists -- which it does on this image, as
// a symlink into /usr.
const (
	issueDir  = "/run/issue.d"
	issueFile = "10-corium-enrolment.issue"
)

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
		// A claimed node must stop advertising a code that will never work
		// again. This also covers the restart straight after an enrolment.
		clearIssue()

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
	writeIssue(banner)
}

// writeIssue puts the banner where the login prompt will find it.
func writeIssue(banner string) {
	// 0755 because the directory is shared: other packages drop their own
	// .issue files in it, and the convention /etc/issue.d sets is a public
	// one. The file below is not.
	if err := os.MkdirAll(issueDir, 0o755); err != nil { //nolint:gosec // G301: a shared drop-in directory
		slog.Debug("no issue directory for the pairing code", "error", err)

		return
	}

	// 0600, unlike the rest of /run/issue.d. agetty reads it as root and
	// renders it to whoever is looking at the screen, which is the audience;
	// the pairing code does not also need to be readable by every local
	// account on a node that has not yet been claimed.
	path := filepath.Join(issueDir, issueFile)
	if err := os.WriteFile(path, []byte(banner), 0o600); err != nil {
		slog.Debug("writing the issue drop-in", "error", err)

		return
	}

	reloadGetty()
}

// clearIssue removes it once the node has an owner.
func clearIssue() {
	if err := os.Remove(filepath.Join(issueDir, issueFile)); err != nil && !os.IsNotExist(err) {
		slog.Debug("removing the issue drop-in", "error", err)

		return
	}

	reloadGetty()
}

// reloadGetty asks the prompts already on screen to redraw.
//
// Without it the change is only seen by the next prompt, which on a console
// nobody has touched is never. Best effort: a node with no getty running, or
// an agetty too old for the flag, loses the refresh and nothing else.
func reloadGetty() {
	if err := exec.Command("agetty", "--reload").Run(); err != nil {
		slog.Debug("could not ask agetty to redraw", "error", err)
	}
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
