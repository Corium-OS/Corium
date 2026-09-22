package config

import (
	"slices"
	"testing"
)

// parseForReconcile is the exact path a document travels on both sides of a
// day-two apply: parsed and defaulted, so the baseline and the proposal are
// compared on equal terms.
func parseForReconcile(t *testing.T, document string) *Config {
	t.Helper()

	cfg, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", document, err)
	}

	return cfg
}

func TestPlanReconcileUnchanged(t *testing.T) {
	doc := "role: single\n"

	plan := PlanReconcile(parseForReconcile(t, doc), parseForReconcile(t, doc))

	if !plan.Empty() {
		t.Errorf("identical documents should reconcile to nothing, got %+v", plan)
	}

	if !plan.Reconcilable() {
		t.Error("an empty plan must be reconcilable")
	}
}

func TestPlanReconcileAddonsAreSafe(t *testing.T) {
	old := parseForReconcile(t, "role: single\n")
	next := parseForReconcile(t, `role: single
addons:
  - name: cert-manager
    chart: jetstack/cert-manager
    version: 1.16.2
    namespace: cert-manager
    repository: {name: jetstack, url: 'https://charts.jetstack.io'}
`)

	plan := PlanReconcile(old, next)

	if !plan.Addons {
		t.Error("an added chart should be seen as an add-on change")
	}

	if !plan.Reconcilable() {
		t.Errorf("an add-on-only change must be reconcilable, got immutable %v", plan.Immutable)
	}

	if plan.Empty() {
		t.Error("an add-on change is not nothing")
	}
}

func TestPlanReconcileRemovingAnAddonIsRefused(t *testing.T) {
	// k0s installs a chart from its configuration but does not uninstall one
	// dropped from it, so a removal cannot be reconciled without lying about it.
	withAddon := `role: single
addons:
  - name: cert-manager
    chart: jetstack/cert-manager
    namespace: cert-manager
`
	old := parseForReconcile(t, withAddon)
	next := parseForReconcile(t, "role: single\n")

	plan := PlanReconcile(old, next)

	if plan.Reconcilable() {
		t.Errorf("dropping an add-on must not be reconcilable, got %+v", plan)
	}

	if !slices.Contains(plan.RemovedAddons, "cert-manager") {
		t.Errorf("removedAddons = %v, want it to name cert-manager", plan.RemovedAddons)
	}
}

func TestPlanReconcileChangingAnAddonIsSafe(t *testing.T) {
	// Bumping a chart's version keeps the release, so it stays reconcilable --
	// only dropping a chart is the problem.
	oldVersion := `role: single
addons:
  - name: cert-manager
    chart: jetstack/cert-manager
    version: 1.16.1
    namespace: cert-manager
`
	newVersion := `role: single
addons:
  - name: cert-manager
    chart: jetstack/cert-manager
    version: 1.16.2
    namespace: cert-manager
`
	plan := PlanReconcile(parseForReconcile(t, oldVersion), parseForReconcile(t, newVersion))

	if !plan.Addons || !plan.Reconcilable() || len(plan.RemovedAddons) != 0 {
		t.Errorf("changing an add-on's version must be a reconcilable change, got %+v", plan)
	}
}

func TestPlanReconcileK0sPatchIsSafe(t *testing.T) {
	// The k0s escape hatch renders into k0s.yaml, which k0s reconciles on a
	// controller restart, so changing it day-two is reconcilable rather than a
	// reset -- the patch is the operator's to own, as it is at bootstrap.
	old := parseForReconcile(t, "role: single\n")
	next := parseForReconcile(t, `role: single
k0s:
  patch:
    spec:
      api:
        extraArgs:
          oidc-issuer-url: https://id.example.com
          oidc-client-id: k0s
`)

	plan := PlanReconcile(old, next)

	if !plan.K0s {
		t.Error("a changed k0s patch should be seen as a k0s change")
	}

	if !plan.Reconcilable() {
		t.Errorf("a k0s-only change must be reconcilable, got immutable %v", plan.Immutable)
	}

	if plan.Empty() {
		t.Error("a k0s change is not nothing")
	}
}

func TestPlanReconcileImmutableFieldsAreRefused(t *testing.T) {
	base := "role: single\n"

	tests := map[string]struct {
		document string
		field    string
	}{
		"role": {
			"role: controller\njoin:\n  token: abc\n", "role",
		},
		"node name": {
			"role: single\nnode:\n  name: renamed\n", "node",
		},
		"cluster": {
			"role: single\ncluster:\n  name: elsewhere\n", "cluster",
		},
		"network": {
			"role: single\nnetwork:\n  podCIDR: 10.9.0.0/16\n", "network",
		},
		"upgrades": {
			"role: single\nupgrades:\n  automatic: apply\n", "upgrades",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			plan := PlanReconcile(parseForReconcile(t, base), parseForReconcile(t, tt.document))

			if plan.Reconcilable() {
				t.Fatalf("changing %s must not be reconcilable, got %+v", tt.field, plan)
			}

			if !slices.Contains(plan.Immutable, tt.field) {
				t.Errorf("immutable = %v, want it to name %q", plan.Immutable, tt.field)
			}
		})
	}
}

// TestPlanReconcileReportsEveryImmutableField guards the "all or nothing"
// contract: an apply that changes two immutable fields names both, so the
// refusal is not one whack-a-mole round trip per field.
func TestPlanReconcileReportsEveryImmutableField(t *testing.T) {
	old := parseForReconcile(t, "role: single\n")
	next := parseForReconcile(t, "role: single\nnode:\n  name: renamed\ncluster:\n  name: elsewhere\n")

	plan := PlanReconcile(old, next)

	for _, want := range []string{"node", "cluster"} {
		if !slices.Contains(plan.Immutable, want) {
			t.Errorf("immutable = %v, want it to include %q", plan.Immutable, want)
		}
	}
}

// TestPlanReconcileMixedChangeIsRefused proves the safe half of a document does
// not slip through when its unsafe half is rejected: changing an add-on and the
// node name at once is refused whole.
func TestPlanReconcileMixedChangeIsRefused(t *testing.T) {
	old := parseForReconcile(t, "role: single\n")
	next := parseForReconcile(t, `role: single
node: {name: renamed}
addons:
  - name: cert-manager
    chart: jetstack/cert-manager
    namespace: cert-manager
`)

	plan := PlanReconcile(old, next)

	if plan.Reconcilable() {
		t.Error("a change touching an immutable field must be refused even when it also touches a safe one")
	}
}
