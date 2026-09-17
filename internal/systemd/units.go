// Package systemd reads and acts on the services a Corium node runs, and reads
// their journals.
//
// Everything here works from an allowlist. A management API that takes a unit
// name and passes it to systemctl is a management API that can start anything
// on the machine, which is the same thing as a remote shell with extra steps —
// and the whole argument for this API's surface being small rests on it not
// being that.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Unit is one service this API will talk about.
type Unit struct {
	Name string `json:"name"`

	// What it is, in Corium's terms rather than systemd's, because the person
	// reading this did not write the unit file.
	Purpose string `json:"purpose"`

	// Restartable units are the ones an operator may cycle. It is a much
	// shorter list than the readable ones, and deliberately so.
	Restartable bool `json:"restartable"`
}

// known is every unit this API will name.
//
// Read access is broad because reading a journal is how an operator works out
// what went wrong. Restart access is narrow because most of these do something
// exactly once, and doing it again is how a node loses data:
// corium-bootstrap.service re-running on a node that has already joined a
// cluster is the failure this project guards against everywhere else.
var known = []Unit{
	{"k0scontroller.service", "the Kubernetes control plane", true},
	{"k0sworker.service", "the kubelet and container runtime", true},
	{"corium-bootstrap.service", "first-boot configuration; runs once", false},
	{"corium-apid.service", "this API", false},
	{"corium-upgrade-download.service", "stages a newer OS image", false},
	{"corium-upgrade-apply.service", "drains and reboots into a staged image", false},
	{"corium-uncordon.service", "returns the node to service after an upgrade", false},
	{"greenboot-healthcheck.service", "decides whether this boot is healthy", false},
	{"cloud-final.service", "cloud-init; where provisioning problems surface", false},
}

// KernelUnit is the reserved name for the kernel's own messages, which are in
// the journal rather than in a unit. Giving it a name here means the log
// endpoint needs no second shape for dmesg.
const KernelUnit = "kernel"

var (
	// ErrUnknownUnit reports a name that is not on the list.
	ErrUnknownUnit = errors.New("not a unit this API knows about")

	// ErrNotRestartable reports a unit that may be read but not cycled.
	ErrNotRestartable = errors.New("this unit is not one the API will restart")
)

// Units returns the allowlist.
func Units() []Unit {
	out := make([]Unit, len(known))
	copy(out, known)

	return out
}

// lookup finds a unit by name, accepting the bare name as well as the full one
// because `cctl logs k0sworker` is what somebody will type.
func lookup(name string) (Unit, bool) {
	for _, unit := range known {
		if unit.Name == name || strings.TrimSuffix(unit.Name, ".service") == name {
			return unit, true
		}
	}

	return Unit{}, false
}

// Status is what systemd says about a unit right now.
type Status struct {
	Unit

	// Active is systemd's ActiveState: active, inactive, failed, activating.
	Active string `json:"active"`

	// Sub is the finer state: running, exited, dead. A oneshot that succeeded
	// reads active/exited, which looks alarming until you know that.
	Sub string `json:"sub"`

	// Enabled is the unit file state. Corium's units are enabled in the image,
	// so anything else here means somebody changed it on this machine.
	Enabled string `json:"enabled,omitempty"`

	// Since is when it entered its current state.
	Since string `json:"since,omitempty"`

	// Result is how it last finished: success, exit-code, timeout. For a
	// oneshot this is the only field that says whether it worked.
	Result string `json:"result,omitempty"`
}

// Runner executes a command and returns its standard output. It exists so this
// package can be tested without a systemd to talk to.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager talks to systemd.
type Manager struct {
	// Run executes commands. Nil means actually execute them.
	Run Runner
}

// List reports on every unit in the allowlist.
//
// A unit that does not exist on this node — the worker service on a controller,
// for instance — is reported as inactive rather than omitted or errored. Which
// units a role does not have is itself worth seeing.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	statuses := make([]Status, 0, len(known))

	for _, unit := range known {
		status, err := m.status(ctx, unit)
		if err != nil {
			return nil, err
		}

		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	return statuses, nil
}

// Status reports on one unit.
func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	unit, ok := lookup(name)
	if !ok {
		return Status{}, fmt.Errorf("%q: %w", name, ErrUnknownUnit)
	}

	return m.status(ctx, unit)
}

func (m *Manager) status(ctx context.Context, unit Unit) (Status, error) {
	// One call for every property, parsed as Key=Value rather than with
	// --value: positional output is one reordering away from silently
	// reporting the wrong field as the right one.
	output, err := m.run(ctx, "systemctl", "show",
		"--property=ActiveState,SubState,UnitFileState,Result,ActiveEnterTimestamp",
		unit.Name)
	if err != nil {
		// `systemctl show` answers for units that do not exist, so a failure
		// here means systemd itself is unreachable, which is worth reporting
		// rather than papering over.
		return Status{}, fmt.Errorf("asking systemd about %s: %w", unit.Name, err)
	}

	properties := parseProperties(string(output))

	return Status{
		Unit:    unit,
		Active:  properties["ActiveState"],
		Sub:     properties["SubState"],
		Enabled: properties["UnitFileState"],
		Since:   properties["ActiveEnterTimestamp"],
		Result:  properties["Result"],
	}, nil
}

// Restart cycles a unit, if it is one the API will cycle.
//
// The check is here and not only in the caller: whether restarting something
// can destroy a node is a property of the unit, and belongs with the unit.
func (m *Manager) Restart(ctx context.Context, name string) (Status, error) {
	unit, ok := lookup(name)
	if !ok {
		return Status{}, fmt.Errorf("%q: %w", name, ErrUnknownUnit)
	}

	if !unit.Restartable {
		return Status{}, fmt.Errorf("%s: %w (%s)", unit.Name, ErrNotRestartable, unit.Purpose)
	}

	if _, err := m.run(ctx, "systemctl", "restart", unit.Name); err != nil {
		return Status{}, fmt.Errorf("restarting %s: %w", unit.Name, err)
	}

	// Report what happened rather than assuming it worked. `systemctl restart`
	// returning zero means the job was accepted, not that the service is up.
	return m.status(ctx, unit)
}

func parseProperties(output string) map[string]string {
	properties := map[string]string{}

	for line := range strings.SplitSeq(output, "\n") {
		key, value, found := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !found {
			continue
		}

		// systemd writes this for a timestamp that has never happened.
		if value == "n/a" || value == "" {
			continue
		}

		properties[key] = value
	}

	return properties
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	// A command that hangs must not hold a request open forever; the journal
	// reader below manages its own lifetime instead.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}
