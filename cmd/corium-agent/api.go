package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/lifecycle"
)

const apiUsage = `corium-agent api <subcommand>

  set-ca --file <operator-ca.pem>   Replace the operator CA this node obeys

This is the way back when the key that owns a fleet is lost. It is a local
command: it opens no port and accepts no request, and root on this machine
already owns it, so it grants nothing that was not already granted.

The node keeps its cluster membership and its identity. Only who may manage it
changes, which is why this is not a reset.
`

func apiCommand(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, apiUsage)

		return errors.New("api needs a subcommand")
	}

	if args[0] != "set-ca" {
		fmt.Fprint(os.Stderr, apiUsage)

		return fmt.Errorf("unknown api subcommand %q", args[0])
	}

	flags := flag.NewFlagSet("corium-agent api set-ca", flag.ExitOnError)

	var (
		file     = flags.String("file", "", "PEM certificate of the CA to obey; required")
		stateDir = flags.String("state-dir", lifecycle.StateDir, "Corium's state directory")
	)

	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	if *file == "" {
		return errors.New("--file is required: the PEM certificate of the CA to obey")
	}

	pemData, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *file, err)
	}

	store := api.NewStore(filepath.Join(*stateDir, "api"))

	enrolled, err := store.Enrolled()
	if err != nil {
		return err
	}

	// Rotate rather than Adopt, and it refuses an unclaimed node on purpose:
	// installing a CA on a node nobody has claimed is enrolment by another
	// name, and would walk around the pairing code that guards it. Such a node
	// is claimed with `cctl enroll`.
	if !enrolled {
		return errors.New("this node has no owner to replace; claim it with `cctl enroll`")
	}

	if err := store.Rotate(pemData); err != nil {
		return err
	}

	certificate, err := store.OperatorCA()
	if err != nil {
		return err
	}

	fmt.Printf("This node now obeys %q (%s).\n\n",
		certificate.Subject.CommonName, api.Fingerprint(certificate.Raw))
	fmt.Print("The running daemon still has the old one loaded; restart it to\n" +
		"pick this up:\n\n  systemctl restart corium-apid.service\n")

	return nil
}
