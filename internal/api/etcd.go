package api

import (
	"net/http"

	"github.com/Corium-OS/Corium/internal/config"
	"github.com/Corium-OS/Corium/internal/etcd"
)

// handleEtcd reports this controller's own etcd member and the membership it
// sees. Whether the cluster can lose a member is cctl's call, from every
// controller's answer.
func (s *Server) handleEtcd(w http.ResponseWriter, r *http.Request) {
	node := s.inspector.Collect(r.Context())

	if !config.Role(node.Role).IsController() {
		writeError(w, http.StatusConflict, etcd.ErrNoControlPlane.Error())

		return
	}

	status, err := s.etcd.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, status)
}
