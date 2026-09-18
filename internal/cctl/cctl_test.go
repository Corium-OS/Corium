package cctl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/config"
)

func newStore(t *testing.T) *Store {
	t.Helper()

	return NewStore(t.TempDir())
}

func initialised(t *testing.T) *Store {
	t.Helper()

	store := newStore(t)
	if err := InitCA(store, "test operators"); err != nil {
		t.Fatalf("InitCA() error = %v", err)
	}

	return store
}

func TestInitCAProducesSomethingANodeWouldAccept(t *testing.T) {
	// The check that matters: a CA this tool signs with must be one a node
	// would pin, so both go through the same parser.
	store := initialised(t)

	certPEM, err := OperatorCA(store)
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	certificate, err := config.ParseOperatorCA(certPEM)
	if err != nil {
		t.Fatalf("a node would reject the CA cctl just made: %v", err)
	}

	if certificate.Subject.CommonName != "test operators" {
		t.Errorf("CommonName = %q, want %q", certificate.Subject.CommonName, "test operators")
	}
}

func TestInitCARefusesToDestroyOne(t *testing.T) {
	// Overwriting a CA does not lose a file, it loses every node that pinned
	// it -- each one then needing a visit to its console.
	store := initialised(t)

	before, err := os.ReadFile(store.Path(CACertFile))
	if err != nil {
		t.Fatalf("reading the CA: %v", err)
	}

	if err := InitCA(store, "a second attempt"); !errors.Is(err, ErrCAExists) {
		t.Fatalf("InitCA() twice = %v, want %v", err, ErrCAExists)
	}

	after, err := os.ReadFile(store.Path(CACertFile))
	if err != nil {
		t.Fatalf("reading the CA: %v", err)
	}

	if string(before) != string(after) {
		t.Error("the CA changed despite the refusal")
	}
}

func TestTheKeyIsPrivateAndTheCertificateIsNot(t *testing.T) {
	store := initialised(t)

	if err := Issue(store, "someone", api.RoleAdmin, DefaultClientLifetime); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	for name, want := range map[string]os.FileMode{
		CACertFile:     0o644,
		CAKeyFile:      0o600,
		ClientCertFile: 0o644,
		ClientKeyFile:  0o600,
	} {
		info, err := os.Stat(store.Path(name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}

		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", name, got, want)
		}
	}

	// The directory holds the key that owns a fleet, whatever the modes inside.
	info, err := os.Stat(store.Dir())
	if err != nil {
		t.Fatalf("stat %s: %v", store.Dir(), err)
	}

	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", got)
	}
}

func TestIssueWritesTheRoleWhereANodeReadsIt(t *testing.T) {
	for _, role := range []api.Role{api.RoleReadOnly, api.RoleOperator, api.RoleAdmin} {
		store := initialised(t)

		if err := Issue(store, "someone", role, DefaultClientLifetime); err != nil {
			t.Fatalf("Issue() error = %v", err)
		}

		certificate := readCertificate(t, store.Path(ClientCertFile))

		// The node takes the role from the organisation, so this is not a
		// cosmetic field: it is the authorisation.
		if got := certificate.Subject.Organization; len(got) != 1 || got[0] != string(role) {
			t.Errorf("Organization = %v, want [%s]", got, role)
		}

		if !certificate.BasicConstraintsValid || certificate.IsCA {
			t.Error("a client certificate must not be a CA")
		}

		var clientAuth bool

		for _, usage := range certificate.ExtKeyUsage {
			if usage == x509.ExtKeyUsageClientAuth {
				clientAuth = true
			}
		}

		if !clientAuth {
			t.Error("the certificate does not carry clientAuth")
		}
	}
}

func TestIssueRefusesToOutliveItsCA(t *testing.T) {
	// A certificate that outlives its issuer stops working for a reason nobody
	// thinks to look for.
	store := initialised(t)

	err := Issue(store, "someone", api.RoleAdmin, CALifetime+24*time.Hour)
	if err == nil {
		t.Fatal("Issue() error = nil, want a refusal")
	}
}

func TestIssueNeedsACA(t *testing.T) {
	err := Issue(newStore(t), "someone", api.RoleAdmin, DefaultClientLifetime)
	if err == nil {
		t.Fatal("Issue() without a CA = nil, want an error pointing at pki init")
	}
}

func TestParseRole(t *testing.T) {
	for typed, want := range map[string]api.Role{
		"readonly":               api.RoleReadOnly,
		"operator":               api.RoleOperator,
		"admin":                  api.RoleAdmin,
		string(api.RoleAdmin):    api.RoleAdmin,
		string(api.RoleReadOnly): api.RoleReadOnly,
	} {
		got, err := ParseRole(typed)
		if err != nil {
			t.Errorf("ParseRole(%q) error = %v", typed, err)
		}

		if got != want {
			t.Errorf("ParseRole(%q) = %q, want %q", typed, got, want)
		}
	}

	if _, err := ParseRole("root"); err == nil {
		t.Error("ParseRole(\"root\") = nil, want an error")
	}
}

func TestStoreRemembersFingerprints(t *testing.T) {
	store := newStore(t)

	// A first run has no configuration, which is not an error.
	fingerprint, err := store.Fingerprint("192.168.1.51:7443")
	if err != nil {
		t.Fatalf("Fingerprint() on a fresh store = %v", err)
	}

	if fingerprint != "" {
		t.Errorf("Fingerprint() = %q, want empty", fingerprint)
	}

	if err := store.Remember("192.168.1.51:7443", "SHA256:abc"); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}

	if err := store.Remember("192.168.1.52:7443", "SHA256:def"); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}

	// Reopened, as a second invocation of the command would.
	reopened := NewStore(store.Dir())

	for address, want := range map[string]string{
		"192.168.1.51:7443": "SHA256:abc",
		"192.168.1.52:7443": "SHA256:def",
	} {
		got, err := reopened.Fingerprint(address)
		if err != nil {
			t.Fatalf("Fingerprint(%s) error = %v", address, err)
		}

		if got != want {
			t.Errorf("Fingerprint(%s) = %q, want %q", address, got, want)
		}
	}
}

func TestClientCertificateIsAbsentRatherThanBroken(t *testing.T) {
	// Enrolment happens before any client certificate exists, so its absence
	// must not read as a failure.
	certificates, err := ClientCertificate(newStore(t))
	if err != nil {
		t.Fatalf("ClientCertificate() error = %v, want nil", err)
	}

	if certificates != nil {
		t.Error("ClientCertificate() returned something on an empty store")
	}
}

func TestEnrolAgainstARealNode(t *testing.T) {
	// The whole round trip, against the actual server: claim an unenrolled
	// node, then talk to it with the certificate the CA just signed.
	operator := initialised(t)

	if err := Issue(operator, "someone", api.RoleOperator, DefaultClientLifetime); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	node, address, code := startNode(t)

	operatorCA, err := OperatorCA(operator)
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// First contact: no fingerprint known, which is what reading the console
	// is for. The client records what it saw.
	probe := Dial(address, "")
	if _, err := probe.Enrol(ctx, code, operatorCA, nil); err != nil {
		t.Fatalf("Enrol() error = %v", err)
	}

	fingerprint := probe.Fingerprint()
	if fingerprint == "" {
		t.Fatal("the client did not record the node's fingerprint")
	}

	if err := operator.Remember(address, fingerprint); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}

	// The node restarts into its authenticated shape.
	node.restart(t)

	certificates, err := ClientCertificate(operator)
	if err != nil {
		t.Fatalf("ClientCertificate() error = %v", err)
	}

	health, err := Dial(address, fingerprint, certificates...).Health(ctx)
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}

	if health.Status != "ok" {
		t.Errorf("status = %q, want ok", health.Status)
	}

	// The node read the role out of the certificate cctl signed.
	if health.Role != api.RoleOperator {
		t.Errorf("role = %q, want %q", health.Role, api.RoleOperator)
	}
}

func TestPinningRefusesAnImpostor(t *testing.T) {
	operator := initialised(t)

	if err := Issue(operator, "someone", api.RoleAdmin, DefaultClientLifetime); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	_, address, code := startNode(t)

	operatorCA, err := OperatorCA(operator)
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	// A fingerprint from the console that does not match what answered: either
	// the wrong machine, or something speaking for it. Nothing is sent.
	wrong := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	_, err = Dial(address, wrong).Enrol(t.Context(), code, operatorCA, nil)
	if err == nil {
		t.Fatal("Enrol() against a mismatched fingerprint = nil, want a refusal")
	}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("%s is not PEM", path)
	}

	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	return certificate
}

func TestACertificateFromTheWrongCAIsDiagnosed(t *testing.T) {
	// Go sends no certificate at all when the one it holds was signed by a CA
	// the server did not name as acceptable, so the node answers "certificate
	// required" -- which reads like the client sent nothing, and sends people
	// looking in the wrong place. The cause is almost always a rotated CA.
	trusted := initialised(t)

	stranger := NewStore(t.TempDir())
	if err := InitCA(stranger, "somebody else"); err != nil {
		t.Fatalf("InitCA() error = %v", err)
	}

	if err := Issue(stranger, "them", api.RoleAdmin, DefaultClientLifetime); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	// A node that accepts only the trusted CA.
	trustedPEM, err := OperatorCA(trusted)
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	certificate, err := config.ParseOperatorCA(trustedPEM)
	if err != nil {
		t.Fatalf("ParseOperatorCA() error = %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(certificate)

	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	server.TLS = &tls.Config{
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()

	t.Cleanup(server.Close)

	theirs, err := ClientCertificate(stranger)
	if err != nil {
		t.Fatalf("ClientCertificate() error = %v", err)
	}

	address := strings.TrimPrefix(server.URL, "https://")

	_, err = Dial(address, api.Fingerprint(server.Certificate().Raw), theirs...).Node(t.Context())
	if err == nil {
		t.Fatal("Node() = nil, want the handshake to fail")
	}

	for _, want := range []string{"does not accept your certificate", "rotated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
