package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Corium-OS/Corium/internal/config"
)

func TestWaitsForAFileThatAppearsLater(t *testing.T) {
	// The whole point: a node can boot before the thing that mints its token.
	path := filepath.Join(t.TempDir(), "token")

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(path, []byte("the-token\n"), 0o600)
	}()

	secret, err := resolveSecret(context.Background(),
		&config.SecretSource{File: path, WaitFor: "10s"}, "join token")
	if err != nil {
		t.Fatalf("resolveSecret() error = %v, want it to wait", err)
	}

	if secret != "the-token" {
		t.Errorf("resolveSecret() = %q, want the-token", secret)
	}
}

func TestDoesNotWaitWhenNotAskedTo(t *testing.T) {
	// Without waitFor the old behaviour stands: fail immediately.
	_, err := resolveSecret(context.Background(),
		&config.SecretSource{File: "/nonexistent/token"}, "join token")
	if err == nil {
		t.Fatal("resolveSecret() error = nil, want an immediate failure")
	}
}

func TestGivesUpAtTheDeadline(t *testing.T) {
	start := time.Now()

	_, err := resolveSecret(context.Background(),
		&config.SecretSource{File: "/nonexistent/token", WaitFor: "1s"}, "join token")
	if err == nil {
		t.Fatal("resolveSecret() error = nil, want a timeout")
	}

	if !strings.Contains(err.Error(), "did not appear within") {
		t.Errorf("error = %q, want it to say the wait expired", err)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s, well past the 1s budget", elapsed)
	}
}

func TestRejectedCredentialFailsImmediately(t *testing.T) {
	// Retrying a wrong password for the full budget hides the mistake.
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}))
	defer server.Close()

	start := time.Now()

	_, err := resolveSecret(context.Background(),
		&config.SecretSource{URL: server.URL, WaitFor: "30s"}, "join token")
	if err == nil {
		t.Fatal("resolveSecret() error = nil, want a refusal")
	}

	if errors.Is(err, errNotYet) {
		t.Error("a 401 was treated as 'not yet'; it must fail immediately")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("server was called %d times, want exactly 1", got)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; a rejected credential must not be retried", elapsed)
	}
}

func TestWaitsThroughNotFoundThenSucceeds(t *testing.T) {
	// The realistic shape: the secret store answers 404 until the first
	// controller has published the token.
	var calls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) < 3 {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			_, _ = w.Write([]byte("late-token"))
		}))
	defer server.Close()

	secret, err := resolveSecret(context.Background(),
		&config.SecretSource{URL: server.URL, WaitFor: "30s"}, "join token")
	if err != nil {
		t.Fatalf("resolveSecret() error = %v, want it to wait out the 404s", err)
	}

	if secret != "late-token" {
		t.Errorf("resolveSecret() = %q, want late-token", secret)
	}
}

func TestRetryableStatuses(t *testing.T) {
	for code, want := range map[int]bool{
		http.StatusNotFound:            true,
		http.StatusServiceUnavailable:  true,
		http.StatusTooManyRequests:     true,
		http.StatusBadGateway:          true,
		http.StatusUnauthorized:        false,
		http.StatusForbidden:           false,
		http.StatusBadRequest:          false,
		http.StatusInternalServerError: false,
	} {
		if got := retryableStatus(code); got != want {
			t.Errorf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestRetryDelayBacksOffAndSettles(t *testing.T) {
	previous := time.Duration(0)

	for attempt := 1; attempt <= 8; attempt++ {
		delay := retryDelay(attempt)

		if delay < previous {
			t.Errorf("delay shrank at attempt %d: %s after %s", attempt, delay, previous)
		}

		if delay > 32*time.Second {
			t.Errorf("delay at attempt %d is %s; it must settle", attempt, delay)
		}

		previous = delay
	}
}
