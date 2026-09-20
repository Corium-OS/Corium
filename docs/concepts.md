# Concepts

The model underneath everything else. Read this once and the
[quick start](quickstart.md) and [reference](reference.md) stop being
surprising.

---

## The operating system is an image

Most Linux systems are assembled on the machine: install, add packages, edit
configuration, apply updates. Two machines that started identical drift apart,
and neither matches what anyone intended.

Corium is built once, as an OCI image, with an ordinary `Containerfile`. That
image *is* the operating system — pushed to a registry, addressed by digest,
installed onto machines that are identical by construction.

You already own the tools: inspect it with `podman`, scan it with your scanner,
promote it by moving a tag. And because two builds share most of their layers,
upgrading transfers only the difference — 214 MB measured on a 2.3 GB image.

The mechanism is [bootc](https://bootc.dev/), which boots a container image as
a system on top of OSTree.

## The filesystem contract

Nearly every mistake in this kind of system comes from getting this wrong.

```
                  ┌──────────────── the image ────────────────┐
   boot image A   │  /usr    binaries, systemd units, k0s     │  read-only
                  └───────────────────────────────────────────┘
                            │
      upgrade               │  /usr replaced wholesale
      to image B            ▼
                  ┌──────────────── the machine ──────────────┐
                  │  /etc    three-way merged: your edits win │  writable
                  │  /var    untouched: k0s state, etcd, logs │  writable
                  └───────────────────────────────────────────┘
```

**`/usr` is the image.** Nothing writes here at runtime, and all of it is
replaced when you boot a new image. That is why the k0s binary lives here, and
why upgrading Kubernetes means upgrading the OS.

**`/etc` is merged.** OSTree reconciles the image's defaults against the
machine's edits. A file you have edited stops tracking the image's version of
it — convenient, and worth remembering.

**`/var` is yours.** The image never touches it. This is what makes upgrades
safe and rollbacks partial: rolling back the OS does not roll back what the
cluster wrote.

One corollary catches people out: you cannot ship data in `/var` by baking it
into the image. It is seeded once at install and never again. State directories
are declared in `tmpfiles.d` instead.

## First boot, exactly once

A generic image has to become *this* node. That is `corium-agent`, run once.

```
  ┌─ configuration sources, most specific first ─┐
  │  /etc/corium/config.yaml                     │   an operator, this machine
  │  cloud-init                                  │   the platform
  │  corium.config= on the kernel command line   │   PXE
  │  /usr/share/corium/config.yaml               │   a default in the image
  └──────────────────┬───────────────────────────┘
                     │  first source that answers wins
                     ▼
              ┌─────────────┐
              │ corium-agent│  validate everything before touching anything
              └──────┬──────┘
                     │
        ┌────────────┼─────────────┬──────────────────┐
        ▼            ▼             ▼                  ▼
   hostname     /etc/k0s/     k0s install        marker written to
   (stable)     k0s.yaml      + start            /var/lib/corium
                                                        │
                        next boot ──────────────────────┘
                        sees the marker and does nothing
```

Once is the operative word. Re-running cluster bootstrap on a node that already
joined destroys data, so the marker is written last and checked first — and it
lives in `/var`, which survives upgrades.

The agent fails loudly and early. It validates the whole configuration before
changing anything and reports every problem at once, because a node that stops
with a reason in the journal beats one that half-joins a cluster and looks
healthy.

Two details follow from the same reasoning. A node's name is **derived from the
machine ID**, not randomised: a random name would register a new node on every
reboot and leave the old one behind. And a source that fails for any reason
other than being absent stops the search, because falling through to a
baked-in default when your intent is merely unreachable is how a node joins the
wrong cluster.

## Configuration is cloud-init, not a new API

The `corium:` block rides inside an ordinary cloud-config — the mechanism every
cloud, hypervisor and PXE setup already speaks. `role` is the only required
field.

Everything the schema does not model stays reachable through `k0s.patch`,
applied verbatim. The rule the project holds itself to: **no k0s feature is
unreachable; some have no shorter name.** See
[feature support](features.md).

There *is* a management API, and it does not contradict that heading, because
the two answer different questions. Cloud-init describes what a machine should
become, once, before it exists. The API answers what a running machine is and
does the handful of things an operator needs afterwards — read it, restart k0s,
upgrade it, take it out of service. It cannot write a `corium:` block, and a
node that needs different configuration is reprovisioned rather than edited.

It is off unless a node's configuration asks for it. See [cctl](cli.md), and
[ADR 4](adr/0004-management-api.md) for why it is shaped that way.

## Kubernetes ships with the OS

[k0s](https://k0sproject.io/) is a single static binary with no host
dependencies that keeps its state under `/var/lib/k0s` — which fits the
filesystem contract exactly: binary in `/usr`, state in `/var`.

One version axis follows. The image determines the Kubernetes version, so
nothing can move it independently, and upgrading a cluster is
[rolling a new image](upgrades.md) rather than a separate procedure.

## What Corium is not

It does not manage fleets, fork k0s, or invent a configuration language. Those
omissions are deliberate: the project is an opinionated integration, and much
of its value is in what it declines to do.

The management API is not an exception to the first of those. It is one daemon
per node, answering for that node, with no registry, no inventory and nothing
that reconciles. Upgrading a cluster is a `cctl` loop over addresses you
supplied, running on your machine — the sequencing lives with the operator
rather than on any node. The reasoning for each is in
[feature support](features.md#out-of-scope).

---

## Next

- [Quick start](quickstart.md) — put this into practice on a real node
- [Examples](examples.md) — a complete document for each common node shape
- [Configuration reference](reference.md) — every field, and what Corium does with it
- [Comparison](comparison.md) — how this model differs from Talos, Kairos and the rest
