package token

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCreateBuildsTheK0sCommandAndTrimsTheToken(t *testing.T) {
	var (
		gotName string
		gotArgs []string
	)

	manager := &Manager{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args

		// k0s prints the token with a trailing newline; Create must strip it so
		// the value can go inline into a node's join.token.
		return []byte("a-join-token\n"), nil
	}}

	raw, err := manager.Create(context.Background(), "worker", "1h")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if gotName != "k0s" {
		t.Errorf("ran %q, want k0s", gotName)
	}

	want := []string{"token", "create", "--role=worker", "--expiry=1h"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}

	if string(raw) != "a-join-token" {
		t.Errorf("token = %q, want it trimmed", raw)
	}
}

func TestCreateNamesTheRoleWhenK0sFails(t *testing.T) {
	manager := &Manager{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("k0s is not running")
	}}

	_, err := manager.Create(context.Background(), "controller", "2h")
	if err == nil {
		t.Fatal("Create() error = nil, want one")
	}

	// The error an operator reads should say which token could not be minted.
	if !strings.Contains(err.Error(), "controller") {
		t.Errorf("error = %q, want it to name the role", err)
	}
}
