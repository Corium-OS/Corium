// Command cctl manages Corium nodes.
//
// It runs on an operator's machine, never on a node, and it keeps two things
// there: the operator CA that nodes are told to trust, and the client
// certificate signed by it that nodes check on every call. The CA's private
// key never leaves this machine — that is the property the whole scheme is
// built on, and the reason a CA certificate can sit in cloud-init in clear.
//
// See docs/adr/0004-management-api.md.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/systemd"
)

// Build metadata, injected at link time.
var (
	version = "dev"
	commit  = "unknown"
)

const usage = `cctl %s (%s)

Manage Corium nodes.

Usage:
  cctl <command> [flags]

Commands:
  pki init      Create the operator CA this fleet will trust
  pki issue     Sign a client certificate for yourself
  enroll        Claim an unenrolled node, using the code on its console
  apply         Give a node the corium: configuration it will bootstrap with
  status        Report what a node is: image, digest, role, k0s, health
  services      List the services this API knows about, and their state
  restart       Restart k0s on a node
  logs          Read a node's journal, optionally following it
  upgrade       Move nodes to another OS image, one at a time
  rollback      Mark a node's previous image as the next to boot
  cordon        Stop new pods being scheduled on a node (--undo to reverse)
  drain         Evict a node's workloads, cordoning it first
  reboot        Restart a node
  shutdown      Power a node off
  reset         Erase a node: leave its cluster, forget its owner, reboot
  ca rotate     Hand nodes to a different operator CA
  kubeconfig    Fetch the cluster's administrator kubeconfig from a node
  health        Check a node answers, and what it authenticated you as
  version       Print version information

Run 'cctl <command> -h' for command-specific flags.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cctl: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version, commit)

		return errors.New("no command given")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command, args := os.Args[1], os.Args[2:]

	switch command {
	case "pki":
		return pkiCommand(args)
	case "enroll", "enrol":
		return enrolCommand(ctx, args)
	case "apply":
		return applyCommand(ctx, args)
	case "status":
		return statusCommand(ctx, args)
	case "services":
		return servicesCommand(ctx, args)
	case "restart":
		return restartCommand(ctx, args)
	case "logs":
		return logsCommand(ctx, args)
	case "upgrade":
		return upgradeCommand(ctx, args)
	case "rollback":
		return rollbackCommand(ctx, args)
	case "cordon":
		return cordonCommand(ctx, args)
	case "drain":
		return drainCommand(ctx, args)
	case "reboot", "shutdown":
		return powerCommand(ctx, command, args)
	case "reset":
		return resetCommand(ctx, args)
	case "ca":
		return caCommand(ctx, args)
	case "kubeconfig":
		return kubeconfigCommand(ctx, args)
	case "health":
		return healthCommand(ctx, args)
	case "version":
		fmt.Printf("cctl %s (%s)\n", version, commit)

		return nil
	case "-h", "--help", "help":
		fmt.Printf(usage, version, commit)

		return nil
	default:
		fmt.Fprintf(os.Stderr, usage, version, commit)

		return fmt.Errorf("unknown command: %q", command)
	}
}

// parseFlags parses flags that may appear before or after positional arguments.
//
// Go's flag package stops at the first non-flag argument, so
// `cctl enroll 10.0.0.1 --code ABCD` parses no flags at all -- and that is
// precisely the form printed on a node's console and written in the
// documentation. Rather than telling people they typed it wrong, reorder it.
//
// Which flags take a value is asked of the FlagSet rather than assumed, by the
// same IsBoolFlag test the flag package itself uses, so a boolean added later
// does not silently swallow the argument after it.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	boolean := map[string]bool{}

	flags.VisitAll(func(f *flag.Flag) {
		if value, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && value.IsBoolFlag() {
			boolean[f.Name] = true
		}
	})

	var ordered, positional []string

	for i := 0; i < len(args); i++ {
		argument := args[i]

		// Everything after -- is positional by definition.
		if argument == "--" {
			positional = append(positional, args[i+1:]...)

			break
		}

		if !strings.HasPrefix(argument, "-") || argument == "-" {
			positional = append(positional, argument)

			continue
		}

		ordered = append(ordered, argument)

		name := strings.TrimLeft(argument, "-")
		if strings.Contains(name, "=") || boolean[name] {
			continue
		}

		// A flag expecting a value takes the next argument with it.
		if i+1 < len(args) {
			i++

			ordered = append(ordered, args[i])
		}
	}

	if err := flags.Parse(append(ordered, positional...)); err != nil {
		return nil, err
	}

	return flags.Args(), nil
}

// openStore resolves the operator's directory, which every command needs.
func openStore(dir string) (*cctl.Store, error) {
	if dir == "" {
		resolved, err := cctl.DefaultDir()
		if err != nil {
			return nil, err
		}

		dir = resolved
	}

	return cctl.NewStore(dir), nil
}

func pkiCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("pki needs a subcommand: init or issue")
	}

	switch args[0] {
	case "init":
		return pkiInit(args[1:])
	case "issue":
		return pkiIssue(args[1:])
	default:
		return fmt.Errorf("unknown pki subcommand %q; use init or issue", args[0])
	}
}

func pkiInit(args []string) error {
	flags := flag.NewFlagSet("cctl pki init", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	name := flags.String("name", "corium operators", "common name for the CA")

	if _, err := parseFlags(flags, args); err != nil {
		return err
	}

	store, err := openStore(*dir)
	if err != nil {
		return err
	}

	if err := cctl.InitCA(store, *name); err != nil {
		return err
	}

	certificate, err := cctl.OperatorCA(store)
	if err != nil {
		return err
	}

	fmt.Printf("Created an operator CA in %s\n\n", store.Dir())
	fmt.Printf("  certificate  %s\n", store.Path(cctl.CACertFile))
	fmt.Printf("  private key  %s\n\n", store.Path(cctl.CAKeyFile))
	fmt.Print("The key owns every node that trusts this CA. It never leaves this\n" +
		"machine, and losing it means re-enrolling every node from its console.\n\n" +
		"The certificate is public. Put it in cloud-init to have nodes trust you\n" +
		"without any console visit at all:\n\n")

	fmt.Printf("corium:\n  api:\n    operatorCA: |\n%s\n", indent(string(certificate), "      "))
	fmt.Print("Next: cctl pki issue --role admin\n")

	return nil
}

func pkiIssue(args []string) error {
	flags := flag.NewFlagSet("cctl pki issue", flag.ExitOnError)

	var (
		dir      = flags.String("dir", "", "operator directory (default ~/.corium)")
		roleName = flags.String("role", "admin", "readonly, operator or admin")
		name     = flags.String("name", "", "common name (default: your username)")
		lifetime = flags.Duration("lifetime", cctl.DefaultClientLifetime,
			"how long the certificate is valid")
	)

	if _, err := parseFlags(flags, args); err != nil {
		return err
	}

	role, err := cctl.ParseRole(*roleName)
	if err != nil {
		return err
	}

	if *name == "" {
		if *name = os.Getenv("USER"); *name == "" {
			*name = "corium operator"
		}
	}

	store, err := openStore(*dir)
	if err != nil {
		return err
	}

	if err := cctl.Issue(store, *name, role, *lifetime); err != nil {
		return err
	}

	fmt.Printf("Signed a client certificate for %q as %s, valid for %s.\n\n", *name, role, *lifetime)
	fmt.Printf("  certificate  %s\n", store.Path(cctl.ClientCertFile))
	fmt.Printf("  private key  %s\n", store.Path(cctl.ClientKeyFile))

	return nil
}

func enrolCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl enroll", flag.ExitOnError)

	var (
		dir  = flags.String("dir", "", "operator directory (default ~/.corium)")
		code = flags.String("code", "",
			"the pairing code printed on the node's console; a node with "+
				"api.insecure set asks for none")
		fingerprint = flags.String("fingerprint", "",
			"the fingerprint printed beside it; without this you are asked to confirm")
		configFile = flags.String("config", "",
			"a corium: document to give the node as part of claiming it; it "+
				"bootstraps with this instead of what it booted with")
	)

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if len(rest) != 1 {
		return errors.New("usage: cctl enroll <address> --code <code>")
	}

	address := withDefaultPort(rest[0])

	store, err := openStore(*dir)
	if err != nil {
		return err
	}

	operatorCA, err := cctl.OperatorCA(store)
	if err != nil {
		return err
	}

	// Enrolment is the one call made before any trust exists, so the node's
	// identity is established here or never: either against the fingerprint
	// from the console, or by a person confirming the one it presented.
	if *fingerprint == "" {
		if *fingerprint, err = confirmFingerprint(ctx, address); err != nil {
			return err
		}
	}

	// Read and checked before the node is touched. A document that cctl itself
	// would refuse should never become the reason an enrolment half-happened.
	document, err := readConfigDocument(*configFile)
	if err != nil {
		return err
	}

	client := cctl.Dial(address, *fingerprint)

	result, err := client.Enrol(ctx, *code, operatorCA, document)
	if err != nil {
		return err
	}

	if err := store.Remember(address, *fingerprint); err != nil {
		return err
	}

	fmt.Printf("Claimed %s.\n\n", address)
	fmt.Printf("  node fingerprint  %s\n", *fingerprint)
	fmt.Printf("  operator CA       %s\n\n", result.OperatorCA)
	describes := "the cluster its cloud-init configuration describes"
	if len(document) > 0 {
		describes = "the cluster the configuration you just sent describes"
	}

	fmt.Print("The node is restarting to require your client certificate, and its\n" +
		"bootstrap is released: it will now join " + describes + ".\n\n" +
		"Check with: cctl health " + address + "\n")

	return nil
}

// confirmFingerprint connects once, without sending anything, to show a human
// what is answering.
//
// A `yes` here is the same statement `--fingerprint` makes, just typed later.
// It is refused when nobody is watching, because a script that confirms
// whatever answers has verified nothing while looking as though it has.
func confirmFingerprint(ctx context.Context, address string) (string, error) {
	probe := cctl.Dial(address, "")
	if _, err := probe.Health(ctx); err == nil {
		// An unenrolled node refuses /v1/health, so a node that answers it is
		// already claimed. The handshake is what mattered regardless.
		_ = err
	}

	seen := probe.Fingerprint()
	if seen == "" {
		return "", fmt.Errorf("could not reach %s to read its certificate", address)
	}

	if !isTerminal(os.Stdin) {
		return "", fmt.Errorf("%s presented %s; pass --fingerprint to confirm it, "+
			"since there is nobody here to ask", address, seen)
	}

	fmt.Printf("%s presents the certificate\n\n  %s\n\n"+
		"It should match the fingerprint printed on the node's console, beside\n"+
		"the pairing code.\n\nDoes it match? [y/N] ", address, seen)

	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')

	switch {
	case errors.Is(err, io.EOF):
		// Nobody was there after all: stdin is closed, or redirected from
		// /dev/null, which is a character device and so passes for a terminal.
		// Say the useful thing rather than reporting EOF at somebody.
		return "", fmt.Errorf("%s presented %s, and there is nobody here to confirm it; "+
			"pass --fingerprint with the value from the node's console", address, seen)
	case err != nil:
		return "", fmt.Errorf("reading your answer: %w", err)
	}

	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return "", errors.New("not confirmed, so nothing was sent")
	}

	return seen, nil
}

func statusCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl status", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		asJSON      = flags.Bool("json", false, "print the node's reply verbatim")
	)

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if len(rest) != 1 {
		return errors.New("usage: cctl status <address>")
	}

	address := withDefaultPort(rest[0])

	client, err := connect(*dir, address, *fingerprint)
	if err != nil {
		return err
	}

	node, err := client.Node(ctx)
	if err != nil {
		return err
	}

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		return encoder.Encode(node)
	}

	cctl.FormatNode(os.Stdout, address, node)

	return nil
}

func servicesCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl services", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	fingerprint := flags.String("fingerprint", "", "override the remembered fingerprint")

	client, _, err := target(flags, args, "cctl services <address>", dir, fingerprint)
	if err != nil {
		return err
	}

	services, err := client.Services(ctx)
	if err != nil {
		return err
	}

	for _, service := range services {
		state := service.Active
		if service.Sub != "" {
			state += "/" + service.Sub
		}

		fmt.Printf("  %-32s %-18s %s\n", service.Name, state, service.Purpose)
	}

	return nil
}

func restartCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl restart", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		unit        = flags.String("unit", "", "the unit to restart; required")
	)

	client, _, err := target(flags, args, "cctl restart <address> --unit <unit>", dir, fingerprint)
	if err != nil {
		return err
	}

	if *unit == "" {
		return errors.New("--unit is required; `cctl services <address>` lists what can be restarted")
	}

	status, err := client.Restart(ctx, *unit)
	if err != nil {
		return err
	}

	// systemd accepting the job is not the service being up, and the node
	// reports the state it actually landed in.
	fmt.Printf("%s is now %s/%s\n", status.Name, status.Active, status.Sub)

	return nil
}

func logsCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl logs", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		unit        = flags.String("unit", "", "a unit name, or `kernel`; default every unit")
		lines       = flags.Int("lines", 0, "how many records to start from")
		since       = flags.String("since", "", "only records newer than this, such as 15m")
		follow      = flags.Bool("follow", false, "keep the stream open as records arrive")
	)

	client, _, err := target(flags, args, "cctl logs <address> [--unit <unit>]", dir, fingerprint)
	if err != nil {
		return err
	}

	query := cctl.LogQuery{Unit: *unit, Lines: *lines, Since: *since, Follow: *follow}

	// Written straight out as each record arrives rather than collected: the
	// point of following a log is seeing a line before the request ends.
	return client.Logs(ctx, query, func(record systemd.Record) {
		cctl.FormatRecord(os.Stdout, record)
	})
}

func upgradeCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl upgrade", flag.ExitOnError)

	var (
		dir    = flags.String("dir", "", "operator directory (default ~/.corium)")
		image  = flags.String("image", "", "the image to move to; required")
		settle = flags.Duration("settle", cctl.DefaultSettle,
			"how long to wait for a node to come back")
	)

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		return errors.New("usage: cctl upgrade <address>... --image <image>")
	}

	if *image == "" {
		return errors.New("--image is required")
	}

	store, err := openStore(*dir)
	if err != nil {
		return err
	}

	addresses := make([]string, 0, len(rest))
	for _, argument := range rest {
		addresses = append(addresses, withDefaultPort(argument))
	}

	// One at a time, stopping at the first node that does not come back. A
	// rollout that carries on past a broken machine turns one outage into a
	// cluster-wide one.
	rollout := &cctl.Rollout{
		Image:   *image,
		Nodes:   addresses,
		Settle:  *settle,
		Out:     os.Stdout,
		Connect: func(address string) (*cctl.Client, error) { return connectWith(store, address, "") },
	}

	return rollout.Run(ctx)
}

func rollbackCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl rollback", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	fingerprint := flags.String("fingerprint", "", "override the remembered fingerprint")

	client, address, err := target(flags, args, "cctl rollback <address>", dir, fingerprint)
	if err != nil {
		return err
	}

	if err := client.Rollback(ctx); err != nil {
		return err
	}

	// Deliberately not a reboot. Rollback exists because somebody is already
	// having a bad day; taking the node down at a moment they did not choose
	// would not help.
	fmt.Printf("%s will boot its previous image next. Reboot it when you are ready:\n", address)
	fmt.Printf("  cctl restart %s --unit k0sworker   # or reboot the machine\n", address)

	return nil
}

func cordonCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl cordon", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		undo        = flags.Bool("undo", false, "put the node back into service")
	)

	client, address, err := target(flags, args, "cctl cordon <address> [--undo]", dir, fingerprint)
	if err != nil {
		return err
	}

	if err := client.Cordon(ctx, *undo); err != nil {
		return err
	}

	if *undo {
		fmt.Printf("%s is back in service\n", address)
	} else {
		fmt.Printf("%s takes no new pods. Its existing ones stay: `cctl drain` moves those.\n",
			address)
	}

	return nil
}

func drainCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl drain", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	fingerprint := flags.String("fingerprint", "", "override the remembered fingerprint")

	client, address, err := target(flags, args, "cctl drain <address>", dir, fingerprint)
	if err != nil {
		return err
	}

	fmt.Printf("draining %s...\n", address)

	if err := client.Drain(ctx); err != nil {
		return err
	}

	fmt.Printf("%s is drained and cordoned. `cctl cordon %s --undo` returns it.\n",
		address, address)

	return nil
}

func powerCommand(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet("cctl "+command, flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	fingerprint := flags.String("fingerprint", "", "override the remembered fingerprint")

	client, address, err := target(flags, args, "cctl "+command+" <address>", dir, fingerprint)
	if err != nil {
		return err
	}

	if command == "shutdown" {
		// Worth saying out loud, because this is the one call in the API that
		// nothing in the API can undo.
		fmt.Printf("Powering %s off. Nothing here can turn it back on.\n", address)
	}

	if command == "reboot" {
		err = client.Reboot(ctx)
	} else {
		err = client.Shutdown(ctx)
	}

	if err != nil {
		return err
	}

	fmt.Printf("%s accepted; the connection will drop as it goes.\n", address)

	return nil
}

func resetCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl reset", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		confirm     = flags.String("confirm", "",
			"the node's own name, which it requires before erasing itself")
	)

	client, address, err := target(flags, args, "cctl reset <address> --confirm <node name>",
		dir, fingerprint)
	if err != nil {
		return err
	}

	// Asked of the node rather than assumed from the address, so that what is
	// typed is checked against the machine that would actually be erased.
	node, err := client.Node(ctx)
	if err != nil {
		return err
	}

	// Checked here as well as on the node, so that a mistyped name is a
	// refusal rather than a round trip that prints "erasing" first and then
	// takes it back.
	if *confirm != node.Hostname {
		if *confirm == "" {
			return fmt.Errorf("this erases %s (%s): it leaves its cluster, forgets its "+
				"owner and reboots unclaimed. Re-run with --confirm %s to mean it",
				node.Hostname, address, node.Hostname)
		}

		return fmt.Errorf("%s calls itself %s, not %q -- check you have the right address",
			address, node.Hostname, *confirm)
	}

	fmt.Printf("Erasing %s...\n", node.Hostname)

	if err := client.Reset(ctx, *confirm); err != nil {
		return err
	}

	fmt.Printf("%s has left its cluster and forgotten its owner. It is rebooting,\n"+
		"and will come back unclaimed -- with a new fingerprint, so the one\n"+
		"remembered here no longer matches.\n", node.Hostname)

	return nil
}

func caCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "rotate" {
		return errors.New("ca needs a subcommand: rotate")
	}

	flags := flag.NewFlagSet("cctl ca rotate", flag.ExitOnError)

	var (
		dir    = flags.String("dir", "", "operator directory (default ~/.corium)")
		newDir = flags.String("to", "",
			"a second operator directory holding the CA to move to; required")
		roleName = flags.String("role", "admin",
			"the role of the certificate minted under the new CA")
		lifetime = flags.Duration("lifetime", cctl.DefaultClientLifetime,
			"how long that certificate is valid")
	)

	rest, err := parseFlags(flags, args[1:])
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		return errors.New("usage: cctl ca rotate <address>... --to <directory>")
	}

	if *newDir == "" {
		return errors.New("--to is required: the directory holding the CA to move to, " +
			"as made by `cctl pki init --dir <directory>`")
	}

	return rotate(ctx, *dir, *newDir, *roleName, *lifetime, rest)
}

// rotate moves a set of nodes to a new operator CA.
//
// The new credentials are minted first and sent to each node as proof, because
// the mistake this is guarding against -- rotating to a CA you cannot issue
// certificates under -- produces a node that only ever accepts somebody else,
// and the way back is a trip to its console.
func rotate(
	ctx context.Context, dir, newDir, roleName string, lifetime time.Duration, addresses []string,
) error {
	role, err := cctl.ParseRole(roleName)
	if err != nil {
		return err
	}

	current, err := openStore(dir)
	if err != nil {
		return err
	}

	next, err := openStore(newDir)
	if err != nil {
		return err
	}

	operatorCA, err := cctl.OperatorCA(next)
	if err != nil {
		return err
	}

	name := os.Getenv("USER")
	if name == "" {
		name = "corium operator"
	}

	// Minted into the new directory, beside the CA that signed it, so the two
	// sets of credentials never share a filename while both are in use.
	if err := cctl.IssueTo(next, cctl.ClientCertFile, cctl.ClientKeyFile,
		name, role, lifetime); err != nil {
		return err
	}

	proof, err := os.ReadFile(next.Path(cctl.ClientCertFile))
	if err != nil {
		return fmt.Errorf("reading the certificate just minted: %w", err)
	}

	for _, argument := range addresses {
		address := withDefaultPort(argument)

		client, err := connectWith(current, address, "")
		if err != nil {
			return fmt.Errorf("%s: %w", address, err)
		}

		if err := client.RotateCA(ctx, operatorCA, proof); err != nil {
			return fmt.Errorf("%s: %w", address, err)
		}

		fmt.Printf("%s now obeys the CA in %s\n", address, next.Dir())

		// Carried over so the new directory can reach the node on its own: the
		// node's fingerprint has not changed, only who may talk to it.
		fingerprint, err := current.Fingerprint(address)
		if err != nil {
			return err
		}

		if err := next.Remember(address, fingerprint); err != nil {
			return err
		}
	}

	fmt.Printf("\nEach node is restarting to pick the new CA up. From now on use\n"+
		"  cctl <command> --dir %s\n"+
		"and check one before you put the old directory away:\n"+
		"  cctl health %s --dir %s\n",
		next.Dir(), withDefaultPort(addresses[0]), next.Dir())

	return nil
}

// applyCommand gives a node the document it will bootstrap with.
//
// It works only before the node has bootstrapped, and the node enforces that
// rather than this: a rule that decides whether a machine's configuration and
// its behaviour can diverge belongs on the machine. See ADR 4.
func applyCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl apply", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		configFile  = flags.String("file", "", "the corium: document to send; - for standard input")
	)

	client, address, err := target(flags, args,
		"cctl apply <address> --file <document.yaml>", dir, fingerprint)
	if err != nil {
		return err
	}

	if *configFile == "" {
		return errors.New("pass --file with the corium: document to send")
	}

	document, err := readConfigDocument(*configFile)
	if err != nil {
		return err
	}

	result, err := client.ApplyConfig(ctx, document)
	if err != nil {
		return err
	}

	fmt.Printf("Applied to %s.\n\n", address)
	fmt.Printf("  written to  %s\n", result.Path)
	fmt.Printf("  role        %s\n\n", result.Role)

	if !result.API {
		// Worth saying loudly. The document is the node's whole configuration,
		// not a patch, so leaving api: out of it is how an operator takes away
		// the only way they have of reaching the machine.
		fmt.Print("Warning: that document does not ask for the management API, so this\n" +
			"node will stop serving it once it has bootstrapped. You would then\n" +
			"need console or SSH access to reach it.\n\n")
	}

	fmt.Print("The node bootstraps with this the next time its bootstrap runs --\n" +
		"now, if it was holding for one.\n")

	return nil
}

// readConfigDocument reads a corium: document and checks it before it is sent.
//
// Checking here as well as on the node is not duplication for its own sake: a
// document rejected on the operator's machine costs a message, and one
// rejected on the node costs a round trip to a machine that may be waiting on
// a console somebody has to walk to.
func readConfigDocument(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}

	var (
		document []byte
		err      error
	)

	if path == "-" {
		document, err = io.ReadAll(os.Stdin)
	} else {
		// The path is one the operator typed on their own machine.
		document, err = os.ReadFile(path) // #nosec G304
	}

	if err != nil {
		return nil, fmt.Errorf("reading the configuration: %w", err)
	}

	cfg, err := config.Parse(document)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return document, nil
}

func kubeconfigCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl kubeconfig", flag.ExitOnError)

	var (
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		fingerprint = flags.String("fingerprint", "", "override the remembered fingerprint")
		server      = flags.String("server", "",
			"address clients should reach the control plane on; "+
				"default is the cluster's virtual IP, or this node")
		output = flags.String("output", "",
			"write to this file instead of standard output")
		force = flags.Bool("force", false, "overwrite the output file if it exists")
	)

	client, address, err := target(flags, args,
		"cctl kubeconfig <address> [--server <addr>] [--output <file>]", dir, fingerprint)
	if err != nil {
		return err
	}

	raw, err := client.Kubeconfig(ctx, *server)
	if err != nil {
		return err
	}

	if *output == "" {
		// To standard output by default, and deliberately not to
		// ~/.kube/config: merging into somebody's existing contexts is a
		// decision with no undo, and `> file` or `--output` says it plainly.
		_, err := os.Stdout.Write(raw)

		return err
	}

	if _, err := os.Stat(*output); err == nil && !*force {
		return fmt.Errorf("%s exists; pass --force to replace it", *output)
	}

	// 0600: this is cluster-admin. Anything wider hands the cluster to every
	// account on the machine.
	//
	// The path comes from a flag the operator typed on their own machine,
	// which is the whole point of --output; there is no privilege boundary
	// here for a traversal to cross.
	if err := os.WriteFile(*output, raw, 0o600); err != nil { //nolint:gosec // G703: the operator's own path
		return err
	}

	fmt.Fprintf(os.Stderr, "Wrote %s -- these are cluster administrator credentials for %s.\n",
		*output, address)

	return nil
}

// target parses the flags every node-facing command shares and connects.
func target(
	flags *flag.FlagSet, args []string, usage string, dir, fingerprint *string,
) (*cctl.Client, string, error) {
	rest, err := parseFlags(flags, args)
	if err != nil {
		return nil, "", err
	}

	if len(rest) != 1 {
		return nil, "", errors.New("usage: " + usage)
	}

	address := withDefaultPort(rest[0])

	client, err := connect(*dir, address, *fingerprint)
	if err != nil {
		return nil, "", err
	}

	return client, address, nil
}

// connect builds a client for an address, using the remembered fingerprint and
// the operator's certificate. Every authenticated command needs both.
func connect(dir, address, fingerprint string) (*cctl.Client, error) {
	store, err := openStore(dir)
	if err != nil {
		return nil, err
	}

	return connectWith(store, address, fingerprint)
}

// connectWith is the same, for a store that is already open -- a rollout opens
// one connection per node and should not reread the directory each time.
func connectWith(store *cctl.Store, address, fingerprint string) (*cctl.Client, error) {
	var err error

	if fingerprint == "" {
		if fingerprint, err = store.Fingerprint(address); err != nil {
			return nil, err
		}
	} else if err := store.Remember(address, fingerprint); err != nil {
		// A fingerprint typed on the command line is an operator saying "this
		// is the machine", which is the same assertion `enroll` records. Two
		// things need it: a node claimed from cloud-init, which cctl has never
		// spoken to, and a node whose identity was reset. Without this they
		// would need --fingerprint on every call forever -- and `upgrade`
		// takes a list of nodes, where a single such flag means nothing.
		return nil, err
	}

	if fingerprint == "" {
		return nil, fmt.Errorf("%w for %s; enrol it, or pass --fingerprint once "+
			"with the value from its console and it will be remembered",
			cctl.ErrFingerprintUnknown, address)
	}

	certificates, err := cctl.ClientCertificate(store)
	if err != nil {
		return nil, err
	}

	if certificates == nil {
		return nil, errors.New("no client certificate; run `cctl pki issue --role admin`")
	}

	return cctl.Dial(address, fingerprint, certificates...), nil
}

func healthCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("cctl health", flag.ExitOnError)
	dir := flags.String("dir", "", "operator directory (default ~/.corium)")
	fingerprint := flags.String("fingerprint", "", "override the remembered fingerprint")

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if len(rest) != 1 {
		return errors.New("usage: cctl health <address>")
	}

	address := withDefaultPort(rest[0])

	client, err := connect(*dir, address, *fingerprint)
	if err != nil {
		return err
	}

	health, err := client.Health(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("%s  %s  (authenticated as %s)\n", address, health.Status, health.Role)

	return nil
}

// withDefaultPort lets an operator type an address without remembering 7443.
func withDefaultPort(address string) string {
	if strings.Contains(address, ":") && !strings.HasSuffix(address, "]") {
		return address
	}

	return fmt.Sprintf("%s:%d", address, apiDefaultPort)
}

// apiDefaultPort mirrors api.DefaultPort. It is duplicated rather than
// imported so that this command does not pull the node-side package, and its
// value is in the ADR and the documentation besides.
const apiDefaultPort = 7443

func indent(text, prefix string) string {
	var out strings.Builder

	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		out.WriteString(prefix + line + "\n")
	}

	return out.String()
}

// isTerminal reports whether there is plausibly a person on the other end.
//
// Regular files and pipes are certainly not; character devices usually are.
// The test is deliberately this crude so that a command-line tool need not add
// a dependency to ask one question, and it is wrong in one direction only:
// /dev/null is a character device too, so stdin redirected from it looks
// interactive. That case is caught where the answer is read, by treating EOF
// as nobody rather than as a failure.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
