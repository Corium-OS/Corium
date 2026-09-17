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
		name string
		api  API
		want APIMode
		held bool
	}{
		{
			// Every node provisioned before the API existed looks like this,
			// and none of them may grow a listening port by being upgraded.
			name: "absent block is off",
			api:  API{},
			want: APIModeDisabled,
		},
		{
			name: "explicit refusal is off",
			api:  API{Enabled: enabled(false)},
			want: APIModeDisabled,
		},
		{
			name: "an inline CA implies enabled",
			api:  API{OperatorCA: ca},
			want: APIModeConfigured,
		},
		{
			name: "a CA source implies enabled",
			api:  API{OperatorCAFrom: &SecretSource{URL: "https://pki.example.com/ca.pem"}},
			want: APIModeConfigured,
		},
		{
			name: "enabled with no CA waits for an operator",
			api:  API{Enabled: enabled(true)},
			want: APIModeMaintenance,
			held: true,
		},
		{
			name: "enabled alongside a CA does not wait",
			api:  API{Enabled: enabled(true), OperatorCA: ca},
			want: APIModeConfigured,
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
