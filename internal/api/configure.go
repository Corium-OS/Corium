package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/secret"
)

// ClaimFromConfig pins the operator CA a configuration names, if it names one.
//
// This is modes A and B of ADR 4 — the certificate inline, or fetched at first
// boot — and it is what makes them differ from maintenance mode only in where
// the certificate came from. Once this has run, the node is enrolled by
// exactly the same measure as one claimed from the console: the CA is on disk,
// and nothing will replace it.
//
// It is idempotent. A node that is already claimed keeps the CA it has, and
// says so rather than failing: a reboot must not turn a working node into a
// broken one because the configuration it booted with has since been edited.
func ClaimFromConfig(ctx context.Context, store *Store, cfg *config.Config) error {
	if cfg.API.Mode() != config.APIModeConfigured {
		return nil
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		return err
	}

	if enrolled {
		if err := warnIfChanged(store, cfg); err != nil {
			return err
		}

		return nil
	}

	pemData, err := operatorCAFromConfig(ctx, cfg)
	if err != nil {
		return err
	}

	if err := store.Adopt(pemData); err != nil {
		return fmt.Errorf("pinning the operator CA from the configuration: %w", err)
	}

	if err := store.RecordClaim(ClaimedFromConfiguration); err != nil {
		return err
	}

	certificate, err := store.OperatorCA()
	if err != nil {
		return err
	}

	slog.Info("node claimed from its configuration",
		"operatorCA", certificate.Subject.CommonName,
		"fingerprint", Fingerprint(certificate.Raw))

	return nil
}

func operatorCAFromConfig(ctx context.Context, cfg *config.Config) ([]byte, error) {
	if cfg.API.OperatorCA != "" {
		return []byte(cfg.API.OperatorCA), nil
	}

	// The same resolver, and the same waiting behaviour, as a join token: a
	// node may boot before whatever publishes the CA has finished starting.
	resolved, err := secret.Resolve(ctx, cfg.API.OperatorCAFrom, "operator CA")
	if err != nil {
		return nil, err
	}

	return []byte(resolved), nil
}

// warnIfChanged reports a configuration naming a different CA from the pinned
// one.
//
// It warns rather than fails, and the choice is deliberate. Failing would mean
// an edited cloud-config could stop a healthy node from booting, and the node
// is not wrong -- it is doing what enrolment being one-way says it should.
// Rotating a CA is `cctl ca rotate`, or a local command, and both say so.
func warnIfChanged(store *Store, cfg *config.Config) error {
	if cfg.API.OperatorCA == "" {
		// Only the inline form can be compared without a network call, and
		// this runs on every boot.
		return nil
	}

	wanted, err := config.ParseOperatorCA([]byte(cfg.API.OperatorCA))
	if err != nil {
		return err
	}

	pinned, err := store.OperatorCA()
	if err != nil {
		return err
	}

	if Fingerprint(pinned.Raw) == Fingerprint(wanted.Raw) {
		return nil
	}

	slog.Warn("configuration names a different operator CA from the pinned one, which wins",
		"pinned", Fingerprint(pinned.Raw),
		"configured", Fingerprint(wanted.Raw),
		"toRotate", "cctl ca rotate, or corium-agent api set-ca on the node")

	return nil
}

// ErrDisabled reports a node whose configuration asks for no management API.
var ErrDisabled = errors.New("management API is disabled")
