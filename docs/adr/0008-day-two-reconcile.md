# 8. Re-applying the safe subset of a node's configuration, day two

Status: accepted

## Context

Corium reads a node's configuration once, at first boot, and then stops. Decision
11 makes this a deliberate position and not an accident: Corium provisions nodes
and does not manage them. ADR 4 states the rule under it: a node answers the
question of its configuration *before* it is a node. `cctl apply`, and the
`POST /v1/config` behind it, writes `/etc/corium/config.yaml` (first in the source
chain, decision 6). As soon as a node has bootstrapped, it refuses the request:

    this node has already bootstrapped, so its configuration is no longer a
    question it can answer; use cctl reset to return it to maintenance mode

A bare edit meets the same one-way door. `corium-bootstrap.service` carries
`ConditionPathExists=!/var/lib/corium/bootstrapped`. After the marker is written,
systemd skips the unit on every later boot, and the agent exits early even if it
runs. If an operator edits `config.yaml` on a running node and reboots, once or
many times, nothing changes and nothing reports it. An operator who bumped a Helm
chart version and expected it to take effect sees a bug, not a policy.

That refusal is correct for the fields that it was written to guard. If Corium
rewrote the role, the cluster, or the name of a machine that already runs
Kubernetes, the configuration and the behavior of the machine would become two
different facts. Decisions 6 and 7 exist to prevent exactly this mismatch. It is
also the reason that `cctl reset` removes a node from its cluster before it
changes any of these fields.

But the refusal covers the whole document, and the fields in the document are not
all of one kind. An operator who adds a `cert-manager` add-on, or flips
`upgrades.automatic`, does not redefine what the node *is*. The operator asks for
a chart to be present, or for a timer to run. These are day-two acts (changes to
a node that is already in service) on a node whose identity does not change.
Today the only route to them is `cctl reset`, which destroys a single-node cluster
to install a chart. The other route is to work under Corium by hand, and then the
configuration and the behavior of the node disagree anyway.

The fields divide cleanly:

- **Identity, cluster and disk.** `role`, `cluster`, `join`, `node.name`,
  `network`, `storage`, `raid`, `wireguard` and `ha` define what the machine is,
  which cluster it belongs to, and what data lives on it. k0s registers a node
  under the name and address that it reads at startup. A CNI or a datastore is a
  choice made when the cluster is created. An array holds data. None of these
  fields can change under a running node unless the node reports something false
  or loses something.
- **Declared cluster content and node policy.** `addons` are Helm charts.
  `upgrades` is a timer and a policy. Their real reconciler (the component that
  makes the running state match the declared state) is a tool that Corium already
  delegates to: k0s for the charts, systemd for the timer. Corium does not have to
  become a reconciler itself.

## Decision

On a node that has already bootstrapped, `cctl apply` is no longer a flat refusal.
It becomes a **gated re-application of the safe subset**.

The node compares the incoming document with the document that it bootstrapped
with. The node records that document for this purpose (see below). Then it
classifies every field that changed:

- **If any identity, cluster or disk field differs, the node refuses the whole
  apply.** It gives the same message and the same pointer to `cctl reset` as
  today. The node applies nothing partially. An operator does not get half of an
  intent, and does not get the safe half of a document whose unsafe half was
  rejected.
- **Otherwise the node re-applies the safe subset.** In the first cut, that subset
  is `addons` and `upgrades`. These are the two fields whose effect Corium can give
  back to a reconciler that it already trusts:
  - `addons` render to k0s Helm extensions (`internal/k0s/render.go`). To
    re-apply them, the node regenerates `/etc/k0s/k0s.yaml` and lets k0s
    reconcile its chart set to match. The bootstrap already renders this file
    deterministically (AGENTS §7). This node runs k0s from a static configuration
    file (`k0s controller --config /etc/k0s/k0s.yaml`, no dynamic config). So the
    re-apply also restarts `k0scontroller`. On a single-node control plane, this
    causes a brief control-plane interruption. The restart does not touch the
    kubelet and its running pods.
  - `upgrades` is a systemd timer and a policy string. To re-apply it, the node
    enables, disables or reschedules a timer. Nothing about the cluster changes.

The apply is **explicit and operator-triggered**. There is no reconcile loop and
no drift detection. Nothing acts on the node between one `cctl apply` and the
next. This line keeps decision 11 intact. The behavior is "re-apply the safe
delta, now, because I asked". It is not "watch the node and correct it toward a
file". Corium still does not manage the fleet. It answers a bounded question again
when the operator asks it again.

The node must compare against something true, and not against the current
content of the configuration file. So the node records the document that it last
applied, in an extension of the `node.json` that already records what the node was
made into. A second identical apply is then a no-op, as AGENTS §7 requires. The
diff is against the intent that the node actually runs.

`k0s.patch` (the escape hatch) was **originally excluded** from the safe subset.
The reason was that a patch can rewrite any part of the k0s configuration,
including important parts such as `cluster`. Corium cannot inspect a patch and
tell a safe patch from one that redefines the cluster. The gate treated the patch
as immutable and sent a node whose patch changed to `cctl reset`.

**Amended 2026-09-22: `k0s.patch` is now in the safe subset.** A reconcile
regenerates `/etc/k0s/k0s.yaml` and lets k0s reconcile against it, as for
`addons`, with the same controller restart. This is what lets a day-two change,
such as an OIDC `extraArgs` for the API server, take effect without a reset.
Corium still does not judge whether a patch is safe or redefines the cluster, and
it does not need to. On day two, the patch carries the same contract as at
bootstrap. There it is "passed through without interpretation", and "a patch that
breaks the cluster is the operator's to own" (`internal/config/types.go`,
`docs/reference.md` §3.15). An operator who uses the escape hatch owns its result
on day two, for the same reason as on day one. Everything that Corium *models*
stays immutable: `role`, `cluster`, `network`, `storage`, and the rest. Corium
guarantees the meaning of these fields. The escape hatch is, by definition, the
part that Corium does not guarantee. So Corium never had a guarantee about it to
protect with a reset.

`api` is left out of the first cut on purpose, for a specific reason. If
`api.enabled: false` arrived over the API, the node would have to shut down the
same surface that the request arrived on. If the node gets the order of these
steps wrong, the operator locks themselves out of a node that is otherwise
healthy. It is a candidate for a later revision, behind a guard that keeps the API
open long enough to confirm that the caller meant it.

## Consequences

This is a second deliberate crack in "provision, don't manage". The first was the
crack that ADR 5 made in "the API has no shell". This record names it as a crack,
and does not pretend that the line held whole. The crack is narrow. The node
re-applies only fields whose reconciler is k0s or systemd, only when asked, and
never a field that changes what the node is.

The control-plane restart is real, and this record states it plainly. An add-on
change restarts `k0scontroller`. On a single node, the API server is unavailable
for a short time. On an HA control plane, the controllers restart one at a time,
and the VIP covers the gap. This is cheaper than `cctl reset` by every measure,
but it is not free. Before it restarts anything, `cctl apply` names what it is
about to restart, as `cctl reset` names what it is about to erase.

The recorded last-applied document is new state under `/var/lib/corium`. It is
not a secret itself, but a document can carry `secretFrom`-style references. So
the node writes it with the same care as the rest of that directory, and never
logs it. Like the rest of `/var`, it is seeded once and survives an OS upgrade.
Because of this, a node that an upgrade brings into this feature can still compare
against what it was built with.

The node refuses the removal of an add-on and does not reconcile it. This record
settles that decision now and does not defer it. k0s Helm extensions install a
chart from the configuration, but they do **not** uninstall a chart that is
dropped from it. The release stays running, and Corium already documents this
(`docs/features.md`, "Removing add-ons"). A node that re-rendered its k0s.yaml
without the chart and reported the removal as done would report something false.
The `cert-manager` that an operator thinks they removed is still there, with its
CRDs and webhooks. So the gate treats a dropped chart as unreconcilable and
refuses the apply. The refusal names the chart and points to
`kubectl delete chart`, the k0s-native way to remove a release. Adding a chart
and changing a chart stay reconcilable. A change is a version or values bump,
which k0s does apply on the running node. Only removal is refused.

The apply path shares the renderer and the validator of the bootstrap. So if the
node refuses a document at boot, it also refuses that document at apply. The same
input produces the same `/etc/k0s/k0s.yaml`, at first boot or on the tenth apply.
There is no second code path for configuration, so there is no second place where
the two can drift apart.

## Alternatives considered

**Per-capability day-two verbs, instead of re-applying the document.** Model each
safe day-two action as its own explicit API surface and `cctl` verb, for example
`cctl addon add/remove/list` and `cctl upgrades set`. ADR 5 uses this model for
SSH keys, with `cctl access ssh` and not the config schema. The configuration
document then stays only a provisioning record: written once, read once, never
re-applied. This is the more conservative shape, and it has real merit. It is
honest that these are separate day-two operations, and it does not imply that the
whole document is live. It needs no last-applied state to diff against, because
each verb carries exactly the change that it makes. And it follows the precedent
that ADR 5 set for this class of problem, where day two must change one narrow
thing. There, the answer was a dedicated surface and *not* the config endpoint.

This record offers it as the alternative, and does not choose it as the primary
design, for two reasons. First, it multiplies surface. Every safe field becomes
its own endpoint, its own role check, its own command and its own documentation.
The gated apply reuses the one surface, schema and validator that the node already
has. Second, it splits the source of truth. After `cctl addon add`, the running
node and its `/etc/corium/config.yaml` describe different nodes. No single
document then says what this machine is. The whole `corium:` block exists to give
this property, and `cctl reset` and the config endpoint were built to protect it.
The gated apply keeps the document authoritative, and adds the behavior of one
verb to a surface that already exists.

The two options do not exclude each other, and the boundary between them is the
reason to record both. The diff-and-classify machinery can prove more fragile than
the surface that it saves. If Corium keeps getting wrong the judgement between a
safe change and an unsafe one, the capability verbs are the fallback. They never
have to make that judgement: a verb that only adds an add-on cannot be tricked
into changing a cluster.

**A broad reconcile: re-apply the whole document, immutable fields included.**
Rejected outright. This is the mismatch that ADR 4 is built to prevent, presented
as a feature. If an operator can rewrite the role or the cluster of a node under a
running kubelet, the configuration and the behavior of that node are two different
facts. No amount of care in the apply path makes that safe.

**A background reconcile loop that watches `config.yaml` and corrects drift.**
Rejected, because it is the fleet-management posture that decision 11 declines,
reached from the other direction. A node that steers itself toward a document is a
managed node. It brings back a class of surprise, such as a change that lands at
3 a.m. because someone edited a file. The "explicit, operator-triggered" rule
exists to keep that out. Corium re-applies when asked. It does not watch.

**Status quo: `cctl reset` remains the only way to change anything.** Rejected as
the general answer, because the proportion is wrong. The 409 sends an operator who
wants a newer chart version to a command that destroys their cluster. On a single
node, `reset` has no gentler meaning. But this record keeps reset, and keeps it
correct, for the immutable fields. Reset stays the *only* way to change what a
node is. This ADR narrows what "everything" meant. It does not open the door that
reset guards.
