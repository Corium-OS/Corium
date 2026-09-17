package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// operatorCA mints a CA certificate for a test, key discarded.
func operatorCA(t *testing.T, name string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	return NewStore(filepath.Join(t.TempDir(), "api"))
}

func TestStoreStartsUnenrolled(t *testing.T) {
	store := newTestStore(t)

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("Enrolled() = true on a fresh node, want false")
	}

	if _, err := store.OperatorCA(); !errors.Is(err, ErrUnenrolled) {
		t.Errorf("OperatorCA() error = %v, want %v", err, ErrUnenrolled)
	}
}

func TestAdoptClaimsTheNodeOnce(t *testing.T) {
	store := newTestStore(t)

	if err := store.Adopt(operatorCA(t, "first operator")); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	certificate, err := store.OperatorCA()
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	if got := certificate.Subject.CommonName; got != "first operator" {
		t.Errorf("pinned CA = %q, want %q", got, "first operator")
	}

	// The property the whole design rests on: a second claimant cannot
	// displace the first, whatever it presents.
	if err := store.Adopt(operatorCA(t, "second operator")); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("Adopt() on a claimed node = %v, want %v", err, ErrAlreadyEnrolled)
	}

	certificate, err = store.OperatorCA()
	if err != nil {
		t.Fatalf("OperatorCA() error = %v", err)
	}

	if got := certificate.Subject.CommonName; got != "first operator" {
		t.Errorf("pinned CA after a second Adopt = %q, want it unchanged", got)
	}
}

func TestAdoptRefusesWhatTheSchemaWouldRefuse(t *testing.T) {
	// The daemon must not accept over the wire what an operator could not have
	// written in cloud-init; both paths go through the same parser.
	store := newTestStore(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}

	for name, offered := range map[string][]byte{
		"not PEM": []byte("hunter2"),
		"a chain": append(operatorCA(t, "a"), operatorCA(t, "b")...),
		"a private key": pem.EncodeToMemory(&pem.Block{
			Type: "EC PRIVATE KEY", Bytes: keyDER,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.Adopt(offered); err == nil {
				t.Fatal("Adopt() error = nil, want a refusal")
			}

			enrolled, err := store.Enrolled()
			if err != nil {
				t.Fatalf("Enrolled() error = %v", err)
			}

			if enrolled {
				t.Error("a refused CA left the node enrolled")
			}
		})
	}
}

func TestOperatorCAIsReadableAndTheKeyIsNot(t *testing.T) {
	store := newTestStore(t)

	if err := store.Adopt(operatorCA(t, "operators")); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	if _, err := store.Identity(); err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	for name, want := range map[string]os.FileMode{
		operatorCAFile: 0o644,
		serverCertFile: 0o644,
		serverKeyFile:  0o600,
	} {
		info, err := os.Stat(filepath.Join(store.dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}

		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", name, got, want)
		}
	}
}

func TestIdentityIsMintedOnceAndReused(t *testing.T) {
	store := newTestStore(t)

	first, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	second, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	// A node that reminted its identity on every start would change
	// fingerprint under every client that had pinned it.
	if Fingerprint(first.Certificate[0]) != Fingerprint(second.Certificate[0]) {
		t.Error("Identity() minted a second certificate, want the stored one reused")
	}
}

func TestEnrollHappyPath(t *testing.T) {
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if !enroller.Open() {
		t.Fatal("Open() = false on a fresh enroller")
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	if enroller.Open() {
		t.Error("Open() = true after a successful enrolment, want false")
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "someone else")); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("a second Enroll() = %v, want %v", err, ErrAlreadyEnrolled)
	}
}

func TestEnrollAcceptsWhatSomebodyActuallyTypes(t *testing.T) {
	// The code is read off a screen and retyped. Case and the grouping dash
	// are presentation, and rejecting them would only make people paste more
	// carefully, not make the node safer.
	for _, transform := range []func(string) string{
		func(s string) string { return s },
		strings.ToLower,
		func(s string) string { return strings.ReplaceAll(s, "-", "") },
		func(s string) string { return "  " + strings.ToLower(s) + "\n" },
	} {
		store := newTestStore(t)

		enroller, err := NewEnroller(store, RequirePairingCode)
		if err != nil {
			t.Fatalf("NewEnroller() error = %v", err)
		}

		if err := enroller.Enroll(transform(enroller.Code()), operatorCA(t, "operators")); err != nil {
			t.Errorf("Enroll(%q) error = %v", transform(enroller.Code()), err)
		}
	}
}

func TestEnrollLocksOutAfterFiveWrongCodes(t *testing.T) {
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	ca := operatorCA(t, "operators")

	for attempt := 1; attempt < maxAttempts; attempt++ {
		if err := enroller.Enroll("00000000", ca); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("attempt %d = %v, want %v", attempt, err, ErrWrongCode)
		}
	}

	if err := enroller.Enroll("00000000", ca); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("attempt %d = %v, want %v", maxAttempts, err, ErrLockedOut)
	}

	// And the correct code no longer helps, or the limit would be decoration.
	if err := enroller.Enroll(enroller.Code(), ca); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("correct code after lockout = %v, want %v", err, ErrLockedOut)
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		t.Fatalf("Enrolled() error = %v", err)
	}

	if enrolled {
		t.Error("a locked-out node ended up enrolled")
	}
}

func TestABadCertificateDoesNotCostAnAttempt(t *testing.T) {
	// The caller proved they are at the console; a typo in a file path should
	// not spend the attempts they will need to correct it.
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	for range maxAttempts + 3 {
		if err := enroller.Enroll(enroller.Code(), []byte("not a certificate")); err == nil {
			t.Fatal("Enroll() with a bad CA = nil, want a refusal")
		}
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
		t.Fatalf("Enroll() after bad certificates = %v, want it to still work", err)
	}
}

func TestEnrolmentIsStickyAcrossRestarts(t *testing.T) {
	// A claimed node must not offer itself again when it reboots, or
	// power-cycling a machine would be enough to take it.
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	// The next boot, against the same /var.
	if _, err := NewEnroller(NewStore(store.dir), RequirePairingCode); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("NewEnroller() on a claimed node = %v, want %v", err, ErrAlreadyEnrolled)
	}
}

func TestConcurrentGuessesShareTheAttemptLimit(t *testing.T) {
	// The limit is only worth anything if it holds when somebody is trying
	// codes in parallel rather than one at a time.
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	ca := operatorCA(t, "operators")

	var (
		group   sync.WaitGroup
		mu      sync.Mutex
		refused int
	)

	for range 50 {
		group.Add(1)

		go func() {
			defer group.Done()

			if err := enroller.Enroll("00000000", ca); err != nil {
				mu.Lock()
				refused++
				mu.Unlock()
			}
		}()
	}

	group.Wait()

	if refused != 50 {
		t.Errorf("refused %d of 50 wrong codes, want all of them", refused)
	}

	if enroller.Open() {
		t.Error("Open() = true after 50 wrong codes, want false")
	}
}

func TestPairingCodeShape(t *testing.T) {
	seen := make(map[string]bool)

	for range 500 {
		code, err := newCode()
		if err != nil {
			t.Fatalf("newCode() error = %v", err)
		}

		if len(code) != codeLength {
			t.Fatalf("newCode() = %q, want %d symbols", code, codeLength)
		}

		// I, L, O and U are excluded because a console at the wrong angle
		// turns them into 1, 1 and 0 -- and U into the occasional word.
		if strings.ContainsAny(code, "ILOU") {
			t.Errorf("newCode() = %q, which contains a symbol people misread", code)
		}

		seen[code] = true
	}

	// Not a randomness test, just a guard against the generator collapsing to
	// a constant, which is the failure that would otherwise pass every test in
	// this file.
	if len(seen) < 450 {
		t.Errorf("500 codes produced only %d distinct values", len(seen))
	}
}

func TestBannerCarriesBothHalvesOfTheTrust(t *testing.T) {
	store := newTestStore(t)

	enroller, err := NewEnroller(store, RequirePairingCode)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	identity, err := store.Identity()
	if err != nil {
		t.Fatalf("Identity() error = %v", err)
	}

	fingerprint := Fingerprint(identity.Certificate[0])
	banner := enroller.Banner("192.168.1.51:7443", fingerprint)

	// The code authenticates the operator to the node; the fingerprint
	// authenticates the node to the operator. A banner missing either leaves
	// one direction unverifiable.
	for _, want := range []string{enroller.Code(), fingerprint, "192.168.1.51:7443", "cctl enroll"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner does not mention %q:\n%s", want, banner)
		}
	}
}

func TestOpenEnrolmentAsksForNothing(t *testing.T) {
	store := newTestStore(t)

	enroller, err := NewEnroller(store, OpenToAnyone)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if !enroller.OpenToAnyone() {
		t.Fatal("OpenToAnyone() = false")
	}

	// No code is printed, because there is none to print.
	if enroller.Code() != "" {
		t.Errorf("Code() = %q, want empty on an open node", enroller.Code())
	}

	// And anything is accepted, including nothing.
	if err := enroller.Enroll("", operatorCA(t, "whoever got here first")); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
}

func TestOpenEnrolmentIsStillOnlyOnce(t *testing.T) {
	// Asking for nothing does not make the door stay open. The first claimant
	// still wins for good, which is the whole risk being accepted.
	store := newTestStore(t)

	enroller, err := NewEnroller(store, OpenToAnyone)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	if err := enroller.Enroll("", operatorCA(t, "first")); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	if err := enroller.Enroll("", operatorCA(t, "second")); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("a second Enroll() = %v, want %v", err, ErrAlreadyEnrolled)
	}

	// And a reboot does not reopen it.
	if _, err := NewEnroller(NewStore(store.dir), OpenToAnyone); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("NewEnroller() after a claim = %v, want %v", err, ErrAlreadyEnrolled)
	}
}

func TestTheNodeRecordsHowItWasClaimed(t *testing.T) {
	// The three are not equally trustworthy, and afterwards the node holds the
	// same pinned CA either way -- so without this there is no way to tell.
	for _, tc := range []struct {
		how    Enrolment
		method ClaimMethod
		honest bool
	}{
		{RequirePairingCode, ClaimedWithPairingCode, true},
		{OpenToAnyone, ClaimedOpenly, false},
	} {
		store := newTestStore(t)

		enroller, err := NewEnroller(store, tc.how)
		if err != nil {
			t.Fatalf("NewEnroller() error = %v", err)
		}

		if err := enroller.Enroll(enroller.Code(), operatorCA(t, "operators")); err != nil {
			t.Fatalf("Enroll() error = %v", err)
		}

		claim, err := store.Claim()
		if err != nil {
			t.Fatalf("Claim() error = %v", err)
		}

		if claim.Method != tc.method {
			t.Errorf("Method = %q, want %q", claim.Method, tc.method)
		}

		if claim.Authenticated() != tc.honest {
			t.Errorf("Authenticated() = %v, want %v", claim.Authenticated(), tc.honest)
		}
	}
}

func TestBannerSaysWhenTheNodeIsOpen(t *testing.T) {
	store := newTestStore(t)

	enroller, err := NewEnroller(store, OpenToAnyone)
	if err != nil {
		t.Fatalf("NewEnroller() error = %v", err)
	}

	banner := enroller.Banner("192.168.1.51:7443", "SHA256:abc")

	// Somebody may be reading this console without having written the
	// configuration that opened the node.
	if !strings.Contains(banner, "api.insecure") ||
		!strings.Contains(banner, "claims this node for good") {
		t.Errorf("banner does not say the node is open:\n%s", banner)
	}

	if strings.Contains(banner, "--code") {
		t.Errorf("banner offers a code the node does not want:\n%s", banner)
	}
}

func TestTheBannerPrintsAnAddressSomebodyCanType(t *testing.T) {
	// A wildcard listener reports itself as [::]:7443, which is true and
	// useless: `cctl enroll [::]:7443` is not a command anybody can run, and
	// the console is the one place where what is printed has to be typed back.
	for _, listening := range []string{"[::]:7443", "0.0.0.0:7443", ":7443"} {
		got := reachableAddress(listening)

		if got == listening {
			t.Errorf("reachableAddress(%q) = %q, want a routable host", listening, got)
		}

		if _, port, err := net.SplitHostPort(got); err != nil || port != "7443" {
			t.Errorf("reachableAddress(%q) = %q, want the port kept", listening, got)
		}
	}

	// An address that was already specific is left alone.
	for _, listening := range []string{"192.168.1.51:7443", "127.0.0.1:9000"} {
		if got := reachableAddress(listening); got != listening {
			t.Errorf("reachableAddress(%q) = %q, want it unchanged", listening, got)
		}
	}
}
