# 11. The management API is on by default, and enrolment closes at bootstrap

Status: accepted

Amends decision 11 and [ADR 4](0004-management-api.md).

## Context

[ADR 4](0004-management-api.md) put the management API behind an explicit
`api.enabled`. Decision 11 states the rule: the API is node-local, off by
default, and a node that it has not claimed is in no cluster. ADR 4 argued for
the off-by-default half in one sentence, under *Alternatives considered*:

> **An always-on API with no way to switch it off.** Fewer states, one less key,
> and a fleet where `cctl` always works. Rejected because a privileged daemon
> listening on every machine is a reasonable thing to veto, and because a node
> that has been running happily for a year should not acquire a new listening
> port by being upgraded.

That reasoning is about a node **in service**. It says nothing about a node that
was just installed and that nobody has told what it is. This record is about that
node.

### The state the default produces

A machine installed from the published ISO with no cloud-init seed reaches a
state that is coherent and supported, but unusable:

- `corium-bootstrap` runs, finds no configuration, logs
  `no corium configuration found, leaving node unconfigured`, and writes no
  marker.
- `corium-apid` starts, `claimFromConfiguration` gets `source.ErrNotFound`, and
  the daemon exits 78. Nothing listens on 7443.
- No account exists. Corium creates no default user (decision 7 leaves accounts
  to cloud-init), and `PermitRootLogin` is `no`.
- The console prints `This node is not part of a cluster` above a login prompt
  that nobody can answer.

The machine is healthy and answers ping, but there is no way in. To recover it,
someone needs physical access: a `CIDATA` stick and a reboot, or a live system
and a write into `/var/lib/cloud/seed/nocloud` with the right SELinux label.

The project's own answer to this is `deploy/appliance/`. It bakes
`api.enabled: true` into `/usr/share/corium/config.yaml`. That answer works and
is documented, but it is a derived image that nobody publishes. The recipe exists
because the published ISO cannot do what the recipe does. That is the definition
of a default in the wrong place.

### What the ISO makes different

ADR 4 was written when every route onto a node carried configuration with it:
cloud-init on a hypervisor, a metadata service on a cloud, a seed written by hand
in a rescue system. In all three cases, "no configuration" means that somebody
made a mistake. The installer ISO broke that assumption. It is the one artefact
that installs a complete, running node but carries nothing about what the node
is. That is what *unattended* means. And it is the artefact that bare metal is
supposed to use.

So the question that ADR 4 answered is not the question that the ISO asks. ADR 4
asked whether to let a node in service grow a port. This record asks whether to
make a node that was never configured reachable at all.

## Decision

### 1. `api:` absent means the API runs

The default arm of `Mode()` flips. An absent `api:` block no longer resolves to
`disabled`. It resolves to a new mode, **`enrollable`**. In this mode, the daemon
serves and the enrolment route is open. The pairing code on the console
authenticates the route, exactly as in mode C today.

`api.enabled: false` keeps its meaning. It stays the way to have no daemon and no
port. It is now the only way, and that is the whole substance of this change.

### 2. `enrollable` serves, but does not hold the bootstrap

ADR 4 did not need this distinction, but this record does. Today `maintenance`
means two things at once: serve the enrolment route, **and** hold the bootstrap of
the node until an operator claims it. A flip of the default without a split of
these two meanings makes every existing single-line configuration hold its
bootstrap forever. Each such node waits for an operator who is never going to
come.

So the two meanings separate. `HoldsBootstrap()` stays exactly what it is: true
only for `maintenance`. An operator still asks for `maintenance` by writing
`api.enabled: true` with no CA, as
[`awaiting-config.yaml`](../examples/awaiting-config.yaml) does. `enrollable`
serves the port and lets the bootstrap continue. A node with `role: single` and
no `api:` block behaves on first boot exactly as it does today. In addition, it
answers on 7443 while it gets there.

### 3. Enrolment closes when the node bootstraps

The central invariant (a rule that is always true) of ADR 4 is that a node is
never valuable and unclaimed at the same time:

> **Letting an unenrolled node join the cluster and enrol later.** [...] it costs
> a node that is simultaneously valuable and unclaimed — running workloads,
> holding cluster credentials, and waiting to obey whoever reaches an
> unauthenticated port first. The pairing code was invented to defend that state.
> Deleting the state is better than defending it.

This record does not change that invariant, and the invariant sets the limits of
this change. The node serves the enrolment route **only while
`/var/lib/corium/bootstrapped` is absent**. A node that bootstrapped holds cluster
credentials and can run workloads. It refuses to be claimed, whatever its
configuration says. To take over such a node, an operator still needs
`cctl reset`, as today.

This is stricter than the current code, which gates enrolment on
`store.Enrolled()` alone. Today an operator can claim a node that bootstrapped and
later received `api.enabled: true` by hand. After this record, the operator
cannot. This is a deliberate tightening, and it is the price of a default that is
on.

### 4. The daemon runs where it can do something

The three rules above leave one state with no purpose: a node that bootstrapped
from its own configuration, was never claimed, and names no operator CA. Rule 3
closes its enrolment route, and it has no CA to authenticate anyone with. Without
a further rule, it listens on 7443 for the rest of its life and refuses every
request. That is attack surface with no capability attached, and that is what the
veto in ADR 4 was actually about.

So `corium-apid` serves when one of these conditions is true, and exits 78
otherwise:

- **the node is still enrollable**: it is not bootstrapped, and it is not
  `api.enabled: false`, or
- **the node has an owner**: it adopted an operator CA, by enrolment or from
  `api.operatorCA`.

So every node is either claimable or already answerable, and no node carries a
port that can do nothing. "On by default" describes what an operator must write.
It does not promise that a socket exists on every machine in every state.

### 5. The pairing code stays the default, and `insecure` stays opt-in

Authentication does not change. A node with no configuration creates a pairing
code, prints it on the console, and requires it. `api.insecure: true` still
removes the code for a bench or a provisioning network, and still logs a warning.
The node still records the claim method as `ClaimedOpenly`, and `cctl status`
reports it.

This is the line between this record and `deploy/appliance/`. The appliance sets
`insecure: true`, and its README states the cost directly: *the first client to
reach port 7443 owns the machine for the rest of its life*. This record makes the
**port** the default, not the **openness**. An attacker on the network still
cannot claim a node if they cannot see its console.

### 6. A document that names no role still needs an explicit `api:`

An operator can omit `role` only when the API is on. This rule exists so that
validation refuses a configuration with a typo in `role:`, and does not produce a
node that waits forever. A flip of the default can silently remove this rule.

It does not remove it, because the two cases are different in kind. Validation
runs on a document, and the ISO case has no document at all. A document that
names no role and has no `api:` block is still refused, with the same error as
today. Writing a configuration that says nothing is a mistake. Shipping no
configuration is a choice, and this record serves that choice.

## Consequences

**A node installed from the ISO is enrollable.** It prints a pairing code on its
console and waits, with no time limit, in no cluster, and with no change to
itself. `cctl enroll <address> --code <code> --config node.yaml` makes it a node.
That is the point of this record.

**Some nodes in service acquire a listening port on upgrade.** ADR 4 declined to
pay this cost, and this record now pays part of it. A node that was claimed, or
that names an operator CA, already had the daemon. A node that bootstrapped from
its own configuration and was never claimed does not gain one, because rule 4
makes the daemon exit. What gains a port is the window between boot and bootstrap
on every node. That window is about one minute, and its pairing code closes
permanently when k0s comes up.

That window is new and it is real. The console requirement limits it. An attacker
who can reach 7443 during that minute still needs the code, and the code is on a
channel that the attacker does not have.

**`deploy/appliance/` loses most of its reason to exist.** What remains of it is
`insecure: true` and `awaitConfig: true`, the zero-console posture. This record
deliberately does not make that posture the default. The recipe stays. Its README
gets a line that says the port is now default, and only the openness is not.

**One more state to keep in mind.** `Mode()` gets a fourth value, and "is the API
on?" becomes two questions, not one. That is the honest cost of rule 4. The
alternative was a dead socket on every node.

**Reverting is a configuration change, not a rollback.** A fleet that wants the
old posture writes `api.enabled: false`. There is no image-level switch. Adding
one creates a second authority over the same question.

## Alternatives considered

**Leave the default off and publish the appliance image.** The smallest change:
no ADR, no code, one more artefact in the release. Rejected for two reasons. The
answer to "how do I install on bare metal" then depends on which of two images
you pulled. And the posture of the appliance is `insecure: true`, which is not a
posture to publish as the bare-metal path.

**On by default only while the node is unbootstrapped.** This option came first,
and it is almost rule 4, reached from the other side. It differs on a node that
was claimed and then bootstrapped. That node has an owner and must keep
answering, so the rule must mention ownership anyway. It is clearer to state both
halves than to state one and patch it.

**On by default, with enrolment staying open until the node is claimed rather
than until it bootstraps.** Strictly more useful, because an operator can still
adopt a node in service that nobody owns. Rejected because of the invariant of
ADR 4, which is the load-bearing one in that record. This option recreates
exactly the node that is valuable and unclaimed. It does so by default, not by an
operator's choice.

**A timeout: listen for N minutes after boot, then stop.** This makes the
exposure window smaller. But it creates a state where a machine is neither
configured nor reachable, and recovery again needs a physical visit. This record
exists to remove that problem. For a configured node, bootstrap already limits
the window. For an unconfigured node, the whole point is that it waits.

**Make `insecure` the default too, matching the appliance.** It makes bare metal
truly zero-touch, which is the one thing the pairing code costs. But it is the
model that ADR 4 argued against at length, and offered as an opt-in with the
argument still valid. Nothing here weakens that argument. A default that gives a
machine to whoever reaches it first is not a default.
