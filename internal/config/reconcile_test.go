package config

import (
	"reflect"
	"slices"
	"strings"
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

// reconcileClassification is every field of the `corium:` block, and whether
// PlanReconcile treats it as immutable day-two or as part of the safe subset.
//
// It exists because the immutable list in PlanReconcile is written out by hand,
// and a field added to Config but not to that list is not refused and not
// applied either -- it is silently ignored, which is the one outcome an
// operator cannot see. That is not hypothetical: `zfs` was absent from the list
// from the day it was added until this map started failing the build.
var reconcileClassification = map[string]bool{
	// field name -> immutable
	"role":      true,
	"cluster":   true,
	"network":   true,
	"storage":   true,
	"join":      true,
	"node":      true,
	"ha":        true,
	"raid":      true,
	"zfs":       true,
	"wireguard": true,
	"upgrades":  true,
	"backup":    true,
	"api":       true,

	// The safe subset. Both render into k0s.yaml, which k0s reconciles.
	"addons": false,
	"k0s":    false,
}

// TestPlanReconcileClassifiesEveryField fails when a field is added to Config
// and nowhere else. Adding one to the map above is the deliberate act the
// silence used to skip.
func TestPlanReconcileClassifiesEveryField(t *testing.T) {
	for _, field := range configFields(t) {
		if _, classified := reconcileClassification[field]; !classified {
			t.Errorf("corium.%s is in neither PlanReconcile's immutable list nor "+
				"the safe subset, so a day-two apply that changes it is ignored "+
				"rather than refused; classify it in reconcileClassification", field)
		}
	}

	for field := range reconcileClassification {
		if !slices.Contains(configFields(t), field) {
			t.Errorf("reconcileClassification names %q, which Config no longer has", field)
		}
	}
}

// TestPlanReconcileRefusesEveryImmutableField changes each immutable field in
// turn and checks the plan names it. The map above says what should happen;
// this is what proves PlanReconcile agrees.
func TestPlanReconcileRefusesEveryImmutableField(t *testing.T) {
	for field, immutable := range reconcileClassification {
		if !immutable {
			continue
		}

		t.Run(field, func(t *testing.T) {
			old := &Config{Role: RoleSingle}

			next := &Config{Role: RoleSingle}
			changeField(t, next, field)

			plan := PlanReconcile(old, next)

			if !slices.Contains(plan.Immutable, field) {
				t.Errorf("changing %s: immutable = %v, want it to name %q",
					field, plan.Immutable, field)
			}
		})
	}
}

// configFields lists the `corium:` keys Config declares, in declaration order.
func configFields(t *testing.T) []string {
	t.Helper()

	structType := reflect.TypeOf(Config{})
	fields := make([]string, 0, structType.NumField())

	for i := range structType.NumField() {
		tag, _, _ := strings.Cut(structType.Field(i).Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			t.Fatalf("Config field %s carries no yaml tag", structType.Field(i).Name)
		}

		fields = append(fields, tag)
	}

	return fields
}

// changeField sets one field of a Config to something that is not its zero
// value, so PlanReconcile has a difference to report.
func changeField(t *testing.T, cfg *Config, field string) {
	t.Helper()

	value := reflect.ValueOf(cfg).Elem()
	structType := value.Type()

	for i := range structType.NumField() {
		tag, _, _ := strings.Cut(structType.Field(i).Tag.Get("yaml"), ",")
		if tag != field {
			continue
		}

		value.Field(i).Set(nonZero(t, structType.Field(i).Type))

		return
	}

	t.Fatalf("Config has no field tagged %q", field)
}

// nonZero builds a value of the given type that differs from its zero value.
//
// Only the kinds the schema actually uses are handled: a new kind appearing
// here means the schema grew something this test cannot vary, which is worth
// stopping for rather than silently producing a zero value and passing.
func nonZero(t *testing.T, fieldType reflect.Type) reflect.Value {
	t.Helper()

	value := reflect.New(fieldType).Elem()

	switch fieldType.Kind() {
	case reflect.String:
		value.SetString("changed")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int:
		value.SetInt(1)
	case reflect.Slice:
		value.Set(reflect.Append(value, nonZero(t, fieldType.Elem())))
	case reflect.Map:
		value.Set(reflect.MakeMap(fieldType))
		value.SetMapIndex(nonZero(t, fieldType.Key()), nonZero(t, fieldType.Elem()))
	case reflect.Pointer:
		value.Set(reflect.New(fieldType.Elem()))
		value.Elem().Set(nonZero(t, fieldType.Elem()))
	case reflect.Interface:
		value.Set(reflect.ValueOf("changed"))
	case reflect.Struct:
		// The first field is enough: any difference inside a block is a
		// difference to the block, which is how PlanReconcile compares them.
		value.Field(0).Set(nonZero(t, fieldType.Field(0).Type))
	default:
		t.Fatalf("no non-zero value for %s (kind %s)", fieldType, fieldType.Kind())
	}

	return value
}
