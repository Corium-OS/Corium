package cctl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/access"
	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
	"github.com/Corium-OS/Corium/internal/upgrade"
)

// requestTimeout bounds the calls that answer at once -- node state, health, a
// service restart. It is deliberately short.
//
// It is not for every route, though an earlier version of this comment claimed
// it was. The routes whose work is measured in minutes do not use it: staging
// streams and sets no deadline of its own (the node's pullTimeout is the
// bound), and draining and resetting take slowRequestTimeout below. The old
// invariant -- "every route answers immediately or not at all" -- was already
// wrong the day drain shipped, and staging a half-gigabyte image made it wrong
// in a way an operator could watch happen: a 30-second deadline here cancelled
// the request, and cancelling the request cancelled the pull it was driving.
const requestTimeout = 30 * time.Second

// slowRequestTimeout is a ceiling for the routes whose work is minutes, not
// seconds: draining a node, and the drain a reset does first. It sits above the
// node's own DefaultDrainTimeout so that the node is the one that decides when
// to give up; this only stops a wedged connection from hanging cctl for ever.
// Staging is absent from this list on purpose -- it streams, so it has no
// ceiling at all and is bounded by the node.
const slowRequestTimeout = 15 * time.Minute

// maxResponse caps what a node can make this tool read.
const maxResponse = 1 << 20

// ErrFingerprintUnknown reports an endpoint nobody has vouched for yet.
var ErrFingerprintUnknown = errors.New("no fingerprint known")

// Client talks to one node.
type Client struct {
	address string
	http    *http.Client

	// seen is the fingerprint the node actually presented, recorded whether or
	// not it was the expected one, so a caller can show it to a human.
	seen string

	// offered records that a client certificate was loaded, which is what
	// makes "the node wanted one" diagnosable rather than merely true.
	offered bool
}

// Dial prepares a client for an address.
//
// The expected fingerprint is how a node is identified. A node signs its own
// certificate — the scheme carries no private key in any configuration, so
// there is nothing to sign it with — which means a name proves nothing and the
// fingerprint proves everything. Passing an empty one accepts whatever answers
// and records it, which is only appropriate while reading the console.
func Dial(address, expected string, clientCertificates ...tls.Certificate) *Client {
	client := &Client{address: address, offered: len(clientCertificates) > 0}

	client.http = &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: clientCertificates,
				MinVersion:   tls.VersionTLS13,
				// Verification is done below, against a fingerprint, because
				// there is no chain to validate and no name worth trusting: a
				// node signs its own certificate.
				InsecureSkipVerify: true, //nolint:gosec // pinned in VerifyConnection
				// VerifyConnection rather than VerifyPeerCertificate, because
				// the latter is not called again when a TLS session is
				// resumed -- so a pin enforced there is a pin an attacker can
				// step around by resuming.
				VerifyConnection: func(state tls.ConnectionState) error {
					return client.pin(state, expected)
				},
			},
		},
	}

	return client
}

// pin records what the node presented and checks it against what was expected.
func (c *Client) pin(state tls.ConnectionState, expected string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("the node presented no certificate")
	}

	c.seen = api.Fingerprint(state.PeerCertificates[0].Raw)

	if expected == "" || c.seen == expected {
		return nil
	}

	return fmt.Errorf(
		"the node presented %s, but %s was expected -- either this is not the "+
			"machine you meant, or something is answering for it", c.seen, expected)
}

// Fingerprint is what the node presented on the last connection.
func (c *Client) Fingerprint() string { return c.seen }

// EnrolResult is what a node says when it has been claimed.
type EnrolResult struct {
	Enrolled bool `json:"enrolled"`

	// OperatorCA is the fingerprint of the CA the node pinned, echoed back so
	// the operator can confirm the node kept what they sent.
	OperatorCA string `json:"operatorCA"`
}

// Enrol claims a node, optionally handing it the configuration it should
// bootstrap with.
//
// The document travels with the claim rather than after it because claiming a
// node in maintenance mode is what releases its bootstrap. Sending it
// separately a moment later is a race against a machine that has already
// started becoming something.
func (c *Client) Enrol(
	ctx context.Context, code string, operatorCA, document []byte,
) (*EnrolResult, error) {
	body, err := json.Marshal(map[string]string{
		"code":       code,
		"operatorCA": string(operatorCA),
		"document":   string(document),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var result EnrolResult

	if err := c.call(ctx, http.MethodPost, "/v1/enroll", body, &result); err != nil {
		if code == "" && strings.Contains(err.Error(), "pairing code") {
			// The node wants a code and none was sent. Saying so beats
			// repeating "pairing code is not correct" at somebody who did not
			// give one.
			return nil, fmt.Errorf("%w -- this node requires the pairing code "+
				"printed on its console; pass --code", err)
		}

		return nil, err
	}

	return &result, nil
}

// ConfigResult reports what a node did with a document it was sent.
type ConfigResult struct {
	Status string `json:"status"`

	// Path is where the node wrote it, echoed back so an operator can see
	// which source in the chain now holds their document.
	Path string `json:"path"`

	// Role is what the node read out of it. This is the field worth checking:
	// it is the node's own reading of the document, not the client's.
	Role string `json:"role"`

	// API reports whether the node will still run this API once it has
	// bootstrapped with that document. A false here means the next boot takes
	// the management API away.
	API bool `json:"api"`
}

// ApplyConfig sends a node the corium: document it should bootstrap with.
//
// It is refused by a node that has already bootstrapped, which is the whole
// rule: see ADR 4.
func (c *Client) ApplyConfig(ctx context.Context, document []byte) (*ConfigResult, error) {
	body, err := json.Marshal(map[string]string{"document": string(document)})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var result ConfigResult
	if err := c.call(ctx, http.MethodPost, "/v1/config", body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// Health is the authenticated round trip: it proves the certificate works and
// says what the node authenticated it as.
type Health struct {
	Status string   `json:"status"`
	Role   api.Role `json:"role"`
}

// Health asks a claimed node how it is.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var health Health
	if err := c.call(ctx, http.MethodGet, "/v1/health", nil, &health); err != nil {
		return nil, err
	}

	return &health, nil
}

// Node asks a claimed node what it is.
//
// The reply is nodeinfo.Node, decoded here rather than restated: a second
// definition of the same shape is a second thing to keep in step, and this one
// would drift the first time a field was added.
func (c *Client) Node(ctx context.Context) (*nodeinfo.Node, error) {
	var node nodeinfo.Node
	if err := c.call(ctx, http.MethodGet, "/v1/node", nil, &node); err != nil {
		return nil, err
	}

	return &node, nil
}

// withTimeout returns a copy of this client's HTTP client with a different
// deadline, so a route whose work outlasts requestTimeout can be given the one
// it needs without changing the client every other route shares. A copy rather
// than a mutation for exactly that reason: the routes that answer at once keep
// their short deadline.
func (c *Client) withTimeout(d time.Duration) *http.Client {
	clone := *c.http
	clone.Timeout = d

	return &clone
}

// call makes one request on the default (short) deadline and turns a node's
// error into this tool's error.
func (c *Client) call(ctx context.Context, method, path string, body []byte, into any) error {
	return c.callUsing(ctx, c.http, method, path, body, into)
}

// callUsing is call, over a caller-chosen HTTP client. It exists so the routes
// whose work is measured in minutes can carry a longer deadline than the ones
// that answer at once.
func (c *Client) callUsing(
	ctx context.Context, httpClient *http.Client, method, path string, body []byte, into any,
) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, "https://"+c.address+path, reader)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return c.reach(err)
	}

	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("reading the response: %w", err)
	}

	// 202 is what a node returns when it has accepted a job it will not be
	// around to finish -- applying an upgrade reboots it.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return c.explain(response.Status, raw)
	}

	if into == nil {
		return nil
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("parsing the response: %w", err)
	}

	return nil
}

// reach turns a connection failure into something worth reading.
//
// One of these is worth singling out. "remote error: tls: certificate
// required" means the node asked for a client certificate and got none -- and
// Go sends none, silently, when the certificate it holds was signed by a CA
// the server did not name as acceptable. So the most likely cause is not a
// missing certificate at all: it is a certificate signed by a CA this node no
// longer trusts, which is what happens to every other operator directory after
// somebody rotates it.
//
// The raw alert reads like the client sent nothing, which sends people looking
// in the wrong place.
func (c *Client) reach(err error) error {
	if c.offered && strings.Contains(err.Error(), "certificate required") {
		return fmt.Errorf("%s does not accept your certificate: it is signed by a CA "+
			"this node no longer trusts. If its CA was rotated, use the directory it "+
			"was rotated to; otherwise the node was claimed by somebody else and the "+
			"way back is `corium-agent api set-ca` on its console", c.address)
	}

	return fmt.Errorf("contacting %s: %w", c.address, err)
}

// failure reads an error response that has not been consumed yet.
func (c *Client) failure(response *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("%s from %s", response.Status, c.address)
	}

	return c.explain(response.Status, raw)
}

// explain turns a node's refusal into this tool's error.
//
// The node's own words are used where it gave any: it knows why it said no,
// and rewording it here would only lose detail.
func (c *Client) explain(status string, body []byte) error {
	var failure struct {
		Error string `json:"error"`
	}

	if err := json.Unmarshal(body, &failure); err == nil && failure.Error != "" {
		return fmt.Errorf("%s: %s", status, failure.Error)
	}

	return fmt.Errorf("%s from %s", status, c.address)
}

// ClientCertificate loads the operator's own certificate, if it has one.
//
// A missing certificate is not an error here: enrolment happens before one is
// ever presented, and reporting "no client certificate" as a failure would
// make the first command anybody runs look broken.
func ClientCertificate(store *Store) ([]tls.Certificate, error) {
	if !store.Exists(ClientCertFile) || !store.Exists(ClientKeyFile) {
		return nil, nil
	}

	pair, err := tls.LoadX509KeyPair(store.Path(ClientCertFile), store.Path(ClientKeyFile))
	if err != nil {
		return nil, fmt.Errorf("loading your client certificate: %w", err)
	}

	return []tls.Certificate{pair}, nil
}

// OperatorCA reads the certificate that gets sent to a node.
func OperatorCA(store *Store) ([]byte, error) {
	data, err := os.ReadFile(store.Path(CACertFile))
	if err != nil {
		return nil, fmt.Errorf("reading the operator CA: %w (run `cctl pki init` first)", err)
	}

	return data, nil
}

// Services lists the units the node will talk about.
func (c *Client) Services(ctx context.Context) ([]systemd.Status, error) {
	var reply struct {
		Services []systemd.Status `json:"services"`
	}

	if err := c.call(ctx, http.MethodGet, "/v1/services", nil, &reply); err != nil {
		return nil, err
	}

	return reply.Services, nil
}

// Restart cycles a unit and reports what state it landed in.
func (c *Client) Restart(ctx context.Context, unit string) (*systemd.Status, error) {
	var status systemd.Status

	path := "/v1/services/" + url.PathEscape(unit) + "/restart"
	if err := c.call(ctx, http.MethodPost, path, nil, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// Logs streams a node's journal, calling onRecord for each entry as it arrives.
//
// It does not collect the records first. The point of following a log is to see
// a line before the request ends, and buffering would defeat that at exactly
// the moment somebody is watching a node come back.
func (c *Client) Logs(ctx context.Context, options LogQuery, onRecord func(systemd.Record)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+c.address+"/v1/logs?"+options.values().Encode(), nil)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	// A followed stream has no length and no deadline of its own; the node
	// bounds it, and ^C ends it.
	response, err := c.withTimeout(0).Do(request)
	if err != nil {
		return c.reach(err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return c.failure(response)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponse)

	for scanner.Scan() {
		var record systemd.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			// A line this version does not understand is skipped rather than
			// fatal: a newer node may send a field this client predates.
			continue
		}

		onRecord(record)
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("reading the stream: %w", err)
	}

	return nil
}

// LogQuery is one request for a node's journal.
type LogQuery struct {
	Unit   string
	Lines  int
	Since  string
	Follow bool
}

func (q LogQuery) values() url.Values {
	values := url.Values{}

	if q.Unit != "" {
		values.Set("unit", q.Unit)
	}

	if q.Lines > 0 {
		values.Set("lines", strconv.Itoa(q.Lines))
	}

	if q.Since != "" {
		values.Set("since", q.Since)
	}

	if q.Follow {
		values.Set("follow", "true")
	}

	return values
}

// Stage asks a node to pull an image and prepare to boot it, reporting bootc's
// progress through onProgress as it arrives.
//
// It streams rather than waiting for a single answer, because the answer is
// minutes away: the node is pulling hundreds of megabytes. Two things fall out
// of that. There is no client deadline -- the node's own pullTimeout is the
// bound, exactly as a followed log is bounded by the node rather than by this
// clock -- and the outcome is the last record in the stream, not a status code,
// because the node committed to a 200 the moment it began pulling. The refusals
// that happen before the pull starts still arrive as a status code, and are
// turned into an error the same way every other call's are.
//
// onProgress may be nil, for a caller that wants the result and not the noise.
func (c *Client) Stage(
	ctx context.Context, image string, onProgress func(string),
) (*upgrade.Staged, error) {
	body, err := json.Marshal(map[string]string{"image": image})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+c.address+"/v1/upgrade/stage", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := c.withTimeout(0).Do(request)
	if err != nil {
		return nil, c.reach(err)
	}

	defer func() { _ = response.Body.Close() }()

	// A refusal before the pull began: a bad reference, or an image the policy
	// rejects. It arrives as a status code with a JSON error, the same as any
	// other call, because the node had not committed to the stream yet.
	if response.StatusCode != http.StatusOK {
		return nil, c.failure(response)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponse)

	var staged *upgrade.Staged

	for scanner.Scan() {
		var event struct {
			Progress string          `json:"progress"`
			Staged   *upgrade.Staged `json:"staged"`
			Error    string          `json:"error"`
		}

		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// A line this version does not understand is skipped rather than
			// fatal, as in Logs: a newer node may add a field this client
			// predates.
			continue
		}

		switch {
		case event.Error != "":
			// The node's own words for what went wrong; see explain.
			return nil, errors.New(event.Error)
		case event.Staged != nil:
			staged = event.Staged
		case event.Progress != "" && onProgress != nil:
			onProgress(event.Progress)
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("reading the stream: %w", err)
	}

	if staged == nil {
		// The stream ended without saying what was staged or why not. Rare, and
		// worth naming rather than returning a nil that reads like success.
		return nil, errors.New("the node stopped staging without saying how it ended")
	}

	return staged, nil
}

// Apply tells a node to drain and reboot into what it has staged.
//
// It returns as soon as the node has accepted: the machine is about to go
// away, and waiting for it to finish would mean waiting for this connection to
// be cut.
func (c *Client) Apply(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/upgrade/apply", []byte("{}"), nil)
}

// Rollback marks a node's previous image as the one to boot next. It does not
// reboot.
func (c *Client) Rollback(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/upgrade/rollback", []byte("{}"), nil)
}

// Cordon takes a node out of scheduling, or puts it back.
func (c *Client) Cordon(ctx context.Context, undo bool) error {
	path := "/v1/lifecycle/cordon"
	if undo {
		path += "?undo=true"
	}

	return c.call(ctx, http.MethodPost, path, []byte("{}"), nil)
}

// Drain evicts a node's workloads, cordoning it first.
//
// Evicting pods can legitimately take minutes -- a pod disruption budget makes
// the node wait its turn -- and the node bounds it with DefaultDrainTimeout. So
// this gets slowRequestTimeout rather than the short deadline: the 30 seconds
// meant for calls that answer at once would not only cut a real drain off, it
// would cancel it, the same way it did to staging.
func (c *Client) Drain(ctx context.Context) error {
	return c.callUsing(ctx, c.withTimeout(slowRequestTimeout),
		http.MethodPost, "/v1/lifecycle/drain", []byte("{}"), nil)
}

// Reboot restarts a node. It returns once the node has accepted.
func (c *Client) Reboot(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/lifecycle/reboot", []byte("{}"), nil)
}

// Shutdown powers a node off. Nothing in this API can turn it back on.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/lifecycle/shutdown", []byte("{}"), nil)
}

// Reset erases a node: it leaves its cluster, forgets its owner, and reboots
// unclaimed.
//
// The node's own name has to be sent back to it. That is the node's rule
// rather than this client's, and it is here because an address in a shell's
// history is a poor guard against this landing on the wrong machine.
func (c *Client) Reset(ctx context.Context, confirm string) error {
	body, err := json.Marshal(map[string]string{"confirm": confirm})
	if err != nil {
		return fmt.Errorf("encoding the request: %w", err)
	}

	// A reset drains before it leaves the cluster, so it inherits the drain's
	// minutes and then some; slowRequestTimeout, not the short deadline, for the
	// same reason Drain uses it.
	return c.callUsing(ctx, c.withTimeout(slowRequestTimeout),
		http.MethodPost, "/v1/lifecycle/reset", body, nil)
}

// RotateCA hands a node to a different operator CA.
//
// The proof certificate is not optional and the node enforces it: rotating to
// a CA you cannot issue certificates under leaves a machine that will only
// ever accept somebody else, and the way back is a trip to its console.
func (c *Client) RotateCA(ctx context.Context, operatorCA, proof []byte) error {
	body, err := json.Marshal(map[string]string{
		"operatorCA": string(operatorCA),
		"proof":      string(proof),
	})
	if err != nil {
		return fmt.Errorf("encoding the request: %w", err)
	}

	return c.call(ctx, http.MethodPost, "/v1/ca/rotate", body, nil)
}

// AddSSHKey trusts an SSH public key for a user that already exists on the
// node. The node creates no account: the user is cloud-init's to make.
//
// The reply is access.Key, decoded here rather than restated, for the same
// reason Node reuses nodeinfo.Node: a second definition of the same shape is a
// second thing to keep in step.
func (c *Client) AddSSHKey(ctx context.Context, user, key string) (*access.Key, error) {
	body, err := json.Marshal(map[string]string{"user": user, "key": key})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var added access.Key
	if err := c.call(ctx, http.MethodPost, "/v1/access/ssh", body, &added); err != nil {
		return nil, err
	}

	return &added, nil
}

// ListSSHKeys reports the SSH keys a node trusts, by fingerprint.
func (c *Client) ListSSHKeys(ctx context.Context) ([]access.Key, error) {
	var reply struct {
		Keys []access.Key `json:"keys"`
	}

	if err := c.call(ctx, http.MethodGet, "/v1/access/ssh", nil, &reply); err != nil {
		return nil, err
	}

	return reply.Keys, nil
}

// RevokeSSHKey stops a node trusting the key with the given fingerprint, and
// reports what it removed.
//
// The fingerprint travels as a query parameter because an SSH SHA-256
// fingerprint contains '/', which a path segment cannot carry.
func (c *Client) RevokeSSHKey(ctx context.Context, fingerprint string) ([]access.Key, error) {
	path := "/v1/access/ssh?" + url.Values{"fingerprint": {fingerprint}}.Encode()

	var reply struct {
		Removed []access.Key `json:"removed"`
	}

	if err := c.call(ctx, http.MethodDelete, path, nil, &reply); err != nil {
		return nil, err
	}

	return reply.Removed, nil
}

// Kubeconfig fetches the cluster's administrator credentials from a node.
//
// An empty server takes the node's own answer: the virtual IP on an HA control
// plane, and otherwise the address it was reached on.
func (c *Client) Kubeconfig(ctx context.Context, server string) ([]byte, error) {
	path := "/v1/kubeconfig"
	if server != "" {
		path += "?" + url.Values{"server": {server}}.Encode()
	}

	// Not JSON: a kubeconfig is YAML, and re-encoding it here would mean this
	// client deciding what a Kubernetes client may read.
	return c.raw(ctx, path)
}

// raw performs a GET and returns the body untouched.
func (c *Client) raw(ctx context.Context, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.address+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, c.reach(err)
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, c.explain(response.Status, body)
	}

	return body, nil
}
