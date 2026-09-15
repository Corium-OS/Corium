# Corium

An immutable, container-native Linux distribution that boots into a Kubernetes node.

Corium is [Fedora bootc](https://docs.fedoraproject.org/en-US/bootc/) with
[k0s](https://k0sproject.io/) baked into the read-only system and a small declarative
configuration layer on top of cloud-init.

> **Status: early.** The architecture is settled, the implementation is not. Do not run this
> anywhere you care about yet.

---

## What it looks like

A complete single-node Kubernetes cluster:

```yaml
#cloud-config
corium:
  role: single
```

A worker joining an existing one:

```yaml
#cloud-config
corium:
  role: worker
  join:
    token: <k0s join token>
```

That is the whole interface for the common case. Everything else has a default, and every
default is overridable. See [`docs/examples/`](docs/examples/) for the full surface,
including the escape hatches.

---

## The idea

Three properties, chosen together because each one reinforces the others.

**The operating system is a container image.** Corium is built with a `Containerfile`, pushed
to a registry, and versioned by digest. You inspect it with `podman`, scan it with the
scanner you already run, promote it between environments by moving a tag, and roll it back
atomically. There is no separate image-building toolchain to learn, because the one you use
for your applications already works.

**Kubernetes ships with the OS.** The k0s binary lives in the read-only `/usr`. Upgrading
Kubernetes means booting a new OS image — one version axis, one upgrade mechanism, one
rollback path. A node is a disposable artefact rebuilt from a digest, not a machine that
accumulates state.

**Configuration is cloud-init, where cloud-init exists.** Not a bespoke API, not a new config
language: the mechanism every hypervisor and cloud already speaks. Corium adds a `corium:`
block that collapses the tedious parts into a handful of keys, and gets out of the way the
moment you need something it did not anticipate.

Where cloud-init does not exist — bare metal without a seed device, PXE, an appliance shipped
preconfigured — the same configuration is read from a file, the kernel command line, or a
default baked into the image. One schema, four ways in.

## What it is not

Corium is not a fleet manager — it provisions nodes and stops there. It does not fork or
patch k0s. It does not invent a configuration language. Those are deliberate omissions: the
project is an opinionated integration, and its value is in what it declines to do.

## Prior art, honestly

[Talos Linux](https://www.talos.dev/) is the dominant option in this space and is excellent;
it is also an API-driven system with no shell and its own configuration model, which is a
real commitment. [Kairos](https://kairos.io/) covers similar ground — immutable OS, pluggable
Kubernetes distribution, cloud-init configuration — but deliberately builds its own A/B
partition scheme on top of an arbitrary base distribution rather than using the OSTree/bootc
lineage.

Corium's bet is narrower and specific: that for teams already living in OCI registries and
GitOps, an operating system that *is* an image — built, signed, scanned, and promoted like
every other image they ship — is worth more than a bespoke mechanism, however good.

## Where a node's configuration comes from

Sources are tried in order, most specific first, and the first one that answers wins:

| Source | For |
|---|---|
| `/etc/corium/config.yaml` | An operator's answer for this machine |
| cloud-init | Every cloud and hypervisor: NoCloud, ConfigDrive, EC2, Azure, GCE, OpenStack, Hetzner, VMware |
| `corium.config=` on the kernel command line | PXE and netboot, where the command line is all you control |
| `/usr/share/corium/config.yaml` | A default baked into a derived image |

A source that fails for any reason other than being absent stops the search. An unreachable
config URL means your intent is unknown, and falling through to a baked-in default is how a
node silently joins the wrong cluster.

## Installable artefacts

`make artefacts` produces, via `bootc-image-builder`:

| Artefact | For |
|---|---|
| `qcow2` | Proxmox, KVM, libvirt |
| `raw` | Bare metal, and most clouds' import paths |
| `anaconda-iso` | Interactive or kickstarted bare-metal installs |

## Documentation

- [**Quick start**](docs/quickstart.md) — build an image, boot a node, get a cluster
- [**Feature support**](docs/features.md) — what is modelled, what passes through to k0s, what is out of scope
- [**Configuration reference**](docs/reference.md) — every field, and what Corium does with it
- [`AGENTS.md`](AGENTS.md) — contributor and agent guidelines, architectural decisions
- [`docs/examples/`](docs/examples/) — annotated configuration examples
- [`docs/adr/`](docs/adr/) — architecture decision records

## Licence

MIT. See [`LICENSE`](LICENSE).
