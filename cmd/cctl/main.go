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

	"github.com/Corium-OS/Corium/internal/cctl"
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
  status        Report what a node is: image, digest, role, k0s, health
  services      List the services this API knows about, and their state
  restart       Restart k0s on a node
  logs          Read a node's journal, optionally following it
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
	case "status":
		return statusCommand(ctx, args)
	case "services":
		return servicesCommand(ctx, args)
	case "restart":
		return restartCommand(ctx, args)
	case "logs":
		return logsCommand(ctx, args)
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
		dir         = flags.String("dir", "", "operator directory (default ~/.corium)")
		code        = flags.String("code", "", "the pairing code printed on the node's console")
		fingerprint = flags.String("fingerprint", "",
			"the fingerprint printed beside it; without this you are asked to confirm")
	)

	rest, err := parseFlags(flags, args)
	if err != nil {
		return err
	}

	if len(rest) != 1 {
		return errors.New("usage: cctl enroll <address> --code <code>")
	}

	if *code == "" {
		return errors.New("--code is required; it is printed on the node's console")
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

	client := cctl.Dial(address, *fingerprint)

	result, err := client.Enrol(ctx, *code, operatorCA)
	if err != nil {
		return err
	}

	if err := store.Remember(address, *fingerprint); err != nil {
		return err
	}

	fmt.Printf("Claimed %s.\n\n", address)
	fmt.Printf("  node fingerprint  %s\n", *fingerprint)
	fmt.Printf("  operator CA       %s\n\n", result.OperatorCA)
	fmt.Print("The node is restarting to require your client certificate, and its\n" +
		"bootstrap is released: it will now join the cluster its cloud-init\n" +
		"configuration describes.\n\n" +
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

	if fingerprint == "" {
		if fingerprint, err = store.Fingerprint(address); err != nil {
			return nil, err
		}
	}

	if fingerprint == "" {
		return nil, fmt.Errorf("%w for %s; enrol it first, or pass --fingerprint",
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
