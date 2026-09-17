package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ApplyUnit is the unit that drains this node and reboots it into whatever is
// staged.
//
// Applying an upgrade goes through it rather than reimplementing the sequence.
// That unit already knows the things that cost somebody a node to learn: that a
// drain a pod disruption budget refuses must cancel the upgrade rather than
// force it, that a worker with no cluster admin credentials reboots undrained,
// and that a staged deployment has to be unlocked or the reboot is wasted and
// the node comes back on the image it already had. A second implementation of
// that would be a second thing to get right.
const ApplyUnit = "corium-upgrade-apply.service"

// pullTimeout bounds staging. Pulling an OS image over a slow link is slow;
// pulling one that will never arrive should not hold a request forever.
const pullTimeout = 30 * time.Minute

// Runner executes a command. It exists so this package can be tested without a
// bootc to talk to.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager drives a node's OS upgrades.
type Manager struct {
	// Run executes commands. Nil means actually execute them.
	Run Runner

	// PolicyPath overrides containers-policy(5). Empty means the real one.
	PolicyPath string
}

// Staged is what a node has waiting for its next boot.
type Staged struct {
	Image   string `json:"image,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Version string `json:"version,omitempty"`
}

var (
	// ErrNothingStaged reports an apply with nothing to apply.
	ErrNothingStaged = errors.New("no image is staged")

	// ErrBadReference reports an image reference that is not one.
	ErrBadReference = errors.New("not a valid image reference")
)

// referencePattern is a deliberately strict take on an image reference:
// lowercase registry and path, an optional port, and an optional tag or
// digest. Anything outside it is refused rather than handed to bootc.
//
// The strictness is the point. This value ends up on a command line, and the
// set of references somebody legitimately needs is much smaller than the set
// the grammar allows.
var referencePattern = regexp.MustCompile(
	`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+` +
		`(:[a-zA-Z0-9._-]+|@sha256:[a-f0-9]{64})?$`)

// Stage pulls an image and prepares the node to boot it, without rebooting.
//
// Nothing here reboots anything: staging and applying are separate calls
// because the gap between them is where an operator decides, and where cctl
// checks the rest of the cluster.
func (m *Manager) Stage(ctx context.Context, image string) (*Staged, error) {
	image = strings.TrimSpace(image)

	if !referencePattern.MatchString(image) {
		return nil, fmt.Errorf("%q: %w", image, ErrBadReference)
	}

	// Before the pull, not after: a node that has already downloaded an image
	// it will not run has spent the bandwidth for nothing, and an operator who
	// gets the refusal first can fix the reference.
	if err := checkPolicy(m.PolicyPath, image); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()

	// --apply=false is explicit rather than relying on the default: the
	// difference between this and rebooting the node is one flag.
	if _, err := m.run(ctx, "bootc", "switch", "--apply=false", image); err != nil {
		return nil, fmt.Errorf("staging %s: %w", image, err)
	}

	staged, err := m.Staged(ctx)
	if err != nil {
		return nil, err
	}

	if staged == nil {
		// bootc exited zero and staged nothing, which happens when the node is
		// already on that exact digest. Saying so beats reporting success and
		// leaving the operator to wonder what will happen at the next reboot.
		return nil, fmt.Errorf("%s: %w (the node may already be running it)",
			image, ErrNothingStaged)
	}

	return staged, nil
}

// Staged reports what is waiting, if anything.
func (m *Manager) Staged(ctx context.Context) (*Staged, error) {
	output, err := m.run(ctx, "bootc", "status", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("reading bootc status: %w", err)
	}

	// The same permissive parsing as nodeinfo, for the same reason: bootc
	// documents this schema as unstable.
	var status struct {
		Status struct {
			Staged *struct {
				Image struct {
					Image struct {
						Image string `json:"image"`
					} `json:"image"`
					ImageDigest string `json:"imageDigest"`
					Version     string `json:"version"`
				} `json:"image"`
			} `json:"staged"`
		} `json:"status"`
	}

	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("parsing bootc status: %w", err)
	}

	if status.Status.Staged == nil {
		return nil, nil
	}

	return &Staged{
		Image:   status.Status.Staged.Image.Image.Image,
		Digest:  status.Status.Staged.Image.ImageDigest,
		Version: status.Status.Staged.Image.Version,
	}, nil
}

// Apply drains this node and reboots it into the staged image.
//
// It returns as soon as the unit has been started, because the node is about
// to go away and a request waiting for it to finish would be a request waiting
// for its own connection to be cut.
func (m *Manager) Apply(ctx context.Context) error {
	staged, err := m.Staged(ctx)
	if err != nil {
		return err
	}

	if staged == nil {
		return ErrNothingStaged
	}

	// --no-block for the reason above. The unit's own journal is where the
	// drain and the reboot are reported.
	if _, err := m.run(ctx, "systemctl", "start", "--no-block", ApplyUnit); err != nil {
		return fmt.Errorf("starting %s: %w", ApplyUnit, err)
	}

	return nil
}

// Rollback marks the previous deployment as the one to boot next.
//
// It does not reboot. A node that rolled back and rebooted itself in one call
// would take itself out of service at a moment the operator had not chosen,
// and the whole reason rollback exists is that somebody is already having a
// bad day.
func (m *Manager) Rollback(ctx context.Context) error {
	if _, err := m.run(ctx, "bootc", "rollback"); err != nil {
		return fmt.Errorf("rolling back: %w", err)
	}

	return nil
}

func (m *Manager) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}

	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("running %s: %w", name, err)
	}

	return output, nil
}
