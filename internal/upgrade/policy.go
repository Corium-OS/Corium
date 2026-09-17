// Package upgrade moves a node to another OS image, and refuses to move it
// somewhere it should not go.
//
// The refusal is the interesting part. `bootc switch` will happily put a
// Kubernetes node onto Silverblue, and the operator who typed it will not find
// out until the machine comes back as something else. What stops that here is
// the node's own container signing policy: an image is acceptable only if the
// policy demands a signature for it.
package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultPolicyPath is containers-policy(5), which the image already ships
// configured to require a cosign signature for Corium's own repository.
const DefaultPolicyPath = "/etc/containers/policy.json"

// policy is the subset of containers-policy(5) that matters here.
type policy struct {
	Default    []requirement             `json:"default"`
	Transports map[string]scopedPolicies `json:"transports"`
}

type scopedPolicies map[string][]requirement

type requirement struct {
	Type string `json:"type"`
}

// signed reports whether a requirement actually demands a signature.
//
// insecureAcceptAnything and reject are both "no signature is checked" for this
// purpose -- the second refuses outright, which the pull would catch anyway.
func (r requirement) signed() bool {
	switch r.Type {
	case "signedBy", "sigstoreSigned":
		return true
	default:
		return false
	}
}

// ErrUnsigned reports an image the node's policy would accept without a
// signature.
var ErrUnsigned = errors.New("the node's signing policy does not require a signature for this image")

// checkPolicy refuses an image the node would pull without checking who built
// it.
//
// This is a stronger check than reading the image's labels, which is what an
// earlier draft of ADR 4 proposed: a label saying "Corium" can be set by
// anybody, so it catches a typo and nothing else. A signature requirement
// catches the typo and the attacker, and it needs no tooling the image does
// not already have -- there is no skopeo, podman or jq on a Corium node, and
// bootc's status output carries no labels.
//
// It is also the right shape for derived images, which Corium supports: an
// operator building their own adds their repository and key to the policy, and
// their image becomes acceptable because they said so.
func checkPolicy(path, image string) error {
	if path == "" {
		path = DefaultPolicyPath
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path is a constant, overridden only by tests
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var parsed policy
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	requirements := parsed.requirementsFor(image)
	for _, requirement := range requirements {
		if requirement.signed() {
			return nil
		}
	}

	return fmt.Errorf("%w: %s. Add it to %s with the key that signs it, or "+
		"upgrade to an image that is already covered", ErrUnsigned, image, path)
}

// requirementsFor resolves an image reference the way containers-policy(5)
// does: most specific scope first, then successively broader namespaces, then
// the registry, then the transport default, then the global default.
//
// Getting this order wrong in the permissive direction would mean accepting an
// image the node is about to refuse to pull, so the failure would move from a
// clear message here to a confusing one in the middle of an upgrade.
func (p policy) requirementsFor(image string) []requirement {
	scopes, ok := p.Transports["docker"]
	if !ok {
		return p.Default
	}

	for _, scope := range candidateScopes(image) {
		if requirements, found := scopes[scope]; found {
			return requirements
		}
	}

	// The transport's own catch-all, written as an empty scope.
	if requirements, found := scopes[""]; found {
		return requirements
	}

	return p.Default
}

// candidateScopes lists the policy scopes that could match a reference, most
// specific first.
func candidateScopes(image string) []string {
	// The full reference, tag or digest included, is the most specific scope a
	// policy can name.
	candidates := []string{image}

	repository := repositoryOf(image)
	if repository != image {
		candidates = append(candidates, repository)
	}

	// Then every namespace above it: ghcr.io/corium-os/corium, then
	// ghcr.io/corium-os, then ghcr.io.
	for {
		cut := strings.LastIndex(repository, "/")
		if cut < 0 {
			break
		}

		repository = repository[:cut]
		candidates = append(candidates, repository)
	}

	return candidates
}

// repositoryOf strips the tag or digest from a reference.
func repositoryOf(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		return image[:at]
	}

	// A colon before the first slash is a port, not a tag: localhost:5000/x.
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		return image[:colon]
	}

	return image
}
