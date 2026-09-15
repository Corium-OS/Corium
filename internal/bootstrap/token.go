package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

// secretFetchTimeout bounds how long first boot waits for a token service.
// Long enough to tolerate a slow network coming up, short enough that a node
// pointed at a dead endpoint fails visibly instead of hanging forever.
const secretFetchTimeout = 30 * time.Second

// maxSecretSize caps how much is read from a token source. A k0s join token is a
// few kilobytes; anything larger is a misconfiguration or a wrong URL, and
// reading it into memory unbounded would be the wrong response either way.
const maxSecretSize = 256 << 10

// resolveAuthPass produces the VRRP password shared between controllers.
//
// It is resolved the same way as a join token and for the same reason: a shared
// secret in instance metadata is readable by anything that can reach the
// metadata service.
func resolveAuthPass(ctx context.Context, cfg *config.Config) (string, error) {
	switch {
	case cfg.HA.AuthPass != "":
		return cfg.HA.AuthPass, nil
	case cfg.HA.AuthPassFrom == nil:
		return "", nil
	}

	secret, err := resolveSecret(ctx, cfg.HA.AuthPassFrom, "VRRP password")
	if err != nil {
		return "", err
	}

	return secret, nil
}

// resolveToken produces the join token for this node, if it needs one.
//
// Errors deliberately avoid quoting any response body: the value being handled
// is a credential, and a failure message that echoes it into the journal has
// leaked it to every reader of that journal.
func resolveToken(ctx context.Context, cfg *config.Config) (string, error) {
	switch {
	case cfg.Join.Token != "":
		return strings.TrimSpace(cfg.Join.Token), nil
	case cfg.Join.TokenFrom == nil:
		return "", nil
	}

	return resolveSecret(ctx, cfg.Join.TokenFrom, "join token")
}

// resolveSecret reads a secret from a file or fetches it over HTTPS.
func resolveSecret(ctx context.Context, src *config.SecretSource, what string) (string, error) {
	if src.File != "" {
		data, err := os.ReadFile(src.File)
		if err != nil {
			return "", fmt.Errorf("reading %s from %s: %w", what, src.File, err)
		}

		slog.Info("read secret from file", "what", what, "path", src.File)

		return strings.TrimSpace(string(data)), nil
	}

	return fetchSecret(ctx, src, what)
}

func fetchSecret(ctx context.Context, source *config.SecretSource, what string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, secretFetchTimeout)
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
		return "", fmt.Errorf("fetching %s: %w", what, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: unexpected status %s", what, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSecretSize))
	if err != nil {
		return "", fmt.Errorf("reading %s response: %w", what, err)
	}

	secret := strings.TrimSpace(string(body))
	if secret == "" {
		return "", fmt.Errorf("fetching %s: empty response from %s", what, source.URL)
	}

	return secret, nil
}
