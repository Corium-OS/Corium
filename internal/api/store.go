package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

// StateDir is where a node keeps what it knows about its own management.
//
// It sits inside lifecycle.StateDir rather than beside it, so that erasing a
// node's state erases this too and corium-apid needs one directory rather than
// two that have to be kept in step.
//
// It sits under /var because that is the only part of the filesystem that
// survives an OS upgrade, and because enrolment surviving a reboot is a
// security property rather than a convenience: if it did not, power-cycling a
// machine would be enough to take it.
const StateDir = "/var/lib/corium/api"

const (
	operatorCAFile = "operator-ca.pem"
	serverKeyFile  = "server.key"
	serverCertFile = "server.crt"
	claimFile      = "claim.json"
)

// ClaimMethod records how a node's ownership was established.
//
// It is kept because the three are not equally trustworthy, and afterwards
// there is no other way to tell them apart: a node holds the same pinned CA
// either way. Somebody auditing a fleet is entitled to know which of its nodes
// were claimed by whoever reached them first.
type ClaimMethod string

const (
	// ClaimedFromConfiguration is modes A and B: the CA was named in the
	// node's own configuration, so it was never unclaimed.
	ClaimedFromConfiguration ClaimMethod = "configuration"

	// ClaimedWithPairingCode is maintenance mode as it is meant to be used:
	// somebody read a code off the console.
	ClaimedWithPairingCode ClaimMethod = "pairing-code"

	// ClaimedOpenly is api.insecure: nothing was asked and nothing was proved.
	ClaimedOpenly ClaimMethod = "open"
)

// Claim is what the node remembers about being claimed.
type Claim struct {
	Method ClaimMethod `json:"method"`
	At     time.Time   `json:"at"`
}

// Authenticated reports whether the claimant proved anything.
func (c Claim) Authenticated() bool { return c.Method != ClaimedOpenly }

// ErrUnenrolled reports that no operator has claimed this node.
var ErrUnenrolled = errors.New("node is not enrolled")

// ErrAlreadyEnrolled reports that one already has.
//
// Enrolment is not repeatable over the network by design. Getting back to an
// unclaimed state means resetting the node, which takes it out of its cluster
// on the way; replacing the CA on a node that is running is done over the
// authenticated API, or locally by root.
var ErrAlreadyEnrolled = errors.New("node is already enrolled")

// Store is the node's management state on disk.
type Store struct {
	dir string
}

// NewStore opens the store rooted at dir. It creates nothing until asked to.
func NewStore(dir string) *Store {
	if dir == "" {
		dir = StateDir
	}

	return &Store{dir: dir}
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Enrolled reports whether an operator CA has been pinned.
//
// The presence of the certificate is the whole of the answer. A separate
// marker file would be a second source of truth that could disagree with the
// first, and the disagreement would be resolved at the worst moment.
func (s *Store) Enrolled() (bool, error) {
	switch _, err := os.Stat(s.path(operatorCAFile)); {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("checking enrolment: %w", err)
	}
}

// OperatorCA returns the pinned CA, or ErrUnenrolled.
func (s *Store) OperatorCA() (*x509.Certificate, error) {
	data, err := os.ReadFile(s.path(operatorCAFile))

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrUnenrolled
	case err != nil:
		return nil, fmt.Errorf("reading operator CA: %w", err)
	}

	certificate, err := config.ParseOperatorCA(data)
	if err != nil {
		// Written by this package after the same check, so reaching here means
		// the file was edited or corrupted. Refusing is right: the alternative
		// is serving with an anchor nobody vetted.
		return nil, fmt.Errorf("stored operator CA is unusable: %w", err)
	}

	return certificate, nil
}

// ClientCAs returns the pool a TLS listener verifies client certificates
// against.
func (s *Store) ClientCAs() (*x509.CertPool, error) {
	certificate, err := s.OperatorCA()
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	pool.AddCert(certificate)

	return pool, nil
}

// Adopt pins an operator CA, claiming the node.
//
// It refuses to overwrite one. Enrolment being one-way is what stops a second
// claimant from displacing the first, and the check lives here -- at the write
// -- rather than only in the caller that happens to be in front of it today.
func (s *Store) Adopt(pemData []byte) error {
	if _, err := config.ParseOperatorCA(pemData); err != nil {
		return fmt.Errorf("refusing operator CA: %w", err)
	}

	enrolled, err := s.Enrolled()
	if err != nil {
		return err
	}

	if enrolled {
		return ErrAlreadyEnrolled
	}

	// 0644: this is a certificate. Making it unreadable would suggest it is a
	// secret, and the fact that it is not is the reason the whole scheme can
	// put it in cloud-init in clear.
	return s.write(operatorCAFile, pemData, 0o644)
}

// RecordClaim notes how the node came to be owned.
//
// It is written after the CA, not before: a claim record without a pinned CA
// would describe something that did not happen.
func (s *Store) RecordClaim(method ClaimMethod) error {
	encoded, err := json.Marshal(Claim{Method: method, At: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("encoding the claim record: %w", err)
	}

	return s.write(claimFile, encoded, 0o644)
}

// Claim returns how the node was claimed, if it knows.
//
// A node claimed by an earlier version has no record, which reads as an
// unknown method rather than as an error: it is a missing note about the past,
// not a broken node.
func (s *Store) Claim() (Claim, error) {
	data, err := os.ReadFile(s.path(claimFile))

	switch {
	case errors.Is(err, os.ErrNotExist):
		return Claim{}, nil
	case err != nil:
		return Claim{}, fmt.Errorf("reading the claim record: %w", err)
	}

	var claim Claim
	if err := json.Unmarshal(data, &claim); err != nil {
		return Claim{}, fmt.Errorf("parsing the claim record: %w", err)
	}

	return claim, nil
}

// Forget erases everything that makes this node somebody's.
//
// The serving identity goes too, not just the CA. A machine handed on with the
// certificate its previous owner pinned is a machine that owner's tooling will
// still accept without a word -- and the whole value of the fingerprint is
// that it means one machine.
//
// It is called last in a reset, after the node has already left its cluster,
// so that there is no moment at which the machine is both a member and
// unclaimed.
func (s *Store) Forget() error {
	for _, name := range []string{operatorCAFile, claimFile, serverCertFile, serverKeyFile} {
		if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", name, err)
		}
	}

	return nil
}

// Identity returns the node's serving certificate, minting one on first use.
//
// The key never leaves the node and nothing signs it: the node holds the
// operator CA's certificate and not its key, so there is no online authority
// to ask. Clients pin the fingerprint instead, which they read from the
// console beside the pairing code.
func (s *Store) Identity() (tls.Certificate, error) {
	keyPEM, keyErr := os.ReadFile(s.path(serverKeyFile))
	certPEM, certErr := os.ReadFile(s.path(serverCertFile))

	if keyErr == nil && certErr == nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("loading serving identity: %w", err)
		}

		return pair, nil
	}

	for _, err := range []error{keyErr, certErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return tls.Certificate{}, fmt.Errorf("reading serving identity: %w", err)
		}
	}

	return s.mintIdentity()
}

// servingLifetime is ten years.
//
// Nothing validates this certificate by date -- it is pinned by fingerprint --
// so a short lifetime would buy no security and would eventually strand a node
// nobody had touched. Ten years also survives the case that argues for it: a
// machine whose real-time clock is wrong on first boot, which is ordinary on
// hardware that has been in a cupboard.
const servingLifetime = 10 * 365 * 24 * time.Hour

func (s *Store) mintIdentity() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating serving key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating serial: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "corium-node"
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		// Backdated for the same reason the lifetime is long: a first boot
		// before NTP has corrected the clock must not produce a certificate
		// that is not valid yet.
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(servingLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname},
		IPAddresses:           localAddresses(),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating serving certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshalling serving key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	// The key first, and 0600. If the process dies between the two writes the
	// next start finds a key without a certificate and mints both again, which
	// is why Identity treats a half-written pair as absent rather than fatal.
	if err := s.write(serverKeyFile, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}

	if err := s.write(serverCertFile, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// localAddresses lists the node's own addresses, for the serving certificate's
// SANs. Loopback is included so that a local client works; failures are not
// fatal, because a certificate pinned by fingerprint is usable with no SANs at
// all.
func localAddresses() []net.IP {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}

	var found []net.IP

	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok {
			found = append(found, network.IP)
		}
	}

	return found
}

// Fingerprint is how a certificate is named to a human: the SHA-256 of its DER
// encoding, in the SSH style people already know from host keys.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)

	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// write replaces a file atomically, so that a reader never sees a half-written
// certificate and a crash never leaves one behind.
func (s *Store) write(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", s.dir, err)
	}

	temporary, err := os.CreateTemp(s.dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", s.dir, err)
	}

	defer func() {
		// Removing a file that was renamed away fails, and that failure is the
		// success path. Anything else has already been reported.
		_ = os.Remove(temporary.Name())
	}()

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("setting mode on %s: %w", name, err)
	}

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("writing %s: %w", name, err)
	}

	// Durability before visibility: a node that is told it is enrolled and
	// forgets after a power cut is worse than one that was never told.
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("syncing %s: %w", name, err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}

	if err := os.Rename(temporary.Name(), s.path(name)); err != nil {
		return fmt.Errorf("installing %s: %w", name, err)
	}

	return nil
}
