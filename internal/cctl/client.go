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
	"time"

	"github.com/Corium-OS/Corium/internal/api"
	"github.com/Corium-OS/Corium/internal/nodeinfo"
	"github.com/Corium-OS/Corium/internal/systemd"
	"github.com/Corium-OS/Corium/internal/upgrade"
)

// requestTimeout bounds a single call. Every route this client speaks to
// answers immediately or not at all; streaming routes, when they exist, will
// need their own.
const requestTimeout = 30 * time.Second

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
}

// Dial prepares a client for an address.
//
// The expected fingerprint is how a node is identified. A node signs its own
// certificate — the scheme carries no private key in any configuration, so
// there is nothing to sign it with — which means a name proves nothing and the
// fingerprint proves everything. Passing an empty one accepts whatever answers
// and records it, which is only appropriate while reading the console.
func Dial(address, expected string, clientCertificates ...tls.Certificate) *Client {
	client := &Client{address: address}

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

// Enrol claims a node.
func (c *Client) Enrol(ctx context.Context, code string, operatorCA []byte) (*EnrolResult, error) {
	body, err := json.Marshal(map[string]string{
		"code":       code,
		"operatorCA": string(operatorCA),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var result EnrolResult
	if err := c.call(ctx, http.MethodPost, "/v1/enroll", body, &result); err != nil {
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

// call makes one request and turns a node's error into this tool's error.
func (c *Client) call(ctx context.Context, method, path string, body []byte, into any) error {
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

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", c.address, err)
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
	streaming := *c.http
	streaming.Timeout = 0

	response, err := streaming.Do(request)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", c.address, err)
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

// Stage asks a node to pull an image and prepare to boot it.
func (c *Client) Stage(ctx context.Context, image string) (*upgrade.Staged, error) {
	body, err := json.Marshal(map[string]string{"image": image})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	var staged upgrade.Staged
	if err := c.call(ctx, http.MethodPost, "/v1/upgrade/stage", body, &staged); err != nil {
		return nil, err
	}

	return &staged, nil
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
