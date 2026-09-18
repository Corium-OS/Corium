package cctl

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
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

	// Before anything else about the node, because it is the thing an operator
	// is least likely to think to ask and most needs to know: this machine's
	// owner was decided by whoever reached it first.
	if node.Management.OpenEnrolment {
		out("\n  !! This node is unclaimed and asks nothing of a claimant.\n" +
			"  !! api.insecure is set, so the first client to reach it owns it.\n")
	} else if node.Management.Unauthenticated {
		out("\n  !! This node was claimed without authentication (api.insecure).\n" +
			"  !! Whoever reached it first chose the CA it now obeys.\n")
	}

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

// priorities are syslog levels, as journald reports them.
var priorities = map[int]string{
	0: "emerg", 1: "alert", 2: "crit", 3: "error",
	4: "warn", 5: "notice", 6: "info", 7: "debug",
}

// FormatRecord prints one journal entry.
//
// The unit is shown because a log read without a unit filter is the common
// case -- an operator who does not yet know where the problem is -- and a
// stream of messages with no attribution is no use to them.
func FormatRecord(w io.Writer, record systemd.Record) {
	level, ok := priorities[record.Priority]
	if !ok {
		level = strconv.Itoa(record.Priority)
	}

	unit := strings.TrimSuffix(record.Unit, ".service")
	if unit == "" {
		unit = "kernel"
	}

	_, _ = fmt.Fprintf(w, "%s %-7s %-22s %s\n",
		record.Time.Local().Format("15:04:05"), level, unit, record.Message)
}
