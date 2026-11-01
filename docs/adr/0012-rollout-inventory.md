# 12. A controller's version check accounts for the kubelets it serves

Status: accepted

Amends [ADR 10](0010-kubernetes-version-axis.md) §6 and §7, and adds decision 17.

## Context

`cctl k8s upgrade` moves the nodes the operator names in `--controllers` and
`--workers`, and nothing else. A worker left out of `--workers` stays on the old
minor. That is legal today — a worker may be one minor behind its controllers —
so the rollout succeeds and says so.

The next minor is where it breaks. Controllers move from 1.36 to 1.37, the
forgotten worker is still on 1.35, and the cluster is now two minors apart, which
k0s does not support. Nothing refuses it: `CheckSkew` compares a node with
itself, and the preflight added in #85 only asks the nodes it was given. The
worker cannot catch up in one step either, since it may only move one minor.

The data needed to see this exists, but not where Corium looks. Every kubelet
reports its version in `Node.status.nodeInfo.kubeletVersion`. A controller holds
admin credentials locally and already uses them to cordon and drain itself
(`internal/lifecycle`). `cctl` holds none, by design.

Controller-only nodes run no kubelet and register no `Node` object
([k0s FAQ](https://docs.k0sproject.io/head/troubleshooting/FAQ/)), so a forgotten
controller is not visible this way. This record is about workers.

## Options

**A. `cctl` reads `Node` objects itself**, from a `--kubeconfig` flag or by
fetching one with `cctl kubeconfig`. No API change, and the list is compared
where the rollout lives. But fetching the admin kubeconfig is the most
privileged call the API has, and logged as such; doing it implicitly to run a
check is wrong, and requiring a flag makes the safety check opt-in on the path
where the operator already forgot something. It also makes `cctl` a Kubernetes
client, which `healthy` in `rollout.go` deliberately avoids. And it protects
`cctl` only: `cctl apply` of a new `kubernetes.version` on a controller is not
checked.

**B. A new cluster-scoped route on controllers**, listing kubelets and their
versions. No credentials leave the node. But it is the first route whose answer
is about other machines, which ADR 4 rules out in its first section ("every one
of them scoped to the machine answering the call"), and it is a building block
for the inventory the non-goal declines. Same gap as A for `cctl apply`.

**C. The controller's own skew gate counts its kubelets.** Before a controller
accepts a version (`POST /v1/kubernetes/check`, `POST /v1/kubernetes`, and a
day-two apply), it reads the kubelet versions with `k0s kubectl get nodes` and
refuses a target minor that would leave any kubelet more than one minor behind,
naming those nodes. This runs at apply time, not in `PlanReconcile`, which stays
offline per AGENTS §7. The answer is still
about the node asked — "can *I* move to 1.37?" — and k0s's rule is about exactly
that node and the kubelets it serves.

**Warn or refuse.** A worker left behind by one minor is a state k0s supports,
and a rollout halted halfway produces it legitimately; refusing it would break
`--workers` subsets, which are a normal way to stage a rollout. A controller
move that would put a kubelet two minors behind is never supported. So: refuse
the second, warn on the first.

## Decision

Option C, with a warning in `cctl`.

1. **A controller refuses a minor that would strand a kubelet.** If the target
   minor is more than one ahead of any kubelet's, the controller returns
   `ErrSkew` and names the nodes and their versions. A controller that cannot
   reach the apiserver (`lifecycle.CanReachCluster`) skips the check and logs a
   warning; a bootstrap is never blocked on it.
2. **The check response carries the lagging kubelets**: the name and version of
   each kubelet below the target minor. It is part of the answer to "would you
   accept this version?", not a listing route.
3. **`cctl k8s upgrade` warns** for every lagging kubelet whose name matches no
   listed node's hostname, before anything moves: it will stay behind, and the
   next minor will be refused until it catches up. `--dry-run` shows the same.
4. Patch moves within a minor are unaffected.

## Consequences

- The rule is enforced where k0s's skew rule lives, for every path to a version
  change, including `cctl apply` and a hand-written `curl`.
- A controller's version check now depends on the apiserver. The fallback in 1
  keeps a broken control plane from being unable to upgrade out of its problem,
  at the cost of the check being best-effort there.
- Matching kubelets to `cctl` addresses goes through the hostname the node
  reports, which is the name it registers under (`lifecycle.Manager.Node`).
- Forgotten controllers stay undetected. If that matters, it is a separate record.

## Alternatives considered

Options A and B above. A could come back as an optional `--kubeconfig` for
operators who want the comparison without a controller in the list, but it adds
nothing C does not already enforce.
