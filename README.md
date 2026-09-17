<p align="center">
  <img src="https://raw.githubusercontent.com/Corium-OS/Corium/main/docs/assets/logo.png"
       alt="Corium" width="120" height="120">
</p>

<h1 align="center">Corium</h1>

<p align="center">
  An immutable, container-native Linux distribution that boots into a Kubernetes node.
</p>

<p align="center">
  <a href="https://github.com/Corium-OS/Corium/releases/latest"><img src="https://img.shields.io/github/v/release/Corium-OS/Corium?include_prereleases&sort=semver&logo=github&label=release" alt="Latest release"></a>
  <a href="https://github.com/Corium-OS/Corium/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/Corium-OS/Corium/ci.yml?branch=main&label=CI&logo=github" alt="CI"></a>
  <a href="https://github.com/Corium-OS/Corium/actions/workflows/image.yml"><img src="https://img.shields.io/github/actions/workflow/status/Corium-OS/Corium/image.yml?branch=main&label=image&logo=podman&logoColor=white" alt="Image build"></a>
  <img src="https://img.shields.io/badge/Kubernetes-1.36-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes 1.36">
  <img src="https://img.shields.io/badge/k0s-v1.36.4%2Bk0s.0-0F1689" alt="k0s v1.36.4+k0s.0">
  <img src="https://img.shields.io/badge/Fedora-bootc-51A2DA?logo=fedora&logoColor=white" alt="Fedora bootc">
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Corium-OS/Corium?logo=go&logoColor=white&label=Go" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Corium-OS/Corium?color=blue" alt="License: MIT"></a>
</p>

Corium is [Fedora bootc](https://docs.fedoraproject.org/en-US/bootc/) with
[k0s](https://k0sproject.io/) baked into the read-only system and a small declarative
configuration layer on top of cloud-init.

> **Status: 0.x.** The architecture is settled and the path works end to end — a node boots,
> joins, upgrades, drains itself before rebooting, rolls back when it comes up broken, and
> refuses an image that is not signed. Two things are not settled. The configuration surface
> can still change between minor releases, and the testing is narrower than it looks:
> everything has been verified on virtual machines, none of it on physical hardware. Pin a
> version, read the [changelog](CHANGELOG.md), and do not put anything on it you would miss.

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

The [comparison](docs/comparison.md) covers these and the rest properly, including the
cases where you should pick something else.

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

`mise run artefacts` produces, via `bootc-image-builder`:

| Artefact | For |
|---|---|
| `qcow2` | Proxmox, KVM, libvirt |
| `raw` | Bare metal, and most clouds' import paths |
| `anaconda-iso` | Interactive or kickstarted bare-metal installs |

You only need to build these if you have changed the image. Every release
publishes the ISO and the qcow2 ready-made and signed: see
[**Downloads**](docs/install/downloads.md) for where they live, why nothing is
attached to the release itself, and how to check what you got.

## Documentation

- [**Comparison**](docs/comparison.md) — Talos, Kairos, Flatcar, and when not to use Corium
- [**Concepts**](docs/concepts.md) — how it works and why it is shaped this way
- [**Quick start**](docs/quickstart.md) — build an image, boot a node, get a cluster
- [**Downloads**](docs/install/downloads.md) — where the ISO and the qcow2 are published, and how to verify them
- [**Proxmox**](docs/install/proxmox.md) — a node on a Proxmox host, start to finish
- [**HA cluster**](docs/install/ha-cluster.md) — three controllers sharing a virtual IP
- [**Upgrades**](docs/upgrades.md) — moving a node to a new image, rolling back, upgrading a cluster
- [**Software RAID**](docs/raid.md) — mdadm arrays on a node's spare disks, and where a RAID root stands
- [**Feature support**](docs/features.md) — what is modelled, what passes through to k0s, what is out of scope
- [**Configuration reference**](docs/reference.md) — every field, and what Corium does with it
- [**Changelog**](CHANGELOG.md) — what each release changed, and what is known to be broken
- [`AGENTS.md`](AGENTS.md) — contributor and agent guidelines, architectural decisions
- [`docs/examples/`](docs/examples/) — annotated configuration examples
- [`docs/adr/`](docs/adr/) — architecture decision records

## Licence

MIT. See [`LICENSE`](LICENSE).
