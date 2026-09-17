package api

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Corium-OS/Corium/internal/config"
)

// maxCABody caps the request. Two certificates is a few kilobytes.
const maxCABody = 64 << 10

// rotateRequest replaces the CA a node obeys.
type rotateRequest struct {
	// OperatorCA is the new anchor, PEM encoded.
	OperatorCA string `json:"operatorCA"`

	// Proof is a client certificate signed by that CA.
	//
	// It is required because the failure this guards against is not subtle and
	// is not recoverable over the network: rotate to a CA you cannot issue
	// certificates under, and the next request is refused by a node that will
	// now only ever accept somebody else. Recovery is a trip to the console.
	//
	// What it proves is bounded, and worth stating plainly: that the caller
	// has a certificate the new CA signed. It is a guard against a mistyped
	// path or the wrong file, not a proof that the caller holds the new key.
	// cctl mints this certificate as part of rotating, so in practice the two
	// coincide.
	Proof string `json:"proof"`
}

// handleRotateCA hands the node to a different operator CA.
func (s *Server) handleRotateCA(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCABody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	var request rotateRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	certificate, err := config.ParseOperatorCA([]byte(request.OperatorCA))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	if err := verifyProof(certificate, request.Proof); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	if err := s.store.Rotate([]byte(request.OperatorCA)); err != nil {
		if errors.Is(err, ErrUnenrolled) {
			// Rotating an unclaimed node would be enrolment by another name,
			// and would walk around the pairing code that guards it.
			writeError(w, http.StatusConflict, err.Error())

			return
		}

		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	slog.Warn("operator CA rotated",
		"to", certificate.Subject.CommonName,
		"fingerprint", Fingerprint(certificate.Raw),
		"requestedBy", r.RemoteAddr)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "rotated",
		"operatorCA": Fingerprint(certificate.Raw),
		"note": "the node is restarting; your next request needs a certificate " +
			"signed by the new CA",
	})

	// The listener verifies clients against a pool built when it started, so
	// the new CA only takes effect on a restart. Same reasoning as enrolment:
	// rebuilding TLS under a live listener works until the one request that
	// matters arrives mid-swap.
	s.wantRestart()
}

// verifyProof checks that the offered certificate was signed by the new CA.
func verifyProof(ca *x509.Certificate, proofPEM string) error {
	if proofPEM == "" {
		return errors.New("proof: send a client certificate signed by the new CA, " +
			"so that rotating cannot lock you out of this node")
	}

	block, _ := pem.Decode([]byte(proofPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("proof: expected a PEM certificate")
	}

	proof, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	// The same check the TLS handshake will make after the restart, so that a
	// certificate accepted here is one the node will accept then.
	if _, err := proof.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return errors.New("proof: that certificate was not signed by the CA you are " +
			"rotating to, so rotating would lock you out")
	}

	if roleFromCertificate(proof) == "" {
		// A certificate that authenticates but carries no role would leave the
		// node reachable and useless.
		return errors.New("proof: that certificate carries no Corium role, so it " +
			"could authenticate to this node and do nothing")
	}

	return nil
}
