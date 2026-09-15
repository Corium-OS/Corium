package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setMachineID(t *testing.T, id string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine-id")

	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		t.Fatalf("writing machine id: %v", err)
	}

	original := machineIDPath
	machineIDPath = path
	t.Cleanup(func() { machineIDPath = original })
}

func TestDeriveHostnameIsStable(t *testing.T) {
	// The derived name must not change between boots. A name that did would
	// register a new Kubernetes node on every restart and leave the previous
	// one behind as a ghost.
	setMachineID(t, "12db8c05d01b4eafa76e6031e12643ce\n")

	first, err := deriveHostname()
	if err != nil {
		t.Fatalf("deriveHostname() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		next, err := deriveHostname()
		if err != nil {
			t.Fatalf("deriveHostname() error = %v", err)
		}

		if next != first {
			t.Fatalf("deriveHostname() returned %q then %q; it must be stable", first, next)
		}
	}

	if first != "corium-12db8c05" {
		t.Errorf("deriveHostname() = %q, want corium-12db8c05", first)
	}
}

func TestDeriveHostnameDiffersPerMachine(t *testing.T) {
	setMachineID(t, "12db8c05d01b4eafa76e6031e12643ce")

	a, err := deriveHostname()
	if err != nil {
		t.Fatalf("deriveHostname() error = %v", err)
	}

	setMachineID(t, "158b2e541ad043d98faa623679836fe4")

	b, err := deriveHostname()
	if err != nil {
		t.Fatalf("deriveHostname() error = %v", err)
	}

	if a == b {
		t.Errorf("two machines derived the same hostname %q", a)
	}
}

func TestDeriveHostnameWithoutMachineID(t *testing.T) {
	// Inventing a random name would produce a node that renames itself on
	// every boot, so this must fail rather than improvise.
	setMachineID(t, "")

	if _, err := deriveHostname(); err == nil {
		t.Fatal("deriveHostname() error = nil, want a refusal")
	}
}

func TestDerivedHostnameIsValidForKubernetes(t *testing.T) {
	setMachineID(t, "ffffffffffffffffffffffffffffffff")

	name, err := deriveHostname()
	if err != nil {
		t.Fatalf("deriveHostname() error = %v", err)
	}

	if len(name) > 63 {
		t.Errorf("derived hostname %q is too long for a node name", name)
	}

	if name != strings.ToLower(name) {
		t.Errorf("derived hostname %q is not lowercase", name)
	}
}

func TestGenericHostnamesAreRecognised(t *testing.T) {
	// These are what an unconfigured image boots with. Treating them as real
	// identities is exactly how every node in a cluster ends up sharing a name.
	for _, name := range []string{"", "fedora", "localhost", "localhost.localdomain", "FEDORA"} {
		if !genericHostnames[strings.ToLower(name)] {
			t.Errorf("%q should be treated as a generic hostname", name)
		}
	}

	for _, name := range []string{"ctrl-1", "corium-12db8c05", "node01"} {
		if genericHostnames[strings.ToLower(name)] {
			t.Errorf("%q should be treated as a real hostname", name)
		}
	}
}

func TestVirtualAddressParsing(t *testing.T) {
	addr, err := virtualAddress("192.168.0.200/24")
	if err != nil {
		t.Fatalf("virtualAddress() error = %v", err)
	}

	if addr.String() != "192.168.0.200" {
		t.Errorf("virtualAddress() = %v, want 192.168.0.200", addr)
	}

	// No HA configured means nothing to exclude.
	addr, err = virtualAddress("")
	if err != nil {
		t.Fatalf("virtualAddress(\"\") error = %v", err)
	}

	if addr.IsValid() {
		t.Errorf("virtualAddress(\"\") = %v, want an invalid (absent) address", addr)
	}

	if _, err := virtualAddress("not-an-address"); err == nil {
		t.Error("virtualAddress() accepted a malformed virtual IP")
	}
}

func TestDetectNodeIPAvoidsTheVirtualIP(t *testing.T) {
	// Whatever this machine's real address is, detection must never return the
	// address it was told to exclude.
	actual, err := detectNodeIP("")
	if err != nil {
		t.Skipf("no usable address on this host: %v", err)
	}

	got, err := detectNodeIP(actual + "/32")
	if err != nil {
		// A host with exactly one address legitimately has no alternative.
		t.Skipf("host has only one usable address: %v", err)
	}

	if got == actual {
		t.Errorf("detectNodeIP() returned the excluded virtual IP %q", got)
	}
}
