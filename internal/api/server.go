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

	"github.com/Corium-OS/Corium/internal/access"
	"github.com/Corium-OS/Corium/internal/kubeconfig"
	"github.com/Corium-OS/Corium/internal/lifecycle"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
	"github.com/Corium-OS/Corium/internal/token"
	"github.com/Corium-OS/Corium/internal/upgrade"
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

	// upgrades drives bootc, likewise.
	upgrades *upgrade.Manager

	// lifecycle does the things that cannot be undone by doing them again.
	lifecycle *lifecycle.Manager

	// kubeconfig fetches the cluster's administrator credentials.
	kubeconfig *kubeconfig.Manager

	// token mints k0s join tokens, so a controller can hand `cctl worker-config`
	// what a new node needs to join.
	token *token.Manager

	// access manages the SSH keys this node trusts for an existing user. A
	// field for the same reason as the rest: a test writes to its own directory
	// rather than the real one.
	access *access.Manager

	// configPath is where an applied corium: document is written. A field so
	// that a test does not have to write to the real /etc to prove that
	// applying one works.
	configPath string

	// sessionDir is where the per-boot state lives: the enrolment session, and
	// the marker that tells a held bootstrap its operator has answered.
	sessionDir SessionDir

	// restart is closed when something has changed that the listener can only
	// pick up by being rebuilt -- an enrolment, or a rotated CA. Rebuilding
	// TLS underneath a live listener works until the one request that matters
	// arrives mid-swap, so the process stops instead and systemd brings it
	// back.
	restart chan struct{}

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

// Upgrades replaces how the server drives bootc, likewise.
func (s *Server) Upgrades(manager *upgrade.Manager) { s.upgrades = manager }

// Lifecycle replaces how the server acts on the machine, likewise.
func (s *Server) Lifecycle(manager *lifecycle.Manager) { s.lifecycle = manager }

// Kubeconfig replaces how the server asks k0s for credentials, likewise.
func (s *Server) Kubeconfig(manager *kubeconfig.Manager) { s.kubeconfig = manager }

// Tokens replaces how the server mints k0s join tokens, likewise.
func (s *Server) Tokens(manager *token.Manager) { s.token = manager }

// Access replaces where the server keeps the SSH keys it trusts, likewise.
func (s *Server) Access(manager *access.Manager) { s.access = manager }

// NewServer prepares a listener for whichever state the node is in.
//
// how says what an unclaimed node asks of somebody claiming it. It is a
// parameter rather than a setter because the answer decides what the listener
// is, and a server that could be opened after it started would be a server
// nobody could reason about.
func NewServer(store *Store, address string, how Enrolment, session SessionDir) (*Server, error) {
	server := &Server{
		store:      store,
		address:    address,
		restart:    make(chan struct{}),
		ready:      make(chan struct{}),
		inspector:  &nodeinfo.Inspector{},
		systemd:    &systemd.Manager{},
		upgrades:   &upgrade.Manager{},
		lifecycle:  &lifecycle.Manager{},
		kubeconfig: &kubeconfig.Manager{},
		token:      &token.Manager{},
		access:     &access.Manager{},
		configPath: ConfigPath,
		sessionDir: session,
	}

	enrolled, err := store.Enrolled()
	if err != nil {
		return nil, err
	}

	if !enrolled {
		if server.enroller, err = NewEnroller(store, how, session); err != nil {
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

// Restarting returns a channel closed when the process needs to come back up
// for its listener to reflect a change.
func (s *Server) Restarting() <-chan struct{} { return s.restart }

// wantRestart asks for that, at most once.
func (s *Server) wantRestart() {
	select {
	case <-s.restart:
	default:
		close(s.restart)
	}
}

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
	case <-s.restart:
		slog.Info("restarting so the listener picks up what changed")
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

	// Staging pulls an image and changes nothing else, so it sits with the
	// other operator work. Applying takes the node out of service and rollback
	// decides what it comes back as; both are admin.
	mux.HandleFunc("POST /v1/upgrade/stage", require(RoleOperator, s.handleStage))
	mux.HandleFunc("POST /v1/upgrade/apply", require(RoleAdmin, s.handleApply))
	mux.HandleFunc("POST /v1/upgrade/rollback", require(RoleAdmin, s.handleRollback))

	// Cordon and drain take a node out of service and are reversible, which is
	// operator work. Reboot, shutdown and reset are not reversible from here --
	// nothing in this API can power a machine back on -- so they are admin.
	mux.HandleFunc("POST /v1/lifecycle/cordon", require(RoleOperator, s.handleCordon))
	mux.HandleFunc("POST /v1/lifecycle/drain", require(RoleOperator, s.handleDrain))
	mux.HandleFunc("POST /v1/lifecycle/reboot", require(RoleAdmin, s.handleReboot))
	mux.HandleFunc("POST /v1/lifecycle/shutdown", require(RoleAdmin, s.handleShutdown))
	mux.HandleFunc("POST /v1/lifecycle/reset", require(RoleAdmin, s.handleReset))

	// Handing the node to a different CA decides who may do everything above.
	// admin, because this decides what the node becomes -- including which
	// cluster it joins and with what token. It is refused outright once the
	// node has bootstrapped, so the role is the second gate rather than the
	// only one.
	mux.HandleFunc("POST /v1/config", require(RoleAdmin, s.handleApplyConfig))

	mux.HandleFunc("POST /v1/ca/rotate", require(RoleAdmin, s.handleRotateCA))

	// Admin, and not because it changes anything -- it changes nothing. It
	// hands over credentials to the whole cluster, which outranks every other
	// call here: the rest act on one node.
	mux.HandleFunc("GET /v1/kubeconfig", require(RoleAdmin, s.handleKubeconfig))

	// Minting a join token hands out a credential that adds a machine to the
	// cluster -- a worker token lets one join, a controller token is effectively
	// cluster-admin. Admin, and logged, for the same reason kubeconfig is.
	mux.HandleFunc("GET /v1/join-token", require(RoleAdmin, s.handleJoinToken))

	// Trusting an SSH key for a user grants a shell, which steps outside every
	// guard rail the rest of this API keeps -- so admin, and logged by
	// fingerprint. Reading which keys are trusted is not itself a grant, so the
	// list sits at readonly with the other reads. See ADR 5.
	mux.HandleFunc("GET /v1/access/ssh", require(RoleReadOnly, s.handleListSSHKeys))
	mux.HandleFunc("POST /v1/access/ssh", require(RoleAdmin, s.handleAddSSHKey))
	mux.HandleFunc("DELETE /v1/access/ssh", require(RoleAdmin, s.handleRevokeSSHKey))

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

	// Document is an optional corium: configuration, applied as part of the
	// claim. A node held in maintenance mode is released the instant it is
	// claimed, so a configuration meant to be bootstrapped on this boot has to
	// arrive with the claim rather than after it.
	Document string `json:"document,omitempty"`
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

	err = s.enroller.EnrollWith(request.Code, []byte(request.OperatorCA),
		s.applyDocumentDuringEnrolment(r, request.Document))

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

	// Only now: the response has to reach the client before the process goes
	// away underneath it.
	s.wantRestart()
}

// handleNode reports what this machine is.
//
// Read-only, and the endpoint the other surfaces are meant to be used after:
// it is how an operator finds out what a node is before doing anything to it.
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	node := s.inspector.Collect(r.Context())

	// How this node came to be owned is not something the machine can be
	// inspected for -- only the daemon knows -- so it is filled in here.
	// Somebody auditing a fleet is entitled to find the nodes whose ownership
	// was established by whoever reached them first.
	if claim, err := s.store.Claim(); err == nil {
		node.Management = nodeinfo.Management{
			ClaimedBy:       string(claim.Method),
			Unauthenticated: claim.Method != "" && !claim.Authenticated(),
		}
	}

	if s.Unenrolled() && s.enroller.OpenToAnyone() {
		node.Management.OpenEnrolment = true
	}

	writeJSON(w, http.StatusOK, node)
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
