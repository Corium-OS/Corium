package cctl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/config"
)

// CALifetime is ten years.
//
// Rotating an operator CA means reaching every node that pinned it, so a CA
// that expires is a fleet-wide errand nobody scheduled. Ten years is long
// enough that the rotation is planned rather than forced, and the thing that
// actually limits exposure is the client certificates below, which are short.
const CALifetime = 10 * 365 * 24 * time.Hour

// DefaultClientLifetime is ninety days.
//
// Client certificates are the credential that gets carried around on laptops,
// so they are the ones worth keeping short. Reissuing is one command and needs
// no node to be touched, which is the whole advantage of the CA staying put.
const DefaultClientLifetime = 90 * 24 * time.Hour

// ErrCAExists reports a directory that already holds an operator CA.
var ErrCAExists = errors.New("an operator CA already exists here")

// InitCA creates the operator CA: the certificate that goes into cloud-init,
// and the key that must not.
//
// It refuses to overwrite an existing CA rather than offering a --force. The
// consequence of replacing one is not a lost file: every node that pinned the
// old certificate becomes unmanageable until somebody visits its console. That
// is worth making awkward enough to be deliberate — move the directory aside
// if you really mean it.
func InitCA(store *Store, name string) error {
	if store.Exists(CACertFile) || store.Exists(CAKeyFile) {
		return fmt.Errorf("%w: %s. Every node that trusts it would have to be "+
			"re-enrolled from its console, so move the directory aside if you mean it",
			ErrCAExists, store.Dir())
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating the CA key: %w", err)
	}

	serial, err := newSerial()
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(CALifetime),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating the CA certificate: %w", err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}

	// The key first and 0600: if anything fails after this, the directory holds
	// a key with no certificate, which InitCA refuses to overwrite and which
	// therefore cannot be silently half-used.
	if err := store.Write(CAKeyFile, keyPEM, 0o600); err != nil {
		return err
	}

	return store.Write(CACertFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// Issue signs a client certificate carrying a role.
//
// The role is written into the certificate's organisation, which is where a
// node reads it from. It cannot be changed afterwards without the CA key, so a
// certificate is exactly as privileged as the person who signed it intended.
func Issue(store *Store, commonName string, role api.Role, lifetime time.Duration) error {
	return IssueTo(store, ClientCertFile, ClientKeyFile, commonName, role, lifetime)
}

// IssueTo signs one into named files.
//
// A rotation needs this: it mints credentials for the CA it is moving to while
// the ones for the current CA are still what reaches the node, so the two
// cannot share a filename.
func IssueTo(
	store *Store, certFile, keyFile, commonName string, role api.Role, lifetime time.Duration,
) error {
	caCert, caKey, err := loadCA(store)
	if err != nil {
		return err
	}

	if time.Now().Add(lifetime).After(caCert.NotAfter) {
		// Silently issuing a certificate that outlives its issuer produces one
		// that stops working for a reason nobody will look for.
		return fmt.Errorf("a %s certificate would outlive the CA, which expires on %s",
			lifetime, caCert.NotAfter.Format(time.RFC3339))
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating the client key: %w", err)
	}

	serial, err := newSerial()
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{string(role)},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("signing the client certificate: %w", err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}

	if err := store.Write(keyFile, keyPEM, 0o600); err != nil {
		return err
	}

	return store.Write(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// ParseRole turns what somebody typed into a role.
func ParseRole(name string) (api.Role, error) {
	switch name {
	case "readonly", string(api.RoleReadOnly):
		return api.RoleReadOnly, nil
	case "operator", string(api.RoleOperator):
		return api.RoleOperator, nil
	case "admin", string(api.RoleAdmin):
		return api.RoleAdmin, nil
	default:
		return "", fmt.Errorf("unknown role %q; use readonly, operator or admin", name)
	}
}

func loadCA(store *Store) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(store.Path(CACertFile))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the operator CA: %w "+
			"(run `cctl pki init` first)", err)
	}

	// The same check a node applies, so that a CA this tool would sign with is
	// one a node would accept.
	certificate, err := config.ParseOperatorCA(certPEM)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err := os.ReadFile(store.Path(CAKeyFile))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the operator CA key: %w", err)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("the operator CA key is not PEM data")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing the operator CA key: %w", err)
	}

	return certificate, key, nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshalling a key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func newSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating a serial number: %w", err)
	}

	return serial, nil
}
