// Package issue renders the Corium console status banner.
//
// The banner is an agetty issue file: the text a getty draws above the login
// prompt. Its whole audience is somebody physically in front of the machine --
// at a hypervisor's console view, a serial port, a KVM -- who wants to know
// what this node is before they log in, and often cannot log in at all. So it
// answers the three questions that view is otherwise silent on: what mode this
// node is in, which cluster it belongs to, and whether it is healthy.
//
// Rendering is pure and lives here so it can be tested against a constructed
// node; writing the file and asking the gettys to redraw is the caller's job.
package issue

import (
	"fmt"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// logo is the Corium wordmark in the figlet "standard" font.
//
// It is stored with the single backslashes an operator should see; escape()
// doubles every one before it reaches the issue file, because agetty reads a
// backslash as the start of an escape sequence and would otherwise eat half of
// the drawing.
const logo = "" +
	"  ____           _\n" +
	" / ___|___  _ __(_)_   _ _ __ ___\n" +
	"| |   / _ \\| '__| | | | | '_ ` _ \\\n" +
	"| |__| (_) | |  | | |_| | | | | | |\n" +
	" \\____\\___/|_|  |_|\\__,_|_| |_| |_|\n"

// Render builds the console status banner for a node.
//
// The result is a complete issue file. Every literal backslash is doubled so
// the wordmark survives agetty's escape handling, and the address is left as a
// single \4 -- agetty's escape for this machine's IPv4 -- so the getty fills it
// in fresh each time it draws the prompt, and the one field that changes on its
// own stays right between refreshes.
func Render(node *nodeinfo.Node) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(escape(logo))
	b.WriteString("\n")

	line := func(label, value string) {
		if value == "" {
			return
		}

		fmt.Fprintf(&b, "  %-8s  %s\n", label, escape(value))
	}

	if !node.Bootstrapped {
		// A node with no Corium configuration is a valid outcome, not a fault;
		// the same neutral wording covers a node still waiting to be claimed,
		// whose pairing code corium-apid prints just below this.
		b.WriteString("  This node is not part of a cluster.\n")
	} else {
		line("role", node.Role)
		line("cluster", node.Cluster)
		line("endpoint", node.Endpoint)

		if node.Kubernetes.Version != "" || node.Kubernetes.Service != "" {
			line("k0s", k0sState(node.Kubernetes))
		}

		line("health", node.Health.Greenboot)

		if booted := node.OS.Booted; booted != nil {
			line("image", image(booted))
		}

		if staged := node.OS.Staged; staged != nil {
			line("staged", image(staged)+"  (next reboot)")
		}

		if node.Health.UptimeSeconds > 0 {
			line("uptime", uptime(node.Health.UptimeSeconds))
		}
	}

	// The address line is written by hand rather than through line(): its value
	// is the raw escape \4, and passing it through escape() would double the
	// backslash and leave the literal text "\4" on the screen.
	fmt.Fprintf(&b, "  %-8s  ", "address")
	b.WriteString(`\4`)
	b.WriteString("\n\n")

	return b.String()
}

// escape doubles every backslash so agetty prints it rather than reading it as
// the start of an escape sequence.
func escape(s string) string {
	return strings.ReplaceAll(s, `\`, `\\`)
}

// k0sState reads "v1.36.4+k0s.0 (running)", or just the state when the version
// could not be read.
func k0sState(k nodeinfo.Kubernetes) string {
	state := "not running"
	if k.Active {
		state = "running"
	}

	if k.Version == "" {
		return state
	}

	return k.Version + " (" + state + ")"
}

// image renders a deployment as "0.2.0  sha256:1a2b3c4d5e6f": the version an
// operator recognises, and enough of the digest to tell two of them apart. It
// falls back to the reference when neither parsed, and to nothing when the
// deployment is empty, so line() drops it rather than printing a blank.
func image(d *nodeinfo.Deployment) string {
	var parts []string

	if d.Version != "" {
		parts = append(parts, d.Version)
	}

	if short := shortDigest(d.Digest); short != "" {
		parts = append(parts, short)
	}

	if len(parts) == 0 {
		return d.Image
	}

	return strings.Join(parts, "  ")
}

// shortDigest keeps a digest to a length a console can show without wrapping.
//
// Twelve hex characters is what `podman images` shows and what an operator
// eyeballing "is this the image I expect" needs; the full digest is in
// `cctl inspect` for anyone who needs to match it exactly. A digest that is not
// the sha256 shape this understands is returned whole rather than mangled.
func shortDigest(digest string) string {
	const algo = "sha256:"

	hex, ok := strings.CutPrefix(digest, algo)
	if !ok || len(hex) <= 12 {
		return digest
	}

	return algo + hex[:12]
}

// uptime renders a duration the way an operator says it: "5m", "3h 12m",
// "2d 4h". Seconds are dropped because on a console nobody is timing anything to
// one, and the label is about how long the node has been up, not how precisely.
func uptime(seconds int64) string {
	d := time.Duration(seconds) * time.Second

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
