// Package ui serves a dashboard for a set of Corium nodes on the operator's
// own machine.
//
// It is the browser's half of `cctl`, and it lives here rather than on the
// node deliberately. A node answers for itself and opens no port it was not
// asked to (ADR 4); a dashboard over several nodes is by definition a view
// across a fleet, and the only place that view can be assembled without giving
// every machine a second listening port and a second authentication model is
// the operator's own machine. So this is an HTTP server bound to loopback,
// holding the operator's client certificate, speaking to nodes over the same
// mutual-TLS API cctl already uses. Nothing about a node changes because
// somebody opened a browser.
//
// The address of every node it will talk to is fixed when the server is built.
// A dashboard that proxied whatever address a request named would be an
// open relay for the operator's credentials, reachable from any page the
// browser happens to load; see guard.go for the rest of that defence.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Corium-OS/Corium/internal/cctl"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
)

// DefaultListen is where the dashboard binds unless told otherwise.
//
// Loopback, and not a port any part of Corium already answers on: the node API
// is 7443, k0s takes 6443, 9443, 8132 and 8133, and the kubelet 10250.
const DefaultListen = "127.0.0.1:7500"

// fanOutTimeout bounds one refresh of the overview.
//
// The overview asks every node at once and renders what came back, so a
// machine that is powered off costs a slot for this long and not the page.
const fanOutTimeout = 15 * time.Second

// maxLogLines is the most this dashboard will ask a node for at once. The node
// caps it at 10000 regardless; this keeps a page from asking for that.
const maxLogLines = 2000

// Connector dials one node. It is a function so that this package holds no
// opinion about where the operator's credentials live.
type Connector func(address string) (*cctl.Client, error)

// Server is a dashboard for a fixed set of nodes.
type Server struct {
	// nodes is both the list rendered and the allowlist enforced. A request
	// naming anything else is refused rather than dialled.
	nodes []string

	connect Connector

	// token authorises a browser. See guard.go.
	token string

	// listen is what the server was told to bind, used to check the Host
	// header a browser sends back.
	listen string

	mu      sync.Mutex
	clients map[string]*cctl.Client
}

// NewServer builds a dashboard for the given node addresses.
//
// The addresses are taken as given: adding a default port is the caller's job,
// because the caller is the one that knows what an operator typed.
func NewServer(listen string, nodes []string, connect Connector) (*Server, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes to show")
	}

	if connect == nil {
		return nil, errors.New("no way to reach a node")
	}

	token, err := newToken()
	if err != nil {
		return nil, err
	}

	return &Server{
		nodes:   slices.Clone(nodes),
		connect: connect,
		token:   token,
		listen:  listen,
		clients: make(map[string]*cctl.Client),
	}, nil
}

// URL is the address to open in a browser, carrying the token that authorises
// this one session. It is printed once and never logged.
func (s *Server) URL() string {
	host, port, err := net.SplitHostPort(s.listen)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	if err == nil {
		return fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(host, port), s.token)
	}

	return fmt.Sprintf("http://%s/?token=%s", s.listen, s.token)
}

// Serve runs the dashboard until the context is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler: s.Handler(),

		// A browser on the same machine is not a network peer to defend
		// against slow reads, but a header that never ends is cheap to send
		// from anywhere, so that one keeps a bound.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,

		// No write deadline: a drain answers in minutes, and the node's own
		// timeout is the bound that matters. A deadline here would cut the
		// answer off and leave the page unable to say what happened.
		WriteTimeout: 0,
	}

	done := make(chan error, 1)

	go func() { done <- server.Serve(listener) }()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serving the dashboard: %w", err)

	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = server.Shutdown(shutdown)

		return nil
	}
}

// Handler is the whole dashboard, guarded. It is exported for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /static/{file}", s.handleStatic)

	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/nodes/{address}", s.handleNode)
	mux.HandleFunc("GET /api/nodes/{address}/services", s.handleServices)
	mux.HandleFunc("GET /api/nodes/{address}/logs", s.handleLogs)
	mux.HandleFunc("POST /api/nodes/{address}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/nodes/{address}/cordon", s.handleCordon)
	mux.HandleFunc("POST /api/nodes/{address}/drain", s.handleDrain)

	return s.guard(mux)
}

// client returns a client for an address on the allowlist, building one the
// first time.
//
// They are cached because the overview polls: a fresh client per request would
// mean a fresh TLS handshake with every node every few seconds, which is work
// the node does not need to do to answer the same question again.
func (s *Server) client(address string) (*cctl.Client, error) {
	if !slices.Contains(s.nodes, address) {
		return nil, errUnknownNode
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if client, ok := s.clients[address]; ok {
		return client, nil
	}

	client, err := s.connect(address)
	if err != nil {
		return nil, err
	}

	s.clients[address] = client

	return client, nil
}

var errUnknownNode = errors.New("this dashboard was not started for that node")

// Summary is one node's line in the overview.
//
// Error is set instead of Node when the node did not answer. The overview
// renders both: a machine that is down is exactly what somebody opened this
// to find out, and a page that fails whole because one node is unreachable
// would hide the other nine.
type Summary struct {
	Address string         `json:"address"`
	Node    *nodeinfo.Node `json:"node,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), fanOutTimeout)
	defer cancel()

	summaries := make([]Summary, len(s.nodes))

	var wait sync.WaitGroup

	for i, address := range s.nodes {
		wait.Add(1)

		go func() {
			defer wait.Done()

			summaries[i] = s.summarise(ctx, address)
		}()
	}

	wait.Wait()

	writeJSON(w, http.StatusOK, map[string]any{"nodes": summaries})
}

func (s *Server) summarise(ctx context.Context, address string) Summary {
	summary := Summary{Address: address}

	client, err := s.client(address)
	if err != nil {
		summary.Error = err.Error()

		return summary
	}

	node, err := client.Node(ctx)
	if err != nil {
		summary.Error = err.Error()

		return summary
	}

	summary.Node = node

	return summary
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	node, err := client.Node(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	// The role is what this operator's certificate authenticated as, not
	// anything about the node. The page uses it to disable the buttons a
	// certificate cannot press, so that a refusal is visible before it is a
	// failed request.
	body := map[string]any{"address": r.PathValue("address"), "node": node}

	if health, err := client.Health(r.Context()); err == nil {
		body["role"] = health.Role
	}

	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	services, err := client.Services(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	query := cctl.LogQuery{
		Unit:  r.URL.Query().Get("unit"),
		Since: r.URL.Query().Get("since"),
		Lines: 200,
	}

	if raw := r.URL.Query().Get("lines"); raw != "" {
		lines, err := parseLines(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)

			return
		}

		query.Lines = lines
	}

	// Not followed. A page that streams a journal needs a connection held open
	// per tab and a way to stop it; `cctl logs --follow` already does that job
	// in the terminal, and this asks for a window instead.
	records := []systemd.Record{}

	if err := client.Logs(r.Context(), query, func(record systemd.Record) {
		records = append(records, record)
	}); err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	var body struct {
		Unit string `json:"unit"`
	}

	if !decode(w, r, &body) {
		return
	}

	if body.Unit == "" {
		writeError(w, http.StatusBadRequest, errors.New("no unit named"))

		return
	}

	status, err := client.Restart(r.Context(), body.Unit)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"service": status})
}

func (s *Server) handleCordon(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	var body struct {
		Undo bool `json:"undo"`
	}

	if !decode(w, r, &body) {
		return
	}

	if err := client.Cordon(r.Context(), body.Undo); err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	status := "cordoned"
	if body.Undo {
		status = "uncordoned"
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	client, ok := s.resolve(w, r)
	if !ok {
		return
	}

	if err := client.Drain(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "drained"})
}

// resolve turns the address in a route into a client, answering the request
// itself when it cannot.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) (*cctl.Client, bool) {
	client, err := s.client(r.PathValue("address"))
	if err != nil {
		if errors.Is(err, errUnknownNode) {
			writeError(w, http.StatusNotFound, err)

			return nil, false
		}

		writeError(w, http.StatusBadGateway, err)

		return nil, false
	}

	return client, true
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.ContentLength == 0 {
		return true
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("reading the request: %w", err))

		return false
	}

	return true
}

func parseLines(raw string) (int, error) {
	var lines int
	if _, err := fmt.Sscanf(raw, "%d", &lines); err != nil {
		return 0, fmt.Errorf("lines: %w", err)
	}

	if lines < 1 || lines > maxLogLines {
		return 0, fmt.Errorf("lines: want 1 to %d, got %d", maxLogLines, lines)
	}

	return lines, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("writing a dashboard reply", "error", err)
	}
}

// writeError reports a failure to the page.
//
// The message is the one cctl would have printed. Nothing here has seen a
// secret -- no route in this dashboard fetches a kubeconfig or a join token --
// so the error a node gave is the error a person should read.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
