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

### Added

- **An `api:` block, ahead of the daemon it configures.** The schema and its
  validation land first so the rest can be built against something settled.
  `api.enabled` turns the management API on; `api.operatorCA` and
  `api.operatorCAFrom` name the CA whose client certificates a node will
  accept; `enabled: true` with neither asks for maintenance mode, where a node
  waits to be claimed from the console rather than joining a cluster unclaimed.
  The value is a CA *certificate* and never a key, which is what makes writing
  it in clear in cloud-init safe. See [ADR 4](docs/adr/0004-management-api.md).

  `corium-apid` itself is not written yet, and the block is honest about it
  rather than silently inert: a node that names an operator CA bootstraps as
  before and says in the journal that nothing is serving the API, and a node
  asking for maintenance mode **refuses to bootstrap**, because there is
  nothing to enrol against and joining a cluster unclaimed is the one outcome
  the design rules out. Nodes with no `api:` block are unaffected.

- **The enrolment core behind maintenance mode**, still with no daemon in front
  of it: the pairing code, the attempt limit, and the state a node keeps about
  who owns it under `/var/lib/corium/api/`. A node is claimed once and stays
  claimed across reboots, five wrong codes close enrolment until the next boot,
  and the certificate a node serves with is minted on the node, so that no
  private key is ever carried in a configuration. It adds no dependency: the
  API will speak JSON over HTTP and mutual TLS rather than gRPC, so that an OS
  image does not grow five modules behind a listener running as root.

- **`corium-apid`, the management API daemon, and its systemd unit.** On a node
  nobody has claimed it serves one unauthenticated route — enrolment — and
  prints a pairing code and its certificate fingerprint on the console; the
  node joins no cluster until somebody uses them. Once claimed it restarts and
  requires a client certificate signed by the operator CA, with the role taken
  from the certificate's organisation. A node with no `api:` block runs nothing
  and binds no port.

  Maintenance mode is now real rather than a refusal: `corium-bootstrap.service`
  waits for enrolment instead of failing, with no timeout, because what it is
  waiting for is a person walking to a console.

  There is no `cctl` yet, so enrolment is done with `curl` — the API speaks
  JSON over HTTP, which is half the reason it was chosen. A claimed node serves
  `GET /v1/health` and nothing else; the four management surfaces come next.

- **`cctl`, the operator's client.** `cctl pki init` creates the operator CA —
  and prints its certificate ready to paste into cloud-init — while
  `cctl pki issue --role admin` signs a client certificate for you. The CA's
  private key never leaves your machine, which is the property the whole scheme
  rests on. `cctl enroll <node> --code <code>` claims a node in maintenance
  mode and remembers its fingerprint, so later calls need no flags; omitting
  `--fingerprint` shows what answered and asks you to confirm it against the
  console, and refuses when there is nobody there to ask.

  It is a workstation tool and is not shipped in the OS image. Build it with
  `mise run build`.

- **`cctl status`, and roles that actually gate something.** The first of the
  four management surfaces: what a node is, read-only — role, cluster, the
  booted and staged image with their digests, kernel, k0s version and service
  state, greenboot's verdict, uptime. The digest is the field an incident turns
  on, since a tag says what was asked for and a digest says what booted.

  A field the node could not determine is left out rather than shown as a dash
  or a zero: this is read just before doing something irreversible, and a blank
  is honest where a placeholder invites a guess. A machine provisioned without
  a Corium block says so plainly instead of looking broken, and a report
  survives every tool on the node being missing — which is when it is most
  worth having.

  Every route now names the lowest role that may call it, so a route cannot be
  added without answering the question. A certificate signed by the operator CA
  but carrying no recognised role is authenticated and not authorised: it gets
  a 403 saying how to reissue it, because signing a certificate without naming
  a role is not a way to grant every role.

  `corium-agent` now records what a node was bootstrapped as in
  `/var/lib/corium/node.json`. The API reports from that rather than re-reading
  the configuration, which after an edit describes an intention rather than a
  machine.

- **Services and journals**, the second management surface. `cctl services`
  lists what the API knows about and what each unit is for; `cctl logs` reads a
  journal per unit or across all of them, with `--since`, `--follow` and
  `--unit kernel` for the kernel's own messages; `cctl restart --unit k0sworker`
  cycles k0s.

  Units come from a fixed list rather than being passed through. An API that
  takes a unit name and hands it to `systemctl` can start anything on the
  machine, which is a remote shell with extra steps. Restarting is a shorter
  list still: `corium-bootstrap.service` is readable and deliberately not
  restartable, because re-running it on a node that has already joined a
  cluster destroys data — no certificate can ask for that.

  Reads need `corium:readonly` and restarts `corium:operator`. Worth knowing
  before handing out the former: journals are not sanitised, so whatever any
  software on the node has logged is readable with it.

  Logs stream as newline-delimited JSON, flushed per record, so `--follow`
  shows a line before the request ends. A followed stream is bounded at an
  hour, and a request is capped at 10000 records.

### Fixed

- **A bad `ha.authPassFrom` now says `ha.authPassFrom`.** Every problem with a
  secret source was reported as `join.tokenFrom` whichever key it was reached
  through, which sent you to a line that was not the one at fault.

### Changed

- **Building from source uses [mise](https://mise.jdx.dev) instead of make.**
  `make image` is now `mise run image`, and variable overrides move into the
  environment: `REGISTRY=ghcr.io/you IMAGE_TAG=v0.1.0 mise run push`.
  `mise install` fetches the whole toolchain at the versions the project
  expects, which `make` never did — `make fmt` simply failed if you had no
  goimports. `mise tasks` lists what you can run. Nothing about the published
  images or artefacts changes, and you still need a Linux host with podman to
  build a disk.
- **`mise run k0s-lock vX.Y.Z+k0s.N` repins the Kubernetes version.**
  `build/k0s.lock` has always documented a command to refresh it; that command
  now exists.

## [0.1.0] - 2026-09-17

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
  anyone without it, and a CDN link for anyone who would rather click than run
  either. Every route gives the same bytes, and the hash to check them against
  is the one cosign signed rather than a checksum file alongside.
- **Proxmox scripts** that create a single node from a qcow2, a node that
  installs itself from the ISO, and a three-controller HA cluster, with a
  [walkthrough](docs/install/proxmox.md) written from a real run.
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

[Unreleased]: https://github.com/Corium-OS/Corium/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Corium-OS/Corium/releases/tag/v0.1.0
