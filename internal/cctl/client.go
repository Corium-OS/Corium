package cctl

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Corium-OS/Corium/internal/api"
)

// requestTimeout bounds a single call. Every route this client speaks to
// answers immediately or not at all; streaming routes, when they exist, will
// need their own.
const requestTimeout = 30 * time.Second

// maxResponse caps what a node can make this tool read.
const maxResponse = 1 << 20

// ErrFingerprintUnknown reports an endpoint nobody has vouched for yet.
var ErrFingerprintUnknown = errors.New("no fingerprint known for this node")

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

	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}

		if err := json.Unmarshal(raw, &failure); err == nil && failure.Error != "" {
			// The node's own words. It knows why it said no, and rewording it
			// here would only lose detail.
			return fmt.Errorf("%s: %s", response.Status, failure.Error)
		}

		return fmt.Errorf("%s from %s", response.Status, c.address)
	}

	if into == nil {
		return nil
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("parsing the response: %w", err)
	}

	return nil
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
