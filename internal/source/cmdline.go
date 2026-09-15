package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// CmdlineKey is the kernel command line parameter naming a configuration.
//
//	corium.config=https://boot.example.com/node.yaml
//	corium.config=/run/media/config.yaml
//
// This is the escape hatch for PXE and netboot, where the only thing the
// operator controls at boot time is the kernel command line.
const CmdlineKey = "corium.config"

// cmdlinePath is the kernel command line. It is a variable so tests can point
// it somewhere else.
var cmdlinePath = "/proc/cmdline"

// fetchTimeout bounds how long boot waits for a remote configuration. Long
// enough for a slow network to come up, short enough that a node pointed at a
// dead endpoint fails visibly rather than hanging forever.
const fetchTimeout = 30 * time.Second

// maxConfigSize caps how much is read from a remote configuration.
const maxConfigSize = 1 << 20

type cmdlineSource struct{}

// KernelCmdline returns a source that reads the location named by
// corium.config= on the kernel command line.
func KernelCmdline() Source { return cmdlineSource{} }

func (cmdlineSource) Name() string { return "kernel cmdline" }

func (cmdlineSource) Load(ctx context.Context) ([]byte, error) {
	raw, err := os.ReadFile(cmdlinePath)
	if err != nil {
		// No kernel command line is not an error worth failing a boot over.
		return nil, ErrNotFound
	}

	location := parseCmdline(string(raw))
	if location == "" {
		return nil, ErrNotFound
	}

	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return fetch(ctx, location)
	}

	return File(location).Load(ctx)
}

// parseCmdline extracts the corium.config value from a kernel command line.
//
// The last occurrence wins, matching how the kernel itself treats repeated
// parameters: a value appended at boot overrides one baked into the bootloader.
func parseCmdline(cmdline string) string {
	var value string

	for _, field := range splitCmdline(cmdline) {
		key, val, found := strings.Cut(field, "=")
		if found && key == CmdlineKey {
			value = strings.Trim(val, `"`)
		}
	}

	return value
}

// splitCmdline splits a kernel command line into parameters.
//
// strings.Fields is not enough: the kernel honours double quotes, so
// `corium.config="/run/a b.yaml"` is one parameter and splitting on whitespace
// alone would truncate the path at the space.
func splitCmdline(cmdline string) []string {
	var (
		fields  []string
		current strings.Builder
		quoted  bool
	)

	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}

	for _, r := range cmdline {
		switch {
		case r == '"':
			quoted = !quoted
			current.WriteRune(r)
		case !quoted && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			current.WriteRune(r)
		}
	}

	flush()

	return fields
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() {
		// The body is read in full below; failing to close it cannot change
		// the result, and there is nothing useful to do with the error.
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: unexpected status %s", url, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxConfigSize))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("fetching %s: empty response", url)
	}

	return body, nil
}
