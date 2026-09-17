package cctl

import (
	"fmt"
	"io"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// FormatNode lays a node's report out for a person.
//
// Unknown fields are left out rather than shown as a dash or a zero: this is
// usually read just before something irreversible, and a blank is honest where
// a placeholder invites a guess.
func FormatNode(w io.Writer, address string, node *nodeinfo.Node) {
	// Every write goes through here. A report is printed to a terminal or into
	// a buffer, so a write failing means the operator's pipe closed -- there is
	// nothing useful to do about it and nowhere better to say so.
	out := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
	}

	out("%s\n\n", address)

	line := func(label, value string) {
		if value != "" {
			out("  %-12s %s\n", label, value)
		}
	}

	line("hostname", node.Hostname)
	line("machine id", node.MachineID)

	if !node.Bootstrapped {
		out("\n  This machine was provisioned without a Corium configuration,\n" +
			"  so it runs no Kubernetes. That is a valid outcome, not a fault.\n")

		return
	}

	line("role", node.Role)
	line("cluster", node.Cluster)

	out("\n")
	line("os", node.OS.Name)
	line("kernel", node.OS.Kernel)

	if booted := node.OS.Booted; booted != nil {
		line("booted", booted.Image)
		// The digest, not the tag, is what an incident turns on.
		line("digest", booted.Digest)
	}

	if staged := node.OS.Staged; staged != nil {
		out("\n")
		line("staged", staged.Image)
		line("digest", staged.Digest)
		out("  (the next reboot moves this node to the staged image)\n")
	}

	out("\n")
	line("k0s", node.Kubernetes.Version)

	if node.Kubernetes.Service != "" {
		state := "not running"
		if node.Kubernetes.Active {
			state = "running"
		}

		line("service", node.Kubernetes.Service+" ("+state+")")
	}

	line("greenboot", node.Health.Greenboot)

	if node.Health.UptimeSeconds > 0 {
		line("uptime", (time.Duration(node.Health.UptimeSeconds) * time.Second).String())
	}
}
