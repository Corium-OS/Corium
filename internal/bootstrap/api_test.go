package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/config"
)

func enabled(v bool) *bool { return &v }

func operatorCA(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
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
		t.Fatalf("creating certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestGateLetsClaimedNodesThrough(t *testing.T) {
	tests := []struct {
		name string
		api  config.API
	}{
		{
			// The overwhelming majority of nodes, including every one
			// provisioned before the API was designed.
			name: "no api block",
			api:  config.API{},
		},
		{
			name: "an explicit refusal",
			api:  config.API{Enabled: enabled(false)},
		},
		{
			// A node whose owner is named in its own configuration was never
			// unclaimed, so there is nothing for it to wait on.
			name: "a configured CA",
			api:  config.API{OperatorCA: string(operatorCA(t))},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{StateDir: t.TempDir()}
			cfg := &config.Config{Role: config.RoleWorker, API: tc.api}

			if _, err := gateOnEnrolment(t.Context(), cfg, opts); err != nil {
				t.Errorf("gateOnEnrolment() = %v, want nil", err)
			}
		})
	}
}

func TestGateWaitsForAnUnclaimedNode(t *testing.T) {
	dir := t.TempDir()
	store := api.NewStore(dir)
	cfg := &config.Config{Role: config.RoleWorker, API: config.API{Enabled: enabled(true)}}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, gateErr := gateOnEnrolment(ctx, cfg, Options{StateDir: dir})
		done <- gateErr
	}()

	// It must still be waiting: an unclaimed node joins nothing, however long
	// nobody comes.
	select {
	case err := <-done:
		t.Fatalf("gateOnEnrolment() returned %v while unenrolled, want it to wait", err)
	case <-time.After(3 * enrolmentPoll):
	}

	// An operator turns up.
	if err := store.Adopt(operatorCA(t)); err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gateOnEnrolment() after enrolment = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("gateOnEnrolment() did not notice the enrolment")
	}
}

func TestGateGivesUpWhenTheNodeIsShuttingDown(t *testing.T) {
	// A wait with no deadline still has to end when systemd stops the unit,
	// or a node could not be shut down while it was waiting.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cfg := &config.Config{Role: config.RoleWorker, API: config.API{Enabled: enabled(true)}}

	_, err := gateOnEnrolment(ctx, cfg, Options{StateDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gateOnEnrolment() = %v, want %v", err, context.Canceled)
	}
}

func TestUnclaimedNodeStopsBeforeMutatingAnything(t *testing.T) {
	// The ordering is the point of the gate rather than a detail: a node that
	// is going to wait must not have renamed itself or assembled a RAID array
	// on the way to waiting.
	doc := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(doc, []byte(`#cloud-config
corium:
  role: worker
  join:
    token: placeholder
  api:
    enabled: true
`), 0o600); err != nil {
		t.Fatalf("writing document: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*enrolmentPoll)
	defer cancel()

	err := Run(ctx, Options{ConfigPath: doc, StateDir: t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want it to still be waiting at the deadline", err)
	}
}

func TestWaitForConfigurationEndsWhenAnOperatorAnswers(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "configured")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- waitForConfiguration(ctx, marker) }()

	select {
	case err := <-done:
		t.Fatalf("waitForConfiguration() returned %v before anybody answered, want it to wait", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("recording the applied configuration: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForConfiguration() = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitForConfiguration() did not notice that a configuration had been applied")
	}
}

func TestWaitForConfigurationDoesNotWaitForAnAnswerAlreadyGiven(t *testing.T) {
	// corium-apid and corium-bootstrap are ordered against cloud-init and not
	// against each other, so an apply can land before this wait begins. An
	// earlier version compared the document against the one the node booted
	// with, which in that order sees no change and waits for ever -- a node
	// that had been configured, holding, saying it wanted configuring.
	marker := filepath.Join(t.TempDir(), "configured")

	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("recording the applied configuration: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := waitForConfiguration(ctx, marker); err != nil {
		t.Fatalf("waitForConfiguration() = %v, want it to return at once", err)
	}
}
