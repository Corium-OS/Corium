package bootstrap

import (
	"context"
	"strings"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/secret"
)

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

	return secret.Resolve(ctx, cfg.HA.AuthPassFrom, "VRRP password")
}

// resolveToken produces the join token for this node, if it needs one.
func resolveToken(ctx context.Context, cfg *config.Config) (string, error) {
	switch {
	case cfg.Join.Token != "":
		return strings.TrimSpace(cfg.Join.Token), nil
	case cfg.Join.TokenFrom == nil:
		return "", nil
	}

	return secret.Resolve(ctx, cfg.Join.TokenFrom, "join token")
}
