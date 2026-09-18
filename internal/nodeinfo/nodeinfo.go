// Package nodeinfo answers the question "what is this machine, right now".
//
// It is read-only and it guesses at nothing: every field is either read from a
// file the node owns or from a command it ships, and anything that cannot be
// determined comes back empty rather than approximated. An operator reaching
// for this is usually about to do something irreversible to the machine, and a
// plausible-looking wrong answer is worse than a blank.
package nodeinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StateFile records what a node was actually made into, written by
// corium-agent when the bootstrap succeeds.
//
// It is read here in preference to re-reading the configuration, because the
// configuration says what somebody asked for and this says what happened. On a
// node whose cloud-config has been edited since, only one of those is true.
const StateFile = "/var/lib/corium/node.json"

// MarkerFile is what corium-agent has written on a successful bootstrap since
// before StateFile existed.
//
// It is the only thing a node bootstrapped by 0.1.0 leaves behind, and reading
// it is what stops such a node reporting itself as never bootstrapped after an
// upgrade -- which would have `cctl upgrade` refuse to touch every machine in
// service today, since its health gate asks exactly that.
const MarkerFile = "/var/lib/corium/bootstrapped"

// State is what corium-agent writes when a bootstrap completes.
type State struct {
	Role    string `json:"role"`
	Cluster string `json:"cluster,omitempty"`

	// Endpoint is the address clients should use to reach this cluster's
	// control plane: the virtual IP on an HA node, cluster.endpoint where one
	// was set, and empty when neither applies -- which means the node's own
	// address is the answer.
	//
	// It is recorded here rather than worked out later for the same reason the
	// role is: it was true when the node was made, and a configuration edited
	// since describes an intention rather than a machine.
	Endpoint string `json:"endpoint,omitempty"`

	BootstrappedAt time.Time `json:"bootstrappedAt"`
}

// Runner executes a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Inspector reads a node's state.
//
// Root and Run exist so that this package can be tested against a constructed
// filesystem and a stubbed set of commands, rather than only on a real node.
// Both are empty in production.
type Inspector struct {
	// Root prefixes every path read. Empty means the real filesystem.
	Root string

	// Run executes commands. Nil means actually execute them.
	Run Runner
}

// Node is everything this package can say about a machine.
//
// Fields are omitted when unknown, which is a deliberate part of the contract:
// a client showing this to a person should show a gap rather than invent a
// default.
type Node struct {
	Hostname  string `json:"hostname,omitempty"`
	MachineID string `json:"machineID,omitempty"`

	// Bootstrapped is false on a machine that was provisioned without a Corium
	// configuration, which is a valid outcome rather than a fault.
	Bootstrapped bool   `json:"bootstrapped"`
	Role         string `json:"role,omitempty"`
	Cluster      string `json:"cluster,omitempty"`

	// Endpoint is where clients reach this cluster's control plane. Empty
	// means the node's own address.
	Endpoint string `json:"endpoint,omitempty"`

	OS         OS         `json:"os"`
	Kubernetes Kubernetes `json:"kubernetes"`
	Health     Health     `json:"health"`

	// Management is filled in by the daemon rather than read from the machine,
	// because only the daemon knows it. It is here so that one request answers
	// "what is this node" completely, including the part an operator is least
	// likely to think to ask about.
	Management Management `json:"management,omitempty"`
}

// Management describes how this node can be, and was, managed.
type Management struct {
	// ClaimedBy is how ownership was established: configuration, pairing-code
	// or open. Empty on a node claimed before this was recorded.
	ClaimedBy string `json:"claimedBy,omitempty"`

	// Unauthenticated is true when whoever claimed this node proved nothing.
	// The node holds the same pinned CA either way, so without this there is
	// no way to tell afterwards.
	Unauthenticated bool `json:"unauthenticated,omitempty"`

	// OpenEnrolment is true while the node will let anyone who reaches it
	// claim it.
	OpenEnrolment bool `json:"openEnrolment,omitempty"`
}

// OS describes the image this machine booted.
type OS struct {
	Name   string `json:"name,omitempty"`
	Kernel string `json:"kernel,omitempty"`

	// Booted is what is running. Staged is what the next boot would run, and
	// is absent when nothing is waiting.
	Booted *Deployment `json:"booted,omitempty"`
	Staged *Deployment `json:"staged,omitempty"`
}

// Deployment is one bootc deployment.
type Deployment struct {
	// Image is the reference, which may be a mutable tag.
	Image string `json:"image,omitempty"`

	// Digest is what was actually booted, whatever the tag said at the time.
	// In an incident this is the field that matters.
	Digest  string `json:"digest,omitempty"`
	Version string `json:"version,omitempty"`
}

// Kubernetes describes k0s on this node.
type Kubernetes struct {
	Version string `json:"version,omitempty"`
	Service string `json:"service,omitempty"`
	Active  bool   `json:"active"`
}

// Health is what the machine thinks of itself.
type Health struct {
	// Greenboot is "passed", "failed" or empty when it has not run or is not
	// installed. A node that fails three boots in a row is rolled back to its
	// previous image, so this is the field that predicts a surprise.
	Greenboot string `json:"greenboot,omitempty"`

	UptimeSeconds int64 `json:"uptimeSeconds,omitempty"`
}

// Collect gathers everything, best effort.
//
// It returns no error. Every source here is independent, and one missing file
// must not cost the operator the fields that were readable -- the whole point
// of asking is to find out what state a machine is in, which is most valuable
// exactly when parts of it are broken.
func (i *Inspector) Collect(ctx context.Context) *Node {
	node := &Node{}

	node.Hostname, _ = os.Hostname()
	node.MachineID = strings.TrimSpace(i.read("/etc/machine-id"))

	switch state := i.state(); {
	case state != nil:
		node.Bootstrapped = true
		node.Role = state.Role
		node.Cluster = state.Cluster
		node.Endpoint = state.Endpoint

	case i.exists(MarkerFile):
		// Bootstrapped before this file existed. The role is derived from the
		// unit that was installed rather than recorded, so it distinguishes a
		// control plane from a worker and nothing finer -- k0scontroller backs
		// single, controller and controller+worker alike. That is enough for
		// everything this report is used for, and claiming more would be
		// inventing it.
		node.Bootstrapped = true
		node.Role = i.roleFromUnits(ctx)
	}

	node.OS = i.operatingSystem(ctx)
	node.Kubernetes = i.kubernetes(ctx, node.Role)
	node.Health = i.health(ctx)

	return node
}

func (i *Inspector) state() *State {
	data := i.read(StateFile)
	if data == "" {
		return nil
	}

	var state State
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil
	}

	return &state
}

func (i *Inspector) operatingSystem(ctx context.Context) OS {
	system := OS{
		Name:   osRelease(i.read("/etc/os-release"), "PRETTY_NAME"),
		Kernel: strings.TrimSpace(i.read("/proc/sys/kernel/osrelease")),
	}

	system.Booted, system.Staged = i.deployments(ctx)

	return system
}

// bootcStatus mirrors only the parts of `bootc status --format json` that are
// used here.
//
// bootc documents this schema as unstable and free to change between releases,
// so it is parsed permissively: unknown fields are ignored, and a shape that no
// longer matches yields empty deployments rather than an error. Reporting "I
// could not tell" is a fair answer; refusing to report the fields that did
// parse is not.
type bootcStatus struct {
	Status struct {
		Booted *bootcDeployment `json:"booted"`
		Staged *bootcDeployment `json:"staged"`
	} `json:"status"`
}

type bootcDeployment struct {
	Image struct {
		Image struct {
			Image string `json:"image"`
		} `json:"image"`
		ImageDigest string `json:"imageDigest"`
		Version     string `json:"version"`
	} `json:"image"`
}

func (d *bootcDeployment) convert() *Deployment {
	if d == nil {
		return nil
	}

	return &Deployment{
		Image:   d.Image.Image.Image,
		Digest:  d.Image.ImageDigest,
		Version: d.Image.Version,
	}
}

func (i *Inspector) deployments(ctx context.Context) (booted, staged *Deployment) {
	output, err := i.run(ctx, "bootc", "status", "--format", "json")
	if err != nil {
		return nil, nil
	}

	var status bootcStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, nil
	}

	return status.Status.Booted.convert(), status.Status.Staged.convert()
}

func (i *Inspector) kubernetes(ctx context.Context, role string) Kubernetes {
	kubernetes := Kubernetes{Service: serviceFor(role)}

	if output, err := i.run(ctx, "k0s", "version"); err == nil {
		kubernetes.Version = strings.TrimSpace(string(output))
	}

	if kubernetes.Service != "" {
		output, err := i.run(ctx, "systemctl", "is-active", kubernetes.Service)
		// is-active exits non-zero for anything but active, so the exit status
		// carries the answer and is not a failure to report.
		kubernetes.Active = err == nil && strings.TrimSpace(string(output)) == "active"
	}

	return kubernetes
}

// roleFromUnits works backwards from the unit a bootstrap installed.
//
// The same signal the greenboot health check uses, and for the same reason: it
// is the only one a node that predates node.json has.
func (i *Inspector) roleFromUnits(ctx context.Context) string {
	for unit, role := range map[string]string{
		"k0scontroller.service": "controller",
		"k0sworker.service":     "worker",
	} {
		if _, err := i.run(ctx, "systemctl", "cat", unit); err == nil {
			return role
		}
	}

	return ""
}

// serviceFor names the unit a role installs. It mirrors k0s.ServiceName rather
// than importing it, because that package is about building a node and this one
// is about reading it -- and because the mapping is a fact about k0s, not a
// decision either package gets to make.
func serviceFor(role string) string {
	switch role {
	case "worker":
		return "k0sworker.service"
	case "single", "controller", "controller+worker":
		return "k0scontroller.service"
	default:
		return ""
	}
}

func (i *Inspector) health(ctx context.Context) Health {
	health := Health{UptimeSeconds: uptime(i.read("/proc/uptime"))}

	output, err := i.run(ctx, "systemctl", "show",
		"--property=Result", "--value", "greenboot-healthcheck.service")
	if err != nil {
		return health
	}

	// systemd reports "success" for a unit that completed, and an assortment
	// of reasons otherwise. Anything that is not success is a failed health
	// check, and three of those in a row roll the node back.
	switch result := strings.TrimSpace(string(output)); result {
	case "":
	case "success":
		health.Greenboot = "passed"
	default:
		health.Greenboot = "failed"
	}

	return health
}

// exists reports whether a path is there. It is separate from read because the
// bootstrap marker is an empty file: its contents say nothing and its presence
// says everything.
func (i *Inspector) exists(path string) bool {
	_, err := os.Stat(filepath.Join(i.Root, path))

	return err == nil
}

// read returns a file's contents, or empty if it cannot be read. Callers treat
// absence and unreadability the same way, because for reporting they are.
//
// Every path passed here is a literal in this file, and Root is set only by
// tests: nothing a client sends reaches either, which is what makes reading a
// computed path safe. Keep it that way -- the moment a caller can choose the
// path, this becomes a way to read any file on the node through an API that
// deliberately has no such endpoint.
func (i *Inspector) read(path string) string {
	data, err := os.ReadFile(filepath.Join(i.Root, path)) //nolint:gosec // G304: paths are literals, Root is test-only
	if err != nil {
		return ""
	}

	return string(data)
}

func (i *Inspector) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if i.Run != nil {
		return i.Run(ctx, name, args...)
	}

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}

// osRelease pulls one key out of an os-release file, unquoting it.
func osRelease(contents, key string) string {
	for line := range strings.SplitSeq(contents, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || name != key {
			continue
		}

		return strings.Trim(value, `"'`)
	}

	return ""
}

// uptime reads the first field of /proc/uptime, which is seconds since boot.
func uptime(contents string) int64 {
	field, _, _ := strings.Cut(strings.TrimSpace(contents), " ")

	seconds, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0
	}

	return int64(seconds)
}
