package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

// claimedFor starts a node already owned by ca.
func claimedFor(t *testing.T, ca *authority) (string, *Store) {
	t.Helper()

	store := newTestStore(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if err := store.RecordClaim(ClaimedWithPairingCode); err != nil {
		t.Fatalf("RecordClaim() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	return serveOn(t, server), store
}

func rotateBody(t *testing.T, ca *authority, proof string) string {
	t.Helper()

	encoded, err := json.Marshal(rotateRequest{OperatorCA: string(ca.pem), Proof: proof})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	return string(encoded)
}

func TestRotatingNeedsProofTheNewCASignedSomething(t *testing.T) {
	// The failure this guards is not recoverable over the network: rotate to a
	// CA you cannot issue certificates under and the node will only ever
	// accept somebody else.
	current := newAuthority(t)
	address, store := claimedFor(t, current)
	next := newAuthority(t)

	admin := client(t, store, current.issue(t, RoleAdmin))

	// No proof at all.
	status, body := postRaw(t, admin, "https://"+address+"/v1/ca/rotate",
		rotateBody(t, next, ""))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "lock you out") {
		t.Errorf("error = %q, want it to say why proof is asked for", message)
	}

	// Proof from the wrong CA: the one still in use, which is the mistake
	// somebody would actually make.
	stale := pemOf(t, current.issue(t, RoleAdmin))

	status, body = postRaw(t, admin, "https://"+address+"/v1/ca/rotate",
		rotateBody(t, next, stale))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, body)
	}

	pinned, err := store.OperatorCA()
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	if pinned.Subject.CommonName != current.certificate.Subject.CommonName {
		t.Error("a refused rotation changed the pinned CA anyway")
	}
}

func TestRotatingRefusesACertificateWithNoRole(t *testing.T) {
	// It would authenticate and be able to do nothing, which is a locked-out
	// node with extra steps.
	current := newAuthority(t)
	address, store := claimedFor(t, current)
	next := newAuthority(t)

	status, body := postRaw(t, client(t, store, current.issue(t, RoleAdmin)),
		"https://"+address+"/v1/ca/rotate",
		rotateBody(t, next, pemOf(t, next.issue(t, "some other org"))))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, body)
	}

	if message, _ := body["error"].(string); !strings.Contains(message, "no Corium role") {
		t.Errorf("error = %q, want it to name the problem", message)
	}
}

func TestRotatingHandsTheNodeOver(t *testing.T) {
	current := newAuthority(t)
	address, store := claimedFor(t, current)
	next := newAuthority(t)

	status, body := postRaw(t, client(t, store, current.issue(t, RoleAdmin)),
		"https://"+address+"/v1/ca/rotate",
		rotateBody(t, next, pemOf(t, next.issue(t, RoleAdmin))))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	pinned, err := store.OperatorCA()
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	if !bytes.Equal(pinned.Raw, next.certificate.Raw) {
		t.Error("the node did not take the new CA")
	}

	claim, err := store.Claim()
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	if claim.Method != ClaimedByRotation {
		t.Errorf("Method = %q, want %q", claim.Method, ClaimedByRotation)
	}
}

func TestRotatingDoesNotLaunderANodeThatWasTakenOpenly(t *testing.T) {
	// An owner who acquired a node by reaching it first has not made that
	// legitimate by handing it to a second CA, and an audit should still find
	// it.
	current := newAuthority(t)
	store := newTestStore(t)

	if err := store.Adopt(current.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if err := store.RecordClaim(ClaimedOpenly); err != nil {
		t.Fatalf("RecordClaim() error = %v", err)
	}

	if err := store.Rotate(newAuthority(t).pem); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	claim, err := store.Claim()
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	if claim.Method != ClaimedByRotation {
		t.Errorf("Method = %q, want %q", claim.Method, ClaimedByRotation)
	}

	if claim.Authenticated() {
		t.Error("rotating cleared the record that this node was taken openly")
	}
}

func TestRotatingAnUnclaimedNodeIsRefused(t *testing.T) {
	// It would be enrolment by another name, walking around the pairing code.
	store := newTestStore(t)

	if err := store.Rotate(newAuthority(t).pem); err == nil {
		t.Fatal("Rotate() on an unclaimed node = nil, want a refusal")
	}
}

func TestRotatingNeedsAdmin(t *testing.T) {
	current := newAuthority(t)
	address, store := claimedFor(t, current)
	next := newAuthority(t)

	status, _ := postRaw(t, client(t, store, current.issue(t, RoleOperator)),
		"https://"+address+"/v1/ca/rotate",
		rotateBody(t, next, pemOf(t, next.issue(t, RoleAdmin))))

	if status != http.StatusForbidden {
		t.Errorf("status = %d as operator, want 403", status)
	}
}

// pemOf renders a test certificate back to PEM, which is how it travels.
func pemOf(t *testing.T, certificate tls.Certificate) string {
	t.Helper()

	return string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: certificate.Certificate[0],
	}))
}
