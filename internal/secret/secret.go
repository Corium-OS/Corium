// Package secret resolves a value a node is told about rather than given: a
// join token, a VRRP password, an operator CA. The indirection exists so that
// credentials need not sit in instance metadata, where anything reaching the
// metadata service can read them.
//
// It lives apart from the code that consumes it because three unrelated things
// now need it -- cluster bootstrap, HA, and the management API -- and the last
// of those must not have to import the first.
package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

// fetchTimeout bounds how long first boot waits for a token service.
// Long enough to tolerate a slow network coming up, short enough that a node
// pointed at a dead endpoint fails visibly instead of hanging forever.
const fetchTimeout = 30 * time.Second

// maxSize caps how much is read from a token source. A k0s join token is a
// few kilobytes; anything larger is a misconfiguration or a wrong URL, and
// reading it into memory unbounded would be the wrong response either way.
const maxSize = 256 << 10

// errNotYet marks a secret that is absent rather than refused: whatever mints
// it has not got there yet. Only this is worth retrying.
var errNotYet = errors.New("not available yet")

// Resolve reads a secret, waiting for it to appear if allowed to.
//
// The what argument names the value for the journal -- "join token", "operator
// CA". It is used in messages and never holds the value itself: errors here
// deliberately avoid quoting any response body, because what is being handled
// is a credential and a failure message that echoes it into the journal has
// leaked it to every reader of that journal.
func Resolve(ctx context.Context, src *config.SecretSource, what string) (string, error) {
	wait, err := waitBudget(src)
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(wait)

	for attempt := 1; ; attempt++ {
		secret, err := readOnce(ctx, src, what)

		switch {
		case err == nil:
			if attempt > 1 {
				slog.Info("secret appeared", "what", what, "attempts", attempt)
			}

			return secret, nil

		case !errors.Is(err, errNotYet):
			// Refused, malformed, unreachable: waiting changes nothing.
			return "", err

		case wait == 0:
			return "", fmt.Errorf("%s: %w", what, err)

		case !time.Now().Before(deadline):
			return "", fmt.Errorf("%s did not appear within %s", what, wait)
		}

		delay := retryDelay(attempt)
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}

		slog.Info("waiting for secret",
			"what", what, "attempt", attempt, "retryIn", delay.Round(time.Second))

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
}

// waitBudget is how long the caller is willing to wait.
func waitBudget(src *config.SecretSource) (time.Duration, error) {
	if src.WaitFor == "" {
		return 0, nil
	}

	wait, err := time.ParseDuration(src.WaitFor)
	if err != nil {
		return 0, fmt.Errorf("parsing waitFor %q: %w", src.WaitFor, err)
	}

	return wait, nil
}

// retryDelay backs off and then settles. The secret often appears within
// seconds, so early attempts are cheap; after that there is no point hammering.
func retryDelay(attempt int) time.Duration {
	shift := attempt
	if shift > 5 {
		shift = 5
	}

	return time.Duration(1<<shift) * time.Second
}

// readOnce makes a single attempt.
func readOnce(ctx context.Context, src *config.SecretSource, what string) (string, error) {
	if src.File != "" {
		data, err := os.ReadFile(src.File)

		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", errNotYet
		case err != nil:
			return "", fmt.Errorf("reading %s from %s: %w", what, src.File, err)
		}

		secret := strings.TrimSpace(string(data))
		if secret == "" {
			// Created but not yet written: a race worth waiting out.
			return "", errNotYet
		}

		slog.Info("read secret from file", "what", what, "path", src.File)

		return secret, nil
	}

	return fetchSecret(ctx, src, what)
}

func fetchSecret(ctx context.Context, source *config.SecretSource, what string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return "", fmt.Errorf("building request for %s: %w", what, err)
	}

	if source.AuthFile != "" {
		credential, err := os.ReadFile(source.AuthFile)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", source.AuthFile, err)
		}

		request.Header.Set("Authorization",
			"Bearer "+strings.TrimSpace(string(credential)))
	}

	slog.Info("fetching secret", "what", what, "url", source.URL)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		// The endpoint may simply not be listening yet, which is the normal
		// state while the node that publishes the secret is still starting.
		return "", fmt.Errorf("fetching %s: %w: %w", what, errNotYet, err)
	}
	defer func() {
		// Same as above: the body is read in full, so a close failure cannot
		// affect the secret we return.
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		if retryableStatus(response.StatusCode) {
			return "", fmt.Errorf("fetching %s: %w (status %s)",
				what, errNotYet, response.Status)
		}

		// 401, 403, 400 and friends: the request is wrong, not early. Retrying
		// a rejected credential for a quarter of an hour helps nobody and
		// hides the mistake.
		return "", fmt.Errorf("fetching %s: unexpected status %s", what, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSize))
	if err != nil {
		return "", fmt.Errorf("reading %s response: %w", what, err)
	}

	secret := strings.TrimSpace(string(body))
	if secret == "" {
		// A placeholder object created before the real value is written.
		return "", fmt.Errorf("fetching %s from %s: %w", what, source.URL, errNotYet)
	}

	return secret, nil
}

// retryableStatus reports whether a status means "not yet" rather than "no".
//
// The split matters: an absent secret is the expected state while the rest of
// the cluster comes up, but a rejected credential is a configuration error that
// waiting will never fix.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusNotFound,
		http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
