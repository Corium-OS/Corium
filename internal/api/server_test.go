package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
)

// authority is an operator CA a test can actually sign client certificates
// with, unlike the throwaway one used where only the certificate matters.
type authority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newAuthority(t *testing.T) *authority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "operators"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}

	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing CA: %v", err)
	}

	return &authority{
		certificate: certificate,
		key:         key,
		pem:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue signs a client certificate carrying a role.
func (a *authority) issue(t *testing.T, role Role) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating client key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName:   "an operator",
			Organization: []string{string(role)},
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		t.Fatalf("signing client certificate: %v", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// start runs a server on a kernel-assigned port and returns its address.
func start(t *testing.T, store *Store) (*Server, string) {
	t.Helper()

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	return server, serveOn(t, server)
}

// serveOn runs a server that the caller has already configured.
func serveOn(t *testing.T, server *Server) string {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- server.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve() = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve() did not stop")
		}
	})

	select {
	case <-server.Ready():
	case err := <-done:
		t.Fatalf("Serve() returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve() never became ready")
	}

	return server.Addr()
}

// client talks to the node the way cctl does: pinning the node's certificate
// by fingerprint rather than trusting a name, because the node self-signs.
func client(t *testing.T, store *Store, clientCertificates ...tls.Certificate) *http.Client {
	t.Helper()

	identity, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	pinned := Fingerprint(identity.Certificate[0])

	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: clientCertificates,
				MinVersion:   tls.VersionTLS13,
				// The node's certificate is self-signed on purpose: no private
				// key is ever carried in a configuration, so there is nothing
				// to sign it with. The fingerprint is the check.
				InsecureSkipVerify: true, //nolint:gosec // pinned below
				VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
					if len(raw) == 0 || Fingerprint(raw[0]) != pinned {
						return fmt.Errorf("node certificate is not the pinned one")
					}

					return nil
				},
			},
		},
	}
}

func post(t *testing.T, c *http.Client, address, path string, body any) (int, map[string]any) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding request: %v", err)
	}

	response, err := c.Post("https://"+address+path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}

	defer func() { _ = response.Body.Close() }()

	return response.StatusCode, decode(t, response.Body)
}

func decode(t *testing.T, body io.Reader) map[string]any {
	t.Helper()

	var decoded map[string]any
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	return decoded
}

func TestEnrolOverTheWire(t *testing.T) {
	store := newTestStore(t)
	server, address := start(t, store)
	ca := newAuthority(t)

	if !server.Unenrolled() {
		t.Fatal("Unenrolled() = false on a fresh node")
	}

	status, body := post(t, client(t, store), address, "/v1/enroll", enrolRequest{
		Code:       server.enroller.Code(),
		OperatorCA: string(ca.pem),
	})

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	if body["enrolled"] != true {
		t.Errorf("response = %v, want enrolled true", body)
	}

	// The node tells the process to come back up in its other shape rather
	// than rebuilding TLS under a live listener.
	select {
	case <-server.Claimed():
	case <-time.After(10 * time.Second):
		t.Fatal("a successful enrolment did not close Claimed()")
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if !enrolled {
		t.Error("the node is not enrolled after a 200")
	}
}

func TestEnrolRejectsAWrongCodeOverTheWire(t *testing.T) {
	store := newTestStore(t)
	_, address := start(t, store)
	ca := newAuthority(t)

	status, body := post(t, client(t, store), address, "/v1/enroll", enrolRequest{
		Code:       "00000000",
		OperatorCA: string(ca.pem),
	})

	// 401 rather than 403: the credential was wrong, and the caller may try
	// again with the attempts they have left.
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%v)", status, body)
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("a rejected enrolment claimed the node anyway")
	}
}

func TestUnenrolledNodeServesNothingElse(t *testing.T) {
	store := newTestStore(t)
	_, address := start(t, store)

	response, err := client(t, store).Get("https://" + address + "/v1/health")
	if err != nil {
		t.Fatalf("GET /v1/health: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 on an unclaimed node", response.StatusCode)
	}
}

func TestEnrolledNodeRequiresAClientCertificate(t *testing.T) {
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	server, address := start(t, store)

	if server.Unenrolled() {
		t.Fatal("Unenrolled() = true on a claimed node")
	}

	// No client certificate: the handshake itself must fail, not the handler.
	refused(t, client(t, store), address,
		"an unauthenticated request succeeded, want the handshake refused")

	// A certificate from an authority the node never pinned.
	stranger := newAuthority(t)
	refused(t, client(t, store, stranger.issue(t, RoleAdmin)), address,
		"a certificate signed by another CA was accepted")
}

// refused asserts that a request never gets as far as a handler.
func refused(t *testing.T, c *http.Client, address, complaint string) {
	t.Helper()

	response, err := c.Get("https://" + address + "/v1/health")
	if err != nil {
		return
	}

	defer func() { _ = response.Body.Close() }()

	t.Errorf("%s (status %d)", complaint, response.StatusCode)
}

func TestEnrolledNodeReportsTheRoleItAuthenticated(t *testing.T) {
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	_, address := start(t, store)

	for _, role := range []Role{RoleReadOnly, RoleOperator, RoleAdmin} {
		response, err := client(t, store, ca.issue(t, role)).
			Get("https://" + address + "/v1/health")
		if err != nil {
			t.Fatalf("GET /v1/health as %s: %v", role, err)
		}

		body := decode(t, response.Body)
		_ = response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", response.StatusCode)
		}

		if body["role"] != string(role) {
			t.Errorf("role = %v, want %q", body["role"], role)
		}
	}
}

func TestEnrolledNodeRefusesToBeClaimedAgain(t *testing.T) {
	// The door does not reopen for somebody holding a valid certificate any
	// more than it does for a stranger.
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	_, address := start(t, store)

	usurper := newAuthority(t)

	status, _ := post(t, client(t, store, ca.issue(t, RoleAdmin)), address, "/v1/enroll",
		enrolRequest{Code: "whatever", OperatorCA: string(usurper.pem)})

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}

	pinned, err := store.OperatorCA()
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	if !bytes.Equal(pinned.Raw, ca.certificate.Raw) {
		t.Error("the pinned CA changed")
	}
}

func TestRoleFromCertificatePrefersTheStrongest(t *testing.T) {
	// A certificate listing several roles must not be downgraded by the order
	// somebody happened to write them in.
	certificate := &x509.Certificate{Subject: pkix.Name{Organization: []string{
		string(RoleReadOnly), string(RoleAdmin),
	}}}

	if got := roleFromCertificate(certificate); got != RoleAdmin {
		t.Errorf("roleFromCertificate() = %q, want %q", got, RoleAdmin)
	}

	none := &x509.Certificate{Subject: pkix.Name{Organization: []string{"some other org"}}}
	if got := roleFromCertificate(none); got != "" {
		t.Errorf("roleFromCertificate() = %q, want no role", got)
	}
}

func TestRolesReachOnlyWhatTheyShould(t *testing.T) {
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	_, address := start(t, store)

	// Read-only is the floor for both routes served today, so every issued
	// role reaches them. The case that matters is the one below it.
	for _, role := range []Role{RoleReadOnly, RoleOperator, RoleAdmin} {
		response, err := client(t, store, ca.issue(t, role)).Get("https://" + address + "/v1/node")
		if err != nil {
			t.Fatalf("GET /v1/node as %s: %v", role, err)
		}

		_ = response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Errorf("status = %d as %s, want 200", response.StatusCode, role)
		}
	}
}

func TestACertificateWithNoRoleIsAuthenticatedButNotAuthorised(t *testing.T) {
	// Issuing a certificate without naming a role is not a way to grant every
	// role. The handshake succeeds, and the request still does not.
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	_, address := start(t, store)

	response, err := client(t, store, ca.issue(t, "some other org")).
		Get("https://" + address + "/v1/node")
	if err != nil {
		t.Fatalf("GET /v1/node: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	// 403 rather than 401: the client is authenticated, and retrying with the
	// same certificate will never work.
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}

	body := decode(t, response.Body)
	if message, _ := body["error"].(string); !strings.Contains(message, "cctl pki issue") {
		t.Errorf("error = %q, want it to say how to fix the certificate", message)
	}
}

func TestAllows(t *testing.T) {
	tests := []struct {
		held, minimum Role
		want          bool
	}{
		{RoleAdmin, RoleReadOnly, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleOperator, RoleReadOnly, true},
		{RoleOperator, RoleAdmin, false},
		{RoleReadOnly, RoleOperator, false},
		{"", RoleReadOnly, false},
		{"corium:root", RoleReadOnly, false},
	}

	for _, tc := range tests {
		if got := allows(tc.held, tc.minimum); got != tc.want {
			t.Errorf("allows(%q, %q) = %v, want %v", tc.held, tc.minimum, got, tc.want)
		}
	}
}

func TestNodeReportsWhatTheInspectorFound(t *testing.T) {
	store := newTestStore(t)
	ca := newAuthority(t)

	if err := store.Adopt(ca.pem); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	server, err := NewServer(store, "127.0.0.1:0", RequirePairingCode)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	// A constructed machine, so the assertion is about the route rather than
	// about whichever host the tests happen to run on.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib/corium"), 0o755); err != nil {
		t.Fatalf("creating the fake state directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, nodeinfo.StateFile),
		[]byte(`{"role":"worker","cluster":"prod"}`), 0o600); err != nil {
		t.Fatalf("writing state: %v", err)
	}

	server.Inspect(&nodeinfo.Inspector{
		Root: root,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("not installed")
		},
	})

	address := serveOn(t, server)

	response, err := client(t, store, ca.issue(t, RoleReadOnly)).Get("https://" + address + "/v1/node")
	if err != nil {
		t.Fatalf("GET /v1/node: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	body := decode(t, response.Body)
	if body["role"] != "worker" || body["cluster"] != "prod" {
		t.Errorf("body = %v, want the constructed node's role and cluster", body)
	}
}
