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

	// Addons is true when the add-on set changed and nothing immutable did.
	Addons bool
}

// Reconcilable reports whether the plan can be applied to a running node: it
// can exactly when nothing immutable changed.
func (p ReconcilePlan) Reconcilable() bool {
	return len(p.Immutable) == 0
}

// Empty reports that the two configurations are equivalent for reconciliation:
// nothing immutable and nothing in the safe subset changed.
func (p ReconcilePlan) Empty() bool {
	return len(p.Immutable) == 0 && !p.Addons
}

// PlanReconcile classifies the change from a node's running configuration (old)
// to a proposed one (new), field by field.
//
// The safe subset is deliberately one field wide: `addons`, whose reconciler is
// k0s. Every other field is treated as immutable day-two, because it defines
// the node's identity (`role`, `node`), its cluster (`cluster`, `join`, `ha`),
// its network or disks (`network`, `storage`, `raid`, `wireguard`), or reaches
// past what this path can reason about (`k0s`, and for now `upgrades` and
// `api`). Changing any of those is a provisioning act. See ADR 8; the set the
// node calls safe is expected to grow there before it grows here.
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
		{"api", old.API, next.API},
		{"k0s", old.K0s, next.K0s},
	}

	for _, field := range immutable {
		if !reflect.DeepEqual(field.old, field.new) {
			plan.Immutable = append(plan.Immutable, field.name)
		}
	}

	plan.Addons = !reflect.DeepEqual(old.Addons, next.Addons)

	return plan
}
