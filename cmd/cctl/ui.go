package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"

	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/ui"
)

// uiCommand serves a dashboard for a set of nodes on this machine.
//
// Naming no node shows every node this directory already knows a fingerprint
// for, which is the set cctl can reach without being told anything further.
// That is a convenience and not an inventory: the file records what has been
// enrolled from here, so a node somebody else claimed is absent until it is
// named on the command line once.
func uiCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl ui", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	listen := flags.String("listen", ui.DefaultListen, "address to serve the dashboard on")
	fingerprint := flags.String("fingerprint", "", "record this fingerprint for a single named node")

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if *fingerprint != "" && len(rest) != 1 {
		return errors.New("--fingerprint applies to one node; name it")
	}

	store, err := openStore(*dir)
	if err != nil {
		return err
	}

	addresses, err := uiAddresses(store, rest)
	if err != nil {
		return err
	}

	// Recorded once, here, rather than carried into every connection: the
	// dashboard dials the same node repeatedly, and the fingerprint is an
	// operator saying "this is the machine" once, exactly as `enroll` does.
	if *fingerprint != "" {
		if err := store.Remember(addresses[0], *fingerprint); err != nil {
			return err
		}
	}

	// Loopback unless the operator insisted otherwise, and the check is here
	// rather than in the package so that the refusal can name the flag. This
	// server holds the certificate that can drain a node and asks for no
	// password: on a shared network it is the fleet's credentials on a public
	// port. Someone who genuinely wants that can pass an address anyway.
	if err := warnIfPublic(*listen); err != nil {
		return err
	}

	server, err := ui.NewServer(*listen, addresses, func(address string) (*cctl.Client, error) {
		return connectWith(store, address, "")
	})
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("binding %s: %w", *listen, err)
	}

	fmt.Printf("cctl ui: %d node(s)\n\n  %s\n\nCtrl-C to stop.\n",
		len(addresses), server.URL())

	return server.Serve(ctx, listener)
}

// uiAddresses is the node list: what was typed, or what has been enrolled.
func uiAddresses(store *cctl.Store, named []string) ([]string, error) {
	if len(named) > 0 {
		addresses := make([]string, 0, len(named))
		for _, address := range named {
			addresses = append(addresses, withDefaultPort(address))
		}

		return addresses, nil
	}

	config, err := store.Config()
	if err != nil {
		return nil, err
	}

	addresses := make([]string, 0, len(config.Nodes))
	for address := range config.Nodes {
		addresses = append(addresses, withDefaultPort(address))
	}

	if len(addresses) == 0 {
		return nil, errors.New("no nodes known; name them, or enrol one first " +
			"with `cctl enroll <address> --code <code>`")
	}

	// Sorted so the page does not reshuffle between runs. Map iteration order
	// is not an ordering an operator should have to read around.
	sort.Strings(addresses)

	return addresses, nil
}

func warnIfPublic(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}

	if host == "" {
		return errors.New("--listen with no host binds every interface, which " +
			"puts an unauthenticated port in front of your operator certificate; " +
			"use 127.0.0.1:<port>, or name an address explicitly")
	}

	address := net.ParseIP(host)
	if address != nil && !address.IsLoopback() {
		fmt.Fprintf(os.Stderr,
			"cctl: warning: %s is not loopback. Anyone who reaches it and has the "+
				"token acts with your certificate.\n", host)
	}

	return nil
}
