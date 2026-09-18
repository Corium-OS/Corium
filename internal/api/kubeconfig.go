package api

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"unicode"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/kubeconfig"
)

// handleKubeconfig hands over the cluster's administrator credentials.
//
// This is the most powerful thing the API will give anybody. Every other call
// acts on one node; this one hands over a cluster. `reset` destroys a machine,
// and this outranks it: whoever holds the file can do anything to every
// workload in the cluster, and nothing here can take it back.
//
// Hence admin, and hence the warning in the journal: an operator should be
// able to find out afterwards that it happened, and when.
func (s *Server) handleKubeconfig(w http.ResponseWriter, r *http.Request) {
	node := s.inspector.Collect(r.Context())

	if !config.Role(node.Role).IsController() {
		writeError(w, http.StatusConflict, kubeconfig.ErrNoControlPlane.Error())

		return
	}

	server := r.URL.Query().Get("server")

	if err := checkServer(server); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	if server == "" {
		server = defaultServer(node.Endpoint, r)
	}

	raw, err := s.kubeconfig.Admin(r.Context(), server)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	slog.Warn("handed over the cluster administrator kubeconfig",
		"server", server, "to", r.RemoteAddr)

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-store")
	// The body is YAML and is never HTML. nosniff says so to anything that
	// would otherwise guess, which is what makes writing it back safe.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	//nolint:gosec // G705: YAML, not markup, and the one caller-supplied field
	// is checked above and then encoded by the YAML marshaller.
	if _, err := w.Write(raw); err != nil {
		slog.Debug("writing the kubeconfig", "error", err)
	}
}

// maxServer is generous for a hostname and short for anything else.
const maxServer = 253

// checkServer refuses a server address that is not one.
//
// The value is the only thing a caller controls here, and it ends up in a file
// somebody's tooling will read forever. It is encoded by the YAML marshaller
// rather than pasted, so this is about catching a mistake early rather than
// about escaping -- but a kubeconfig that silently points somewhere unusable
// is worse than a refusal.
func checkServer(server string) error {
	if server == "" {
		return nil
	}

	if len(server) > maxServer {
		return errors.New("server: too long to be an address")
	}

	if strings.ContainsFunc(server, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return errors.New("server: an address has no whitespace in it")
	}

	return nil
}

// defaultServer decides what the returned kubeconfig should point at.
//
// The endpoint the node recorded at bootstrap wins: on an HA control plane
// that is the virtual IP, and a kubeconfig aimed at one particular controller
// is a kubeconfig that stops working the first time that controller does.
//
// Failing that, the address the client reached this node on -- which is the
// one address the operator has already proved is routable for them, and beats
// guessing between a node's interfaces.
func defaultServer(recorded string, r *http.Request) string {
	if recorded != "" {
		return recorded
	}

	if host, _, err := net.SplitHostPort(r.Host); err == nil && host != "" {
		return host
	}

	if r.Host != "" {
		return r.Host
	}

	// Nothing to go on; k0s's own answer, the node's address, stands.
	return ""
}
