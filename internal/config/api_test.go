package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// certificate mints a PEM certificate for a test to feed to validation.
//
// Certificates are generated rather than checked in as fixtures because a
// fixture carries an expiry date, and a test that starts failing on a Tuesday
// three years from now for reasons unrelated to the code is worse than no test.
func certificate(t *testing.T, isCA bool, notAfter time.Time) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "corium operators"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}

	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func operatorCA(t *testing.T) string {
	t.Helper()

	return certificate(t, true, time.Now().Add(24*time.Hour))
}

func enabled(v bool) *bool { return &v }

func TestAPIMode(t *testing.T) {
	ca := operatorCA(t)

	tests := []struct {
		name     string
		api      API
		want     APIMode
		held     bool
		declared bool
	}{
		{
			// ADR 11: an absent block now serves the enrolment route, because
			// the node most in need of it is the one the installer ISO
			// produces, which carries no configuration at all. It serves and
			// nothing more -- held stays false, so a document that names a
			// role still builds it on first boot.
			name: "absent block is enrollable and holds nothing",
			api:  API{},
			want: APIModeEnrollable,
		},
		{
			name: "explicit refusal is off",
			api:  API{Enabled: enabled(false)},
			want: APIModeDisabled,
		},
		{
			name:     "an inline CA implies enabled",
			api:      API{OperatorCA: ca},
			want:     APIModeConfigured,
			declared: true,
		},
		{
			name:     "a CA source implies enabled",
			api:      API{OperatorCAFrom: &SecretSource{URL: "https://pki.example.com/ca.pem"}},
			want:     APIModeConfigured,
			declared: true,
		},
		{
			name:     "enabled with no CA waits for an operator",
			api:      API{Enabled: enabled(true)},
			want:     APIModeMaintenance,
			held:     true,
			declared: true,
		},
		{
			name:     "enabled alongside a CA does not wait",
			api:      API{Enabled: enabled(true), OperatorCA: ca},
			want:     APIModeConfigured,
			declared: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.api.Mode(); got != tc.want {
				t.Errorf("Mode() = %q, want %q", got, tc.want)
			}

			if got := tc.api.HoldsBootstrap(); got != tc.held {
				t.Errorf("HoldsBootstrap() = %v, want %v", got, tc.held)
			}

			// Declared is what the behaviours that hold a node are gated on,
			// so that the default running the daemon never turns a typo into a
			// node that waits for ever.
			if got := tc.api.Declared(); got != tc.declared {
				t.Errorf("Declared() = %v, want %v", got, tc.declared)
			}
		})
	}
}

func TestValidateAPI(t *testing.T) {
	ca := operatorCA(t)

	tests := []struct {
		name    string
		api     API
		wantErr string
	}{
		{
			name:    "both spellings of the CA",
			api:     API{OperatorCA: ca, OperatorCAFrom: &SecretSource{File: "/etc/ca.pem"}},
			wantErr: "not both",
		},
		{
			name:    "refusing the API while naming its owner",
			api:     API{Enabled: enabled(false), OperatorCA: ca},
			wantErr: "cannot both refuse the API and name its owner",
		},
		{
			name:    "a CA fetched over plain http",
			api:     API{OperatorCAFrom: &SecretSource{URL: "http://pki.example.com/ca.pem"}},
			wantErr: "api.operatorCAFrom.url: scheme must be https",
		},
		{
			name:    "not PEM at all",
			api:     API{OperatorCA: "hunter2"},
			wantErr: "not PEM data",
		},
		{
			// The mistake worth naming: pasting the key that owns the fleet
			// into a document that ends up in instance metadata.
			name: "a private key instead of a certificate",
			api: API{OperatorCA: string(pem.EncodeToMemory(&pem.Block{
				Type: "EC PRIVATE KEY", Bytes: []byte("not really a key"),
			}))},
			wantErr: "not a certificate",
		},
		{
			name:    "a chain rather than an anchor",
			api:     API{OperatorCA: ca + operatorCA(t)},
			wantErr: "exactly one certificate",
		},
		{
			name:    "a leaf certificate",
			api:     API{OperatorCA: certificate(t, false, time.Now().Add(24*time.Hour))},
			wantErr: "is not a CA",
		},
		{
			name:    "an expired CA",
			api:     API{OperatorCA: certificate(t, true, time.Now().Add(-24*time.Hour))},
			wantErr: "expired on",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Role: RoleSingle, API: tc.api}
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want one containing %q", tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAPIAcceptsGoodConfigs(t *testing.T) {
	ca := operatorCA(t)

	good := map[string]API{
		"absent":      {},
		"disabled":    {Enabled: enabled(false)},
		"maintenance": {Enabled: enabled(true)},
		"inline CA":   {OperatorCA: ca},
		"CA from a source": {OperatorCAFrom: &SecretSource{
			URL: "https://pki.example.com/ca.pem", WaitFor: "15m",
		}},
	}

	for name, api := range good {
		t.Run(name, func(t *testing.T) {
			cfg := Config{Role: RoleSingle, API: api}
			cfg.ApplyDefaults()

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestSecretSourceErrorsNameTheFieldTheyCameFrom(t *testing.T) {
	// A regression: every SecretSource problem used to be reported as
	// join.tokenFrom whichever key it was reached through, which sent an
	// operator to a line that was not the one at fault.
	cfg := Config{
		Role:    RoleController,
		Cluster: Cluster{Endpoint: "192.168.0.200"},
		Join:    Join{Token: "x"},
		HA: HA{
			Enabled:      true,
			VirtualIP:    "192.168.0.200/24",
			AuthPassFrom: &SecretSource{URL: "http://vault.example.com/vrrp"},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want one")
	}

	if !strings.Contains(err.Error(), "ha.authPassFrom.url") {
		t.Errorf("Validate() error = %q, want it to name ha.authPassFrom.url", err)
	}

	if strings.Contains(err.Error(), "join.tokenFrom") {
		t.Errorf("Validate() error = %q, must not blame join.tokenFrom", err)
	}
}

func TestInsecureOnlyAppliesToMaintenanceMode(t *testing.T) {
	ca := operatorCA(t)

	// A key that silently does nothing is the mistake that costs an operator a
	// reboot cycle to find, so the contradictions are refused.
	for name, api := range map[string]API{
		"with an inline CA":   {OperatorCA: ca, Insecure: true},
		"with a CA source":    {OperatorCAFrom: &SecretSource{File: "/etc/ca.pem"}, Insecure: true},
		"with the API off":    {Insecure: true},
		"with an explicit no": {Enabled: enabled(false), Insecure: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{Role: RoleSingle, API: api}
			cfg.ApplyDefaults()

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "api.insecure") {
				t.Errorf("Validate() = %v, want a complaint about api.insecure", err)
			}
		})
	}
}

func TestInsecureMaintenanceIsAccepted(t *testing.T) {
	cfg := Config{Role: RoleWorker, Join: Join{Token: "x"},
		API: API{Enabled: enabled(true), Insecure: true}}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if !cfg.API.OpenEnrolment() {
		t.Error("OpenEnrolment() = false on an insecure maintenance node")
	}

	if !cfg.API.HoldsBootstrap() {
		t.Error("an open node still joins no cluster until it is claimed")
	}
}

func TestAwaitConfigNeedsAnAPIToWaitOn(t *testing.T) {
	// A node told to wait for a configuration it has no way of receiving waits
	// for ever, and nothing on the console would explain why.
	cfg := Config{Role: RoleSingle, API: API{AwaitConfig: true}}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "api.awaitConfig") {
		t.Errorf("Validate() = %v, want a complaint about api.awaitConfig", err)
	}
}

func TestAwaitConfigIsAcceptedWhereverTheAPIRuns(t *testing.T) {
	// Both modes are legitimate: maintenance mode waits for an owner and then
	// for a document, and a node whose owner is already named in its
	// configuration can still be waiting to be told what it is.
	for name, api := range map[string]API{
		"in maintenance mode": {Enabled: enabled(true), AwaitConfig: true},
		"with an inline CA":   {OperatorCA: operatorCA(t), AwaitConfig: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{Role: RoleSingle, API: api}
			cfg.ApplyDefaults()

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestARolelessDocumentIsHowANodeSaysItIsWaiting(t *testing.T) {
	// A document that names no role describes no node, so there is nothing to
	// build from it. That is a legitimate thing to boot with -- it is the whole
	// fleet-wide cloud-config -- provided somebody can still answer, which
	// means the API has to be running.
	for name, api := range map[string]API{
		"in maintenance mode":  {Enabled: enabled(true)},
		"with an inline CA":    {OperatorCA: operatorCA(t)},
		"saying so explicitly": {Enabled: enabled(true), AwaitConfig: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{API: api}
			cfg.ApplyDefaults()

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}

			if !cfg.HoldsForConfiguration() {
				t.Error("HoldsForConfiguration() = false, want true: there is no role to build")
			}
		})
	}
}

func TestARolelessDocumentWithNoAPIIsRefused(t *testing.T) {
	// Since ADR 11 somebody *could* tell this node what it is -- the daemon is
	// running. It is refused anyway, because a document that names neither a
	// role nor an api: block is far more often a typo in role: than a node
	// meaning to wait, and turning that typo into a machine that waits for ever
	// is the trade the old rule existed to avoid. Validation is the one moment
	// somebody is still watching.
	cfg := Config{}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "role: required") {
		t.Errorf("Validate() = %v, want a complaint that role is required", err)
	}
}

func TestTheDefaultRunsTheAPIWithoutHoldingAnything(t *testing.T) {
	// ADR 11's central promise to every configuration already in service: the
	// port opens, and nothing else about first boot changes.
	cfg := Config{Role: RoleSingle}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if got := cfg.API.Mode(); got != APIModeEnrollable {
		t.Errorf("Mode() = %q, want %q", got, APIModeEnrollable)
	}

	if cfg.API.HoldsBootstrap() {
		t.Error("HoldsBootstrap() = true, want false: nobody asked this node to wait")
	}

	if cfg.HoldsForConfiguration() {
		t.Error("HoldsForConfiguration() = true, want false: the document says what the node is")
	}
}

func TestHoldingTheBootstrapHasToBeAskedFor(t *testing.T) {
	// awaitConfig on its own used to be refused because the API was off. The
	// API is on now, so the refusal needs its own reason: holding a node until
	// a document arrives is not something a default may do to somebody who
	// never asked for it.
	cfg := Config{Role: RoleSingle, API: API{AwaitConfig: true}}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "api.awaitConfig") {
		t.Errorf("Validate() = %v, want a complaint about api.awaitConfig", err)
	}
}

func TestInsecureStillNeedsMaintenanceMode(t *testing.T) {
	// ADR 11 makes the port default, not the openness. Reaching open enrolment
	// still takes an explicit api.enabled: true beside it.
	cfg := Config{Role: RoleSingle, API: API{Insecure: true}}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "api.insecure") {
		t.Errorf("Validate() = %v, want a complaint about api.insecure", err)
	}

	if cfg.API.OpenEnrolment() {
		t.Error("OpenEnrolment() = true, want false: the default is the pairing code")
	}
}

func TestADocumentThatNamesARoleDoesNotHold(t *testing.T) {
	// The other half of the rule: a document that says what the node is gets
	// built, and maintenance mode only decides when.
	cfg := Config{Role: RoleSingle, API: API{Enabled: enabled(true)}}
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if cfg.HoldsForConfiguration() {
		t.Error("HoldsForConfiguration() = true, want false: this document names a role")
	}
}
