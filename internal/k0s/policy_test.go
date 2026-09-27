package k0s

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheImagePolicyRequiresASignatureForExtensions ties the mirror this
// package pulls from to the signing policy the image ships.
//
// It exists because the two were configured separately and got out of step, in
// the direction that fails silently. The image shipped
// `use-sigstore-attachments` for the k0s repository -- where to *look* for a
// signature -- without a matching rule in policy.json demanding one, so the
// default `insecureAcceptAnything` applied and extensions were pulled with no
// verification at all. Everything worked, which is the problem: a missing
// requirement looks exactly like a satisfied one.
//
// Found on a real node by reading its policy rather than by anything failing.
func TestTheImagePolicyRequiresASignatureForExtensions(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "build", "files", "etc", "containers", "policy.json")

	raw, err := os.ReadFile(path) // #nosec G304 -- a fixed path in this repository.
	if err != nil {
		t.Fatalf("reading the shipped policy: %v", err)
	}

	var policy struct {
		Transports map[string]map[string][]struct {
			Type    string `json:"type"`
			KeyPath string `json:"keyPath"`
		} `json:"transports"`
	}

	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("parsing the shipped policy: %v", err)
	}

	// The scope is the repository, without the tag Reference adds.
	repository := DefaultMirror

	rules, ok := policy.Transports["docker"][repository]
	if !ok {
		t.Fatalf("policy.json has no rule for %s, so the node's default "+
			"(insecureAcceptAnything) applies and extensions are pulled "+
			"unverified; add a sigstoreSigned rule for it", repository)
	}

	if len(rules) == 0 {
		t.Fatalf("policy.json has an empty rule list for %s", repository)
	}

	for _, rule := range rules {
		if rule.Type != "sigstoreSigned" {
			t.Errorf("%s is gated by %q, which demands no signature",
				repository, rule.Type)
		}

		// A keyless rule cannot be enforced on a node: policy matches a signer
		// by subjectEmail and a GitHub Actions certificate carries a URI. The
		// key is what makes the requirement real.
		if !strings.HasSuffix(rule.KeyPath, "cosign.pub") {
			t.Errorf("%s is gated by a rule with keyPath %q, want the shipped "+
				"public key", repository, rule.KeyPath)
		}
	}
}
