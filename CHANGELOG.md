# Changelog

Everything a user would notice, release by release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the version
numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Corium is below 1.0, and semantic versioning reserves 0.y.z for initial
development: a minor release may change or remove configuration that an
earlier one accepted. When that happens it is listed under **Changed**, with
what to do about it. Which image tag to follow, and what each one promises,
is covered in [upgrades](docs/upgrades.md#choosing-what-to-track).

## [Unreleased]

The first release. Corium boots a machine straight into a Kubernetes node: the
operating system is a container image, k0s lives in the read-only `/usr`, and
the node is described in cloud-init.

### Added

- **A declarative `corium:` block over cloud-init.** A single-node cluster is
  two lines of YAML, and every other field has a default. Roles are `single`,
  `controller`, `controller+worker` and `worker`.
- **Four places a node can read its configuration from**, tried most specific
  first: `/etc/corium/config.yaml`, cloud-init (NoCloud, ConfigDrive, EC2,
  Azure, GCE, OpenStack, Hetzner, VMware), `corium.config=` on the kernel
  command line, and `/usr/share/corium/config.yaml` baked into a derived
  image. A source that fails for any reason other than being absent stops the
  search, so an unreachable config URL cannot fall through to a default and
  join the wrong cluster.
- **Networking**: pod and service CIDRs, and a choice of kube-router (the
  default), Calico, or no CNI at all when you install your own.
- **Storage** picked from the role — SQLite for a single node, etcd for
  anything that can grow a second controller — and overridable either way.
- **A highly available control plane.** `ha.enabled` brings up keepalived with
  a virtual IP, a virtual router ID, unicast peers, and a VRRP password that
  can be read from a file instead of written into the configuration.
- **Joining without a pre-shared secret.** `join.tokenFrom` reads the token
  from a file or an HTTPS endpoint, and `waitFor` lets a node boot before the
  token exists and wait for it to appear. Three controllers can be started
  together rather than one after another.
- **Helm charts at bootstrap** through `addons[]`, rendered into k0s's own
  extensions — no Helm binary on the node and no in-cluster operator.
- **Upgrades that replace the whole OS, Kubernetes included.** `bootc upgrade`
  stages a new image, a reboot applies it, and `bootc rollback` undoes it
  without downloading anything. Unattended behaviour is opt-in: `download`
  stages an image and never reboots, `apply` drains the node, reboots into the
  image, and uncordons once k0s is answering. A drain that a pod disruption
  budget refuses cancels the upgrade instead of forcing it.
- **A node that boots a broken image goes back to the one before it**, after
  four failed boots, gated on a health check that k0s is actually serving.
- **A ladder of image tags per release**, so a node chooses how much movement
  it accepts. Prereleases publish only their exact tag, so a release candidate
  never reaches a node following a stable one.
- **Images signed twice, and enforced on the node.** Keyless signing records
  the workflow run in a public transparency log, which is what a human checks.
  A key signature is what a node can require, because a container policy can
  name a public key with no identity matching involved. Enforcement is scoped
  to this repository, so pulls from anywhere else are unaffected.
- **Software RAID on a node's spare disks** — levels 0, 1, 5, 6 and 10, hot
  spares, ext4, xfs or a raw device, mounted by UUID and assembled before k0s
  starts. A disk that already holds data stops the bootstrap rather than being
  overwritten; destroying it is an explicit opt-in.
- **Installable artefacts** from `bootc-image-builder`: `qcow2` for Proxmox,
  KVM and libvirt, `raw` for bare metal and cloud imports, and an
  `anaconda-iso` that installs unattended.
- **The installer ISO and a qcow2 disk image are published with every
  release**, so installing a node no longer requires a Linux host, `sudo`, and
  a privileged container. The qcow2 is what the Proxmox scripts in
  `deploy/proxmox/` take as `DISK_IMAGE`, which until now had to be built
  before they could be used at all. It is too
  large to attach to a GitHub release, so it ships as a signed OCI artifact in
  the same registry: `oras pull`, or `curl` against the registry API for
  anyone without it, and a plain HTTPS link for anyone who would rather click
  than run either. Every route gives the same bytes, and the hash to check
  them against is the one cosign signed rather than a checksum file alongside.
- **Proxmox scripts** that create a single node from a qcow2, a node that
  installs itself from the ISO, and a three-controller HA cluster.
- **An escape hatch at every level**: raw `write_files` and `runcmd`, and a
  verbatim k0s configuration patch that Corium neither validates nor alters.

### Known issues

- **Nothing here has run on bare metal.** Every claim in this release was
  verified on Proxmox virtual machines. The ISO path is the one bare metal
  would take and it installs unattended, but no physical machine has booted
  it.
- **A node whose root filesystem is on RAID reports `degraded`.**
  `bootc-generic-growpart.service` fails permanently on an md root and there
  is nothing to grow, so the failure is cosmetic — but it is permanent. Root
  on RAID is an install-time layout outside the `corium:` surface; see
  [software RAID](docs/raid.md).
- **Air-gapped installs are untested.** k0s supports them and an image bundle
  can be baked in with a `COPY`, but there is no `corium:` field for it and
  nobody has run one.
- **Disk artefacts are verifiable, not reproducible.** The image is signed and
  addressed by digest, so you can check what you build from. The builder that
  turns it into a disk image is pinned to a floating tag, so two runs against
  the same digest may not produce identical bytes.

[Unreleased]: https://github.com/Corium-OS/Corium/commits/main
