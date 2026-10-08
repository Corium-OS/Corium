package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"os"
)

// errNoRecordedConfig reports a node that has never been handed a document:
// one still waiting in maintenance mode, or one bootstrapped from a source --
// cloud-init, the kernel command line, the image default -- that leaves
// nothing behind on disk for this to read.
//
// Not a 404, which would read like a wrong path. Nothing is wrong with the
// request; the node simply has no configuration of its own to show, the same
// answer shape `POST /v1/upgrade/apply` gives when nothing is staged.
var errNoRecordedConfig = errors.New(
	"this node has no recorded configuration; nothing has been applied to it, " +
		"and what it bootstrapped with came from a source it does not keep")

// configSource names which of the two files on disk a document came from.
//
// The distinction is the entire reason this endpoint reports it rather than
// just handing over bytes: /var/lib/corium/applied.yaml is what the node acted
// on, and /etc/corium/config.yaml is what somebody wrote. On a node where they
// disagree, only the first describes what is running. See ADR 8.
const (
	// configSourceApplied is the baseline the node recorded applying.
	configSourceApplied = "applied"

	// configSourceEtc is the machine-local file, reported only on a node that
	// has no recorded baseline -- one that predates day-two apply, or whose
	// state was cleared.
	configSourceEtc = "etc"
)

// recordedConfig is a node's own answer to "what are you configured as".
type recordedConfig struct {
	// document is the YAML verbatim, as the node stored it. Not re-rendered
	// from a parsed Config: a round trip through the schema would fill in
	// defaults the operator never wrote and silently drop anything a newer
	// release understands and this one does not.
	document []byte

	// source is configSourceApplied or configSourceEtc.
	source string

	// path is where document was read from, so an operator reading the reply
	// on one node and `cat` on another is looking at the same thing.
	path string

	// localEdit reports a /etc/corium/config.yaml that no longer matches the
	// baseline -- a hand edit that has not been applied and is not running.
	// Only meaningful when source is configSourceApplied, because otherwise
	// the file in /etc is the thing being reported.
	localEdit bool
}

// handleShowConfig returns the configuration document this node is reasoning
// from.
//
// It is the read half of `POST /v1/config`, and it exists because the write
// half had none: a node recorded what it applied, diffed the next apply
// against that record, and offered no way to ask what the record said. An
// operator arriving at a running fleet could change a node's configuration but
// not read it, which made every apply a guess.
//
// Admin, for what the document may carry rather than for what reading it does.
// A corium: block can hold an inline join token, a VRRP authPass or a
// WireGuard private key, and `k0s.patch` is an escape hatch whose contents
// Corium only checks is valid YAML. That puts this at the rung where
// `GET /v1/join-token` already sits, so it hands an admin nothing they could
// not already mint -- and keeps it away from the readonly holder the ladder
// places below that.
func (s *Server) handleShowConfig(w http.ResponseWriter, r *http.Request) {
	recorded, err := s.recordedConfig()
	if err != nil {
		if errors.Is(err, errNoRecordedConfig) {
			writeError(w, http.StatusConflict, err.Error())

			return
		}

		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	// Info rather than the Warn that `GET /v1/kubeconfig` and
	// `GET /v1/join-token` carry. Those mint or hand over a credential to a
	// cluster; this returns the node's own description of itself to somebody
	// who already holds the role that could rewrite it.
	slog.Info("returned the recorded configuration",
		"source", recorded.source, "path", recorded.path, "to", r.RemoteAddr)

	writeJSON(w, http.StatusOK, map[string]any{
		"document":  string(recorded.document),
		"source":    recorded.source,
		"path":      recorded.path,
		"localEdit": recorded.localEdit,
	})
}

// recordedConfig reads the document the node is reasoning from, preferring
// what it recorded applying over the file in /etc.
//
// The order is baselineConfig's, and deliberately the same one: an endpoint
// that reported a different document from the one the next apply will diff
// against would be worse than no endpoint at all.
func (s *Server) recordedConfig() (recordedConfig, error) {
	applied, appliedErr := os.ReadFile(s.appliedPath) //nolint:gosec // G304: a server-owned path, not caller input
	local, localErr := os.ReadFile(s.configPath)      //nolint:gosec // G304: as above

	switch {
	case appliedErr == nil:
		return recordedConfig{
			document:  applied,
			source:    configSourceApplied,
			path:      s.appliedPath,
			localEdit: localErr == nil && !sameDocument(applied, local),
		}, nil
	case localErr == nil:
		return recordedConfig{
			document: local,
			source:   configSourceEtc,
			path:     s.configPath,
		}, nil
	default:
		return recordedConfig{}, errNoRecordedConfig
	}
}

// sameDocument reports whether two stored documents say the same thing.
//
// Compared byte for byte but for surrounding whitespace, because both files
// are written from the same bytes by writeConfigDocument -- so anything beyond
// a trailing newline really is an edit, and flagging one would be the kind of
// false alarm that teaches an operator to ignore the field.
func sameDocument(a, b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}
