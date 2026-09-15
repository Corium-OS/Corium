# Concepts

How Corium works, and why it is shaped this way.

The [quick start](quickstart.md) shows what to type and the
[reference](reference.md) lists every field. This page is the model underneath
both: read it once and the rest stops being surprising.

---

## The operating system is a container image

Most Linux systems are assembled on the machine: a base install, then packages
added, configuration edited, updates applied one at a time. Two machines that
started identical drift apart, and neither matches what anyone intended.

Corium is built once, as an OCI image, with an ordinary `Containerfile`. That
image is the operating system. It is pushed to a registry, addressed by digest,
and installed onto machines that are then identical by construction.

The practical consequences matter more than the philosophy:

- **You already own the tools.** `podman inspect` it, scan it with your
  scanner, promote it between environments by moving a tag, diff two versions.
  There is no bespoke OS build system to learn.
- **Upgrades are incremental for free.** Two builds share most of their layers,
  so moving between them transfers only the difference — measured at 214 MB for
  a 2.3 GB image.
- **Rollback is a boot entry.** The previous image is still on disk, so going
  back downloads nothing.

The mechanism is [bootc](https://bootc.dev/), which deploys a container image
as a bootable system on top of OSTree.

## The filesystem contract

This is the single most important thing to internalise, because nearly every
mistake in this kind of system comes from getting it wrong.

| Path | Belongs to | On upgrade |
|---|---|---|
| `/usr` | The image | Replaced wholesale. Read-only at runtime |
| `/etc` | The machine | Three-way merged: your edits survive |
| `/var` | The machine | Untouched |

**`/usr` is the image.** Binaries, systemd units, defaults. Nothing writes here
at runtime, and everything here is replaced when you boot a new image. This is
why the k0s binary lives here and why Kubernetes upgrades are image upgrades.

**`/etc` is merged.** OSTree compares the image's defaults with what is on the
machine and keeps your changes. The catch is worth knowing: a file you have
edited stops tracking the image's version of it.

**`/var` is yours.** `/var/lib/k0s`, container storage, etcd data, logs. The
image never touches it, which is what makes an upgrade safe and a rollback
incomplete — rolling back the OS does not roll back what the cluster wrote.

A corollary that catches people: you cannot ship data in `/var` by putting it
in the image. It is seeded once at install and never again. State directories
are declared in `tmpfiles.d` instead, so systemd creates them on every boot.

## First boot happens once

A machine boots the image, and something has to turn a generic image into
*this* node: give it a name, write a cluster configuration, start Kubernetes.
That is `corium-agent`, run once by `corium-bootstrap.service`.

Once is the operative word. When it finishes it writes a marker to
`/var/lib/corium/bootstrapped`, and the unit does not start when that marker
exists. Rebooting a node does not re-bootstrap it.

This is not defensive coding for its own sake. Re-running cluster bootstrap on
a node that already joined a cluster destroys data, and `/var` surviving
upgrades is exactly what makes the marker reliable across them.

The agent is also **deliberately loud**. It validates the whole configuration
before touching anything, and reports every problem at once rather than one per
reboot. A node that stops with a reason in the journal is better than one that
half-joins a cluster and looks healthy.

## Configuration arrives from outside

Corium does not ask you to learn a configuration API. It reads a `corium:`
block from a cloud-config — the mechanism every cloud, hypervisor and PXE setup
already speaks.

Because not every machine has cloud-init, the agent looks in four places and
takes the first answer:

```
/etc/corium/config.yaml        an operator's answer for this machine
cloud-init                     the platform's answer for this instance
corium.config= (kernel)        whoever booted it, typically PXE
/usr/share/corium/config.yaml  a default baked into an image
```

The order runs from most specific to most general, and a source that fails for
any reason other than being absent stops the search. Falling through to a
baked-in default when the intended configuration is merely unreachable is how a
node silently joins the wrong cluster.

### Small surface, complete escape hatch

The `corium:` schema models what most clusters need; `role` is the only
required field. Everything it does not model stays reachable through
`k0s.patch`, applied verbatim to the generated configuration.

The rule the project holds itself to: **no k0s feature is unreachable, some
simply have no shorter name.** A patch can even override values Corium
computed, including ones it treats as load-bearing. That is what separates an
escape hatch from a suggestion.

And the document remains an ordinary cloud-config throughout — `write_files`,
`runcmd` and `users` keep working. Corium is a guest in that document, not its
owner.

## A node needs a stable name

Kubernetes identifies a node by its hostname, and two nodes sharing one do not
fail loudly. They take turns overwriting each other's Node object while
everything reports healthy — the worst way for this to go wrong.

So Corium derives a name from the machine ID when the hostname is still generic
(`fedora`, `localhost`). Derived, not random: a random name would change on
reboot, registering a new node every time and leaving the old one behind as a
ghost. Stable identity is the requirement; uniqueness alone is not enough.

## Kubernetes ships with the OS

[k0s](https://k0sproject.io/) is a single static binary with no host
dependencies, and it keeps all its state under `/var/lib/k0s`. That fits the
filesystem contract exactly: the binary belongs in the read-only `/usr`, the
state in the machine-owned `/var`.

One version axis follows from this. The OS image determines the Kubernetes
version, so there is no second mechanism able to move it independently — which
is why [k0s Autopilot is not used](features.md#out-of-scope): it upgrades
Kubernetes by replacing a binary that, here, is read-only.

### Roles

A node is one of four things, and the choice is about where the control plane
and the workloads live:

| Role | Control plane | Workloads |
|---|---|---|
| `single` | yes | yes |
| `controller` | yes | no |
| `controller+worker` | yes | yes |
| `worker` | no | yes |

`single` is the only irreversible one: it cannot gain nodes later. Everything
else can be changed by reprovisioning, which on an image-based OS is the normal
way to change anything.

### High availability without extra infrastructure

An HA control plane needs one address that outlives any single controller. The
usual answer is a load balancer — which has to exist and stay healthy *before*
the cluster it fronts does.

Corium uses k0s's control plane load balancing instead: the controllers run
VRRP among themselves and one holds a virtual IP. Nothing to provision ahead of
the cluster, and failover is the controllers' own business.

Joining controllers need no certificates. They present a token, and k0s hands
them the cluster CA over its join API. No PKI material ever appears in a Corium
configuration.

## Upgrades are image changes

Because Kubernetes ships with the OS, upgrading either means booting a new
image: one mechanism, one rollback path. A node is a disposable artefact
rebuilt from a digest, not a machine that accumulates state.

Nothing happens on its own by default — a Kubernetes node that reboots
unprompted is an outage nobody scheduled. Nodes can be told to stage upgrades
unattended and leave the reboot to you, which makes the maintenance window
short without making it a surprise.

See [upgrades](upgrades.md) for the mechanics.

## What Corium is not

Knowing the boundary is part of the model.

It is **not a fleet manager**: it provisions a node and stops. It does not
track, group or reconcile machines.

It does **not fork or patch k0s**. It configures upstream k0s, so k0s's
documentation is authoritative and its releases are not gated on this project.

It does **not invent a configuration language**. The value of cloud-init is
precisely that it is not new.

These are omissions on purpose. The project is an opinionated integration, and
most of its value is in what it declines to do.
