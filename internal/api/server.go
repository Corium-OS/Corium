package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
)

// DefaultPort is where corium-apid listens.
//
// It is clear of everything k0s binds -- 6443, 9443, 8132, 8133 -- and of the
// kubelet's 10250, so that a node running a control plane has no port to
// argue about.
const DefaultPort = 7443

// Role is what a client certificate is allowed to do, carried in the
// certificate's organisation.
type Role string

const (
	// RoleReadOnly reaches node state and journals.
	RoleReadOnly Role = "corium:readonly"

	// RoleOperator adds services, upgrades, cordon and drain.
	RoleOperator Role = "corium:operator"

	// RoleAdmin adds reboot, shutdown, reset and CA rotation.
	RoleAdmin Role = "corium:admin"
)

// maxEnrolBody caps what an unauthenticated caller can make the node read. A
// CA certificate is a couple of kilobytes; anything larger is a mistake or an
// attempt to spend the node's memory, and both deserve the same answer.
const maxEnrolBody = 64 << 10

// Timeouts. An unauthenticated listener with no read timeout is a listener
// that can be held open by anybody who can reach it.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownGrace     = 5 * time.Second
)

// Server is the node's management listener.
//
// It has two shapes and never both at once. Unenrolled, it serves one
// unauthenticated route and nothing else. Enrolled, it requires a client
// certificate signed by the operator CA and does not serve enrolment at all.
type Server struct {
	store    *Store
	enroller *Enroller
	address  string

	// inspector reads the machine. It is a field so that a test can serve a
	// constructed node rather than the one it happens to be running on.
	inspector *nodeinfo.Inspector

	// systemd is how services and journals are reached, for the same reason.
	systemd *systemd.Manager

	// claimed is closed when an enrolment succeeds, so that the process can
	// come back up in its other shape rather than rebuilding TLS underneath a
	// live listener.
	claimed chan struct{}

	// ready is closed once the listener is up and bound, at which point the
	// address below is final. Closing the channel publishes it.
	ready chan struct{}
	bound string
}

// Inspect replaces where the server reads the machine's state from. It exists
// for tests; a real node is inspected as itself.
func (s *Server) Inspect(inspector *nodeinfo.Inspector) { s.inspector = inspector }

// Supervise replaces how the server reaches systemd, for the same reason.
func (s *Server) Supervise(manager *systemd.Manager) { s.systemd = manager }

// NewServer prepares a listener for whichever state the node is in.
func NewServer(store *Store, address string) (*Server, error) {
	server := &Server{
		store:     store,
		address:   address,
		claimed:   make(chan struct{}),
		ready:     make(chan struct{}),
		inspector: &nodeinfo.Inspector{},
		systemd:   &systemd.Manager{},
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		return nil, err
	}

	if !enrolled {
		if server.enroller, err = NewEnroller(store); err != nil {
			return nil, err
		}
	}

	return server, nil
}

// Unenrolled reports whether this server is waiting to be claimed.
func (s *Server) Unenrolled() bool { return s.enroller != nil }

// PairingCode is the code this node is showing on its console, or empty on a
// node that has already been claimed.
//
// It is exported for the same reason it is printed: an operator needs it, and
// so does a test standing in for one. It is never returned over the API, which
// would defeat the point of it.
func (s *Server) PairingCode() string {
	if s.enroller == nil {
		return ""
	}

	return s.enroller.Code()
}

// Claimed returns a channel closed when an operator enrols the node.
func (s *Server) Claimed() <-chan struct{} { return s.claimed }

// Ready returns a channel closed once the listener is bound.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Addr is the address actually bound, which differs from the one asked for
// whenever the port was left to the kernel. It is only meaningful after Ready
// has closed, which is what publishes it.
func (s *Server) Addr() string { return s.bound }

// Serve listens until the context is cancelled, or until the node is claimed.
//
// Enrolment ends the process rather than reconfiguring it in place. Swapping a
// TLS configuration under a live listener is the kind of thing that works
// until the one request that matters arrives mid-swap; letting systemd restart
// a process that now finds a pinned CA on disk is the same outcome with none
// of the doubt.
func (s *Server) Serve(ctx context.Context) error {
	identity, err := s.store.Identity()
	if err != nil {
		return err
	}

	tlsConfig, err := s.tlsConfig(identity)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              s.address,
		Handler:           s.routes(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.address, err)
	}

	s.bound = listener.Addr().String()
	close(s.ready)

	s.announce(identity, s.bound)

	failed := make(chan error, 1)

	go func() {
		err := server.ServeTLS(listener, "", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		failed <- err
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	case <-s.claimed:
		slog.Info("enrolled, restarting to serve the authenticated API")
	}

	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	return server.Shutdown(stop)
}

// tlsConfig is where the two shapes actually differ.
func (s *Server) tlsConfig(identity tls.Certificate) (*tls.Config, error) {
	config := &tls.Config{
		Certificates: []tls.Certificate{identity},
		MinVersion:   tls.VersionTLS13,
	}

	if s.Unenrolled() {
		// No client certificate is required, because the client does not have
		// one yet -- that is the entire reason this state exists. What stands
		// in for it is the pairing code, checked by the one route served here.
		return config, nil
	}

	pool, err := s.store.ClientCAs()
	if err != nil {
		return nil, err
	}

	config.ClientCAs = pool
	config.ClientAuth = tls.RequireAndVerifyClientCert

	return config, nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	if s.Unenrolled() {
		mux.HandleFunc("POST /v1/enroll", s.handleEnrol)

		// Anything else, on an unclaimed node, gets the same answer: there is
		// nothing here yet. Saying so plainly beats a 404 that reads like a
		// wrong path.
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusConflict,
				"node is not enrolled; claim it with `cctl enroll` first")
		})

		return logRequests(mux)
	}

	// Every route names the lowest role that may call it. Health is the one
	// exception at readonly: it exists so an operator can find out whether
	// their certificate works at all, and refusing to answer that would make
	// diagnosing a bad certificate harder than it needs to be.
	mux.HandleFunc("GET /v1/health", require(RoleReadOnly, s.handleHealth))
	mux.HandleFunc("GET /v1/node", require(RoleReadOnly, s.handleNode))
	mux.HandleFunc("GET /v1/services", require(RoleReadOnly, s.handleServices))
	mux.HandleFunc("GET /v1/logs", require(RoleReadOnly, s.handleLogs))

	// Restarting k0s takes a node out of service for as long as it takes to
	// come back, which is an operator's call and not a reader's.
	mux.HandleFunc("POST /v1/services/{unit}/restart", require(RoleOperator, s.handleRestart))

	// Enrolment is not merely unnecessary on a claimed node, it is refused,
	// and the refusal is explicit so that a second claimant learns nothing
	// from the shape of the answer.
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusConflict, ErrAlreadyEnrolled.Error())
	})

	return logRequests(mux)
}

// enrolRequest is what `cctl enroll` sends.
type enrolRequest struct {
	Code       string `json:"code"`
	OperatorCA string `json:"operatorCA"`
}

type enrolResponse struct {
	Enrolled   bool   `json:"enrolled"`
	OperatorCA string `json:"operatorCA"`
}

func (s *Server) handleEnrol(w http.ResponseWriter, r *http.Request) {
	var request enrolRequest

	body, err := io.ReadAll(io.LimitReader(r.Body, maxEnrolBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")

		return
	}

	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")

		return
	}

	err = s.enroller.Enroll(request.Code, []byte(request.OperatorCA))

	switch {
	case err == nil:

	case errors.Is(err, ErrWrongCode):
		// 401, not 403: the credential was wrong, and the caller may try
		// again with the attempts they have left.
		writeError(w, http.StatusUnauthorized, err.Error())

		return

	case errors.Is(err, ErrLockedOut), errors.Is(err, ErrAlreadyEnrolled):
		writeError(w, http.StatusConflict, err.Error())

		return

	default:
		// A rejected certificate. The message says what was wrong with it and
		// never echoes it back.
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	certificate, err := s.store.OperatorCA()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enrolled, but the CA could not be read back")

		return
	}

	writeJSON(w, http.StatusOK, enrolResponse{
		Enrolled:   true,
		OperatorCA: Fingerprint(certificate.Raw),
	})

	// Only now, and only once: the response has to reach the client before the
	// process goes away underneath it.
	s.finishClaim()
}

func (s *Server) finishClaim() {
	select {
	case <-s.claimed:
	default:
		close(s.claimed)
	}
}

// handleNode reports what this machine is.
//
// Read-only, and the endpoint the other surfaces are meant to be used after:
// it is how an operator finds out what a node is before doing anything to it.
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.inspector.Collect(r.Context()))
}

type healthResponse struct {
	Status string `json:"status"`
	Role   Role   `json:"role"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Role: roleOf(r)})
}

// roleOf reads the role from the verified client certificate.
//
// TLS has already checked that the certificate chains to the operator CA, so
// the organisation can be trusted as far as the CA that issued it -- which is
// exactly as far as it is meant to be trusted.
func roleOf(r *http.Request) Role {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}

	return roleFromCertificate(r.TLS.PeerCertificates[0])
}

func roleFromCertificate(certificate *x509.Certificate) Role {
	// Most privileged wins, so that a certificate carrying several roles is
	// not silently downgraded by the order somebody listed them in.
	for _, role := range []Role{RoleAdmin, RoleOperator, RoleReadOnly} {
		for _, organisation := range certificate.Subject.Organization {
			if strings.EqualFold(organisation, string(role)) {
				return role
			}
		}
	}

	return ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// The response is already committed by WriteHeader, so a failed write can
	// only be logged.
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("writing response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The remote address and the route, never the body: an enrolment body
		// carries a pairing code.
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "from", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
