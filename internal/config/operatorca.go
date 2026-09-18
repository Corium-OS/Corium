package config

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPrivateKey reports that what was offered as an operator CA is a private
// key. It is a sentinel because the mistake deserves more than a parse error:
// whoever made it has published the key that owns their fleet, and a caller
// that can say so in context should.
var ErrPrivateKey = errors.New("this is a private key, not a certificate")

// ParseOperatorCA parses an operator CA certificate and checks that it can do
// the job it is being trusted with.
//
// It is exported because two places must agree on the answer: the schema, when
// an operator writes api.operatorCA, and the daemon, when a client presents a
// CA during enrolment. A node that accepted over the wire what its own
// validation would have rejected would be a node whose configuration lies
// about its trust anchor.
//
// Semantic problems are reported together rather than one per call, for the
// same reason the rest of validation does it.
func ParseOperatorCA(pemData []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(pemData)

	switch {
	case block == nil:
		return nil, errors.New("not PEM data; expected a -----BEGIN CERTIFICATE----- block")
	case strings.Contains(block.Type, "PRIVATE KEY"):
		return nil, fmt.Errorf("%w (%s)", ErrPrivateKey, block.Type)
	case block.Type != "CERTIFICATE":
		return nil, fmt.Errorf("expected a CERTIFICATE block, got %q", block.Type)
	case len(bytes.TrimSpace(rest)) > 0:
		// A chain leaves it ambiguous which certificate is the anchor, and the
		// answer decides who can manage the node.
		return nil, errors.New("expected exactly one certificate, got more than one")
	}

	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	var problems []error

	if !certificate.IsCA {
		problems = append(problems, errors.New(
			"certificate is not a CA (basic constraints say CA:FALSE), so it cannot "+
				"sign the client certificates it is here to vouch for"))
	}

	// A zero KeyUsage means the extension is absent, which is permissive rather
	// than wrong. Present and lacking certSign is a refusal.
	if certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		problems = append(problems, errors.New("certificate does not carry the certSign key usage"))
	}

	// Expiry is checked even though it makes the answer depend on the clock.
	// An expired anchor produces a node nobody can manage, and the moment to
	// find that out is before the node is committed to it.
	if now := time.Now(); now.After(certificate.NotAfter) {
		problems = append(problems, fmt.Errorf(
			"certificate expired on %s", certificate.NotAfter.Format(time.RFC3339)))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	return certificate, nil
}
