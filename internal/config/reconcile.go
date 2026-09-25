package config

import "reflect"

// ReconcilePlan is the result of comparing the configuration a node is running
// with one an operator wants to apply after it has already bootstrapped.
//
// The distinction it draws is the one ADR 8 rests on: a field that defines what
// the node *is* cannot change under a running node without making its
// configuration and its behaviour two different facts, while the add-on set is
// content k0s reconciles and can safely be re-applied.
type ReconcilePlan struct {
	// Immutable names the fields that changed and may not change on a running
	// node. A non-empty list means the whole apply must be refused and the node
	// sent to `cctl reset`, rather than applied in part.
	Immutable []string

	// Addons is true when the add-on set changed at all -- added, removed or
	// altered. It says something changed, not that it can be applied; see
	// RemovedAddons.
	Addons bool

	// K0s is true when the k0s escape hatch (`k0s.patch`) changed. Like Addons
	// it renders into /etc/k0s/k0s.yaml, which k0s reconciles when the
	// controller restarts, so it is re-applied the same way rather than sending
	// the node to a reset. The patch is passed through verbatim, so a change
	// that rewrites something load-bearing is the operator's to own -- the same
	// contract it carries at bootstrap.
	K0s bool

	// RemovedAddons names the charts present in the running configuration and
	// absent from the proposal. Removal is not reconcilable: k0s's Helm
	// extensions install a chart from the configuration but do not uninstall
	// one dropped from it -- the release is left running (see docs/features.md,
	// "Removing add-ons"). Re-rendering k0s.yaml without the chart would report
	// a removal that did not happen, so a proposal that drops one is refused.
	RemovedAddons []string
}

// Reconcilable reports whether the plan can be applied to a running node: it
// can exactly when nothing immutable changed and no add-on was removed.
func (p ReconcilePlan) Reconcilable() bool {
	return len(p.Immutable) == 0 && len(p.RemovedAddons) == 0
}

// Empty reports that the two configurations are equivalent for reconciliation:
// nothing immutable and nothing in the safe subset changed.
func (p ReconcilePlan) Empty() bool {
	return len(p.Immutable) == 0 && !p.Addons && !p.K0s
}

// PlanReconcile classifies the change from a node's running configuration (old)
// to a proposed one (new), field by field.
//
// The safe subset is `addons` and the `k0s` escape hatch. Both render into
// /etc/k0s/k0s.yaml, which k0s reconciles when the controller restarts:
// `addons` are charts k0s installs, and `k0s.patch` is passed through verbatim,
// so a patch that rewrites something load-bearing is the operator's to own --
// the same contract it carries at bootstrap. Every other field stays immutable
// day-two, because it defines the node's identity (`role`, `node`), its cluster
// (`cluster`, `join`, `ha`), its network or disks (`network`, `storage`,
// `raid`, `wireguard`), or is not yet reconciled here (`upgrades`, `backup`,
// `api`, `manifests`). Changing any of those is a provisioning act. See ADR 8;
// the set the node calls safe is expected to grow.
//
// `manifests` is the one that looks reconcilable and is not yet: the deployer
// would in fact pick up a rewritten file, and prune what a deleted one created,
// which is more than `addons` manages. What is missing is the decision, not the
// mechanism -- ADR 8 names the safe subset, and this list grows when that
// document does, not when a field looks like it would cope.
func PlanReconcile(old, next *Config) ReconcilePlan {
	var plan ReconcilePlan

	// Compared as whole values rather than field by field within each block: a
	// change anywhere inside `network` or `ha` is still a change to something
	// immutable, and enumerating their internals here would be a second copy of
	// the schema to keep in step with the first.
	immutable := []struct {
		name     string
		old, new any
	}{
		{"role", old.Role, next.Role},
		{"cluster", old.Cluster, next.Cluster},
		{"network", old.Network, next.Network},
		{"storage", old.Storage, next.Storage},
		{"join", old.Join, next.Join},
		{"node", old.Node, next.Node},
		{"ha", old.HA, next.HA},
		{"raid", old.RAID, next.RAID},
		{"wireguard", old.WireGuard, next.WireGuard},
		{"upgrades", old.Upgrades, next.Upgrades},
		{"backup", old.Backup, next.Backup},
		{"api", old.API, next.API},
		{"manifests", old.Manifests, next.Manifests},
	}

	for _, field := range immutable {
		if !reflect.DeepEqual(field.old, field.new) {
			plan.Immutable = append(plan.Immutable, field.name)
		}
	}

	plan.Addons = !reflect.DeepEqual(old.Addons, next.Addons)
	plan.RemovedAddons = removedAddons(old.Addons, next.Addons)
	plan.K0s = !reflect.DeepEqual(old.K0s, next.K0s)

	return plan
}

// removedAddons names the charts in old that next no longer declares, matched
// by name -- the field k0s keys a release on.
func removedAddons(old, next []Addon) []string {
	declared := make(map[string]bool, len(next))
	for _, addon := range next {
		declared[addon.Name] = true
	}

	var removed []string

	for _, addon := range old {
		if !declared[addon.Name] {
			removed = append(removed, addon.Name)
		}
	}

	return removed
}
