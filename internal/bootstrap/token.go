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

	"github.com/qjoly/corium/internal/config"
)

// tokenFetchTimeout bounds how long first boot waits for a token service.
// Long enough to tolerate a slow network coming up, short enough that a node
// pointed at a dead endpoint fails visibly instead of hanging forever.
const tokenFetchTimeout = 30 * time.Second

// maxTokenSize caps how much is read from a token source. A k0s join token is a
// few kilobytes; anything larger is a misconfiguration or a wrong URL, and
// reading it into memory unbounded would be the wrong response either way.
const maxTokenSize = 256 << 10

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

	source := cfg.Join.TokenFrom

	if source.File != "" {
		data, err := os.ReadFile(source.File)
		if err != nil {
			return "", fmt.Errorf("reading join token from %s: %w", source.File, err)
		}

		slog.Info("read join token from file", "path", source.File)

		return strings.TrimSpace(string(data)), nil
	}

	return fetchToken(ctx, source)
}

func fetchToken(ctx context.Context, source *config.TokenSource) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return "", fmt.Errorf("building token request: %w", err)
	}

	if source.AuthFile != "" {
		credential, err := os.ReadFile(source.AuthFile)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", source.AuthFile, err)
		}

		request.Header.Set("Authorization",
			"Bearer "+strings.TrimSpace(string(credential)))
	}

	slog.Info("fetching join token", "url", source.URL)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetching join token: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching join token: unexpected status %s",
			response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenSize))
	if err != nil {
		return "", fmt.Errorf("reading join token response: %w", err)
	}

	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("fetching join token: empty response from %s", source.URL)
	}

	return token, nil
}
