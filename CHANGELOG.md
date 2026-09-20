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

- **A configuration may leave `role` out, and that is how a node says it is
  waiting to be told what it is.** Two lines — `api.enabled: true` and nothing
  else — are now a valid document: the node runs its API, holds its bootstrap,
  and builds nothing until an operator sends a document with `cctl enroll
  --config` or `cctl apply`. It was previously a validation error, which failed
  both `corium-apid` and `corium-bootstrap` and left a machine with no API and
  no cluster. `api.awaitConfig` still works and is still the way to ask for the
  wait in a document that *does* name a role; it is no longer needed in one
  that does not. A document with no role is still refused when the API is off,
  because the answer could never arrive. See
  [ADR 4](docs/adr/0004-management-api.md).

- **`cctl apply` can change the safe part of a running node's configuration,
  without a reset.** A node that has already bootstrapped used to refuse every
  apply and send you to `cctl reset`; it now re-applies the add-on set in place —
  regenerating the k0s configuration and cycling the control plane to pick it up —
  and refuses the fields that define what the node is (its role, cluster, name,
  network, disks and the like), naming the offending field when it does.
  Removing an add-on is refused too, because k0s leaves a dropped chart's release
  running — take it off with `kubectl delete chart`. An apply that matches what
  the node is already running is a no-op. Implements the first cut of
  [ADR 8](docs/adr/0008-day-two-reconcile.md); the set of fields a running node
  will re-apply is expected to grow.

- **A controller can hand out a worker's configuration.** `cctl worker-config
  <controller>` asks a controller to mint a fresh worker join token and prints
  the `corium:` block a new worker needs to join — role, the inline token, and
  an optional `--name` and `--label` — ready to paste into a cloud-config. It
  saves minting a token by hand with `k0s token create` and pasting it: the token
  is generated on the controller over the management API (`corium:admin`, and only
  a controller can answer), embedded in the clear, and short-lived by default
  (`--expiry`, default `1h`). The block is a secret and goes to standard output
  alone, so `> worker.yaml` writes only YAML.

- **The console says what the node is, above the login prompt.** A getty used to
  show the Fedora banner and nothing else; it now shows the node's role, its
  cluster, whether k0s is running, greenboot's verdict, the booted image and any
  image staged for the next reboot. It is a drop-in in `/run/issue.d` that a
  timer keeps current and reprints with `agetty --reload` only when the state it
  shows actually changes, so a console left open tracks a node joining a cluster
  or an upgrade staging without redrawing itself into a wall of stale banners.
  On a node still waiting to be claimed it sits above the pairing code, not
  instead of it.

- **A host WireGuard overlay can be declared from the `corium:` block.** A new
  `wireguard:` list brings up encrypted interfaces at first boot, before k0s, so
  nodes across sites or providers can form one cluster over a private overlay.
  Mark an interface `nodeAddress: true` and k0s registers the overlay address
  rather than the physical NIC — the piece a cloud-init recipe cannot get right,
  because the address is chosen inside the agent, after cloud-init has run. The
  private key resolves through the same secret references as a join token
  (`privateKeyFrom`), is written `0600`, and is never logged; `wireguard-tools`
  now ships in the image, present but inert until an interface is declared. See
  [ADR 6](docs/adr/0006-host-wireguard-overlay.md).

### Changed

- **`cctl enroll` asks before claiming a node that would bootstrap
  immediately.** Claiming is what releases a held bootstrap, so a node whose
  own cloud-init names a `role` starts building that node the instant it is
  claimed — and undoing it is `cctl reset`. The node now refuses such a claim
  and names the role, node name and cluster it would build; `cctl` shows that
  and asks. Nothing is claimed while the question is open and the pairing code
  is not spent, so answering costs nothing. A node waiting to be told what it
  is — one whose document names no role — is claimed without a question,
  because claiming it builds nothing.

  **This breaks scripted enrolment of nodes whose configuration names a role:
  pass `--yes`.** Without a terminal the refusal is returned rather than asked,
  and the message names the flag.


### Fixed

- **The WireGuard guide described the feature as unbuilt.** It opened by saying
  Corium had no native field for a host interface and spent the page on a
  cloud-init workaround, in the same tree that added `wireguard:`, documented
  it in the reference and shipped an example using it. It now documents the
  field, and keeps the cloud-init route as the escape hatch it is.
- **Documentation pointed at 0.1.0.** The quick start and the install guides
  told you to pull artefacts two releases old, and the quick start and
  configuration reference both still said `cctl` had no published binary.
- **Two copy-and-paste failures.** The HA guide shipped a join token through a
  `$TOKEN` that nothing assigned, writing an empty file while the controllers
  waited in silence; Downloads printed the `mise use` command with an unbalanced
  quote, on the page that explains why the quotes matter.

## [0.2.0] - 2026-09-19

### Added

- **The API can grant SSH access to a node.** `cctl access ssh add`, `list` and
  `revoke` trust an SSH key for a user that already exists, so getting a shell
  on a node no longer means a reset or a trip back through cloud-init. Corium
  creates no account and keeps a key file of its own, separate from the user's
  own `~/.ssh`; the account, its password and its shell stay cloud-init's.
  Adding a key is `admin` and logged by fingerprint, listing is `readonly`, and
  a reset removes every key the API was trusting.

- **The pairing code is on the node's screen, above the login prompt.** It was
  written to `/dev/console`, which on a node booting with
  `console=tty0 console=ttyS0` reaches the last of those and no other — so it
  went to the serial port and the hypervisor's own console view never showed
  it. It is now also an issue drop-in in `/run/issue.d`, which every getty
  reprints each time it draws a prompt, so it survives whatever scrolls past
  and is there on the screen an operator is actually looking at. It is removed
  when the node is claimed, so a prompt never advertises a code that no longer
  works.

- **The documentation uses the CLI where it earns its place.** The quick start
  gains a fifth step for managing a node without SSH; the upgrade guide leads
  with `cctl upgrade` and keeps the manual sequence as what it does; the HA
  guide gains the two things that are genuinely easier with it, both about the
  virtual IP. The install walkthroughs keep their SSH narrative and gain a
  pointer — a guide whose job is to boot a node should not acquire a PKI step.
  Downloads says plainly that `cctl` has no published binary yet, which is a
  gap rather than a decision.

- **A management API, and `cctl` to drive it.** A node can now be read,
  restarted, upgraded, drained and reset without an SSH session — which on an
  immutable OS was always a poor fit, since the shell you land in is a shell
  over a system where almost nothing you type persists. `corium-apid` serves
  JSON over HTTP and mutual TLS on `7443`, and is **off unless asked for**: a
  node with no `api:` block runs no daemon and binds no port, exactly as before.

  Trust is anchored in an operator CA. A node is given its *certificate* and
  never its key, which is what makes `api.operatorCA` safe to write in clear in
  cloud-init — unlike a join token, a certificate leaks nothing. `cctl pki init`
  makes the CA and prints it ready to paste; `cctl pki issue --role admin`
  signs the certificate a node checks on every call. Three roles, carried in
  the certificate's organisation, and every route names the lowest one that may
  use it.

  See [ADR 4](docs/adr/0004-management-api.md) for the whole decision, and
  [docs/cli.md](docs/cli.md) for every command.

- **Maintenance mode, for nodes nothing should be told in advance.** Set
  `api.enabled: true` with no CA and the node validates its configuration, then
  **stops before bootstrapping k0s**, printing a single-use pairing code and
  its certificate fingerprint on the console. `cctl enroll` claims it and
  releases the bootstrap.

  A node waiting to be claimed is in no cluster, and the rule holds both ways:
  a node cannot return to maintenance mode while it is a member, so `cctl reset`
  takes it out of the cluster on the way. Enrolment is one-way and survives
  reboots — otherwise power-cycling a machine would be enough to take it.

  It is not zero touch: three nodes means three consoles. `api.operatorCA`
  gives you unattended provisioning with nothing secret in the metadata, since
  the certificate is not a secret. `api.insecure` drops the pairing code for a
  bench or a controlled provisioning network, and the node then records that
  its ownership was established without anybody proving anything — which
  `cctl status` reports from then on, and which rotating the CA does not clear.

- **Four things you can do to a node.**

  *State* — `cctl status`: role, cluster, the booted and staged image with
  their digests, kernel, k0s version and service, greenboot's verdict, uptime.
  A field the node could not determine is left out rather than shown as a dash:
  this is read just before something irreversible.

  *Services and journals* — `cctl services`, `cctl logs` per unit or across all
  of them, with `--since`, `--follow` and `--unit kernel`. Units come from a
  fixed list; an API that hands a unit name to `systemctl` can start anything
  on the machine. Restarting is a shorter list still — `corium-bootstrap` is
  readable and deliberately not restartable.

  *Upgrades* — `cctl upgrade <nodes...> --image`, one node at a time, stopping
  at the first that does not come back **on the digest it was sent to**. A node
  refuses an image its own signing policy would accept unsigned, which is what
  stops a typo rebasing a Kubernetes node onto a desktop. `cctl rollback` marks
  the previous image as next to boot and does not reboot.

  *Lifecycle* — `cctl cordon`, `drain`, `reboot`, `shutdown`, `reset`. Cordon
  and drain work on controllers only: a node acts on itself, and evicting a pod
  needs credentials only a controller holds. `cctl reset` requires the node's
  own name, leaves the cluster, erases the bootstrap, *then* forgets its owner,
  then reboots — in that order, because the other way round could leave a
  cluster member nobody owns.

- **`cctl kubeconfig <node>`** hands over the administrator credentials k0s
  minted, pointed at the cluster's virtual IP where there is one — a kubeconfig
  aimed at one particular controller stops working the first time that
  controller does. `--server` overrides it. Standard output by default, and
  deliberately not `~/.kube/config`.

- **`cctl ca rotate`, and a way back when the key is gone.** Rotation hands a
  set of nodes to a different operator CA; the node refuses a CA the caller
  cannot show a signed certificate for, because rotating to one you cannot
  issue under leaves a machine that will only ever accept somebody else.
  `corium-agent api set-ca --file`, run as root on the node, is the console
  path when the key is lost — the node keeps its cluster membership, which is
  what makes it a recovery rather than a reset.

- **A guide for installing Cilium**, [docs/cilium.md](docs/cilium.md). Corium
  already supported it — `network.cni: custom` plus an `addons:` entry — but
  nothing said how the two fit together, or that the chart installs from the
  controller and therefore works before the cluster has a pod network. The
  guide covers the single-node case, the kube-proxy-free variant and what it
  costs, what a worker may and may not declare, and the failure modes. The
  example it is built on, `examples/custom-cni.yaml`, moves to Cilium 1.20.2
  and drops a `cni.binPath` override that only restated the chart's defaults.

- **A page for `cctl`**, [docs/cli.md](docs/cli.md): the three things it needs
  to work, every command grouped by what an operator is trying to do, the
  roles, the files it keeps in `~/.corium`, what it deliberately will not do,
  and a table for reading a refusal by status code.

- **`cctl apply` gives a node the configuration it will bootstrap with**, and
  `api.awaitConfig` makes it wait for one. A whole fleet can now be provisioned
  from a single identical cloud-config that carries no secrets and says nothing
  machine-specific — three lines turning the API on and asking the node to wait
  — with the role, cluster and join token arriving afterwards over the API.
  `cctl enroll --config` does both in one step, and that ordering matters:
  claiming a node is what releases its bootstrap, so a document sent a moment
  later would be racing a machine that has already started becoming something.

  It works **only before a node has bootstrapped**. A machine already running
  Kubernetes answers `409` and points at `cctl reset`, whoever asks and
  whatever role they hold: rewriting the role or cluster of a node in service
  would leave its configuration and its behaviour saying two different things.
  [ADR 4](docs/adr/0004-management-api.md) is amended with the reasoning, since
  it previously ruled this out altogether.

- **A recipe for an appliance image**, [deploy/appliance](deploy/appliance/).
  Three lines of Containerfile bake a `corium:` document into
  `/usr/share/corium/config.yaml`, so a machine installed from the resulting
  ISO boots into the management API and waits to be claimed and told what it
  is — no cloud-init, no console visit, no pairing code. `mise run
  appliance-image` builds it.

  It is a recipe and not a published artefact on purpose: the configuration it
  ships accepts an unauthenticated claim, so whoever reaches the node first
  owns it for life. Its README says so first and says what to use instead —
  baking in `api.operatorCA`, which is zero touch too and has no
  unauthenticated port. It also documents the trap: the source chain stops at
  the first source that yields a *document*, not the first that contains a
  `corium:` block, so cloud-init carrying nothing but an SSH key is enough to
  mask a baked-in configuration. The ISO is the artefact this works for.

- **`cctl` is published with each release**, for linux and macOS on amd64 and
  arm64, and installable with
  `mise use -g 'github:Corium-OS/Corium[exe=cctl]@0.2.0'`.
  Until now it was built from a checkout and the documentation said plainly
  that this was a gap rather than a decision; it is closed.

  The archives are attached to the release rather than pushed to the registry
  the ISO and the qcow2 live in, because the reasoning that keeps those out
  points the other way for a three-megabyte binary fetched by a person setting
  up a workstation: the installers people already use read release assets. A
  single signed `SHA256SUMS` covers every archive, under the same key as the OS
  image. There is no Windows build, because nobody has run `cctl` there once.

  Pin the version if you want a stable one: mise resolves an unpinned
  `github:Corium-OS/Corium` to the newest tag, release candidates included.

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

- **The VM console is quieter at boot.** `quiet loglevel=3`, shipped as a bootc
  kernel argument under `/usr/lib/bootc/kargs.d` and so reapplied on every
  upgrade, drops routine kernel chatter — device probes, netfilter, the bridge
  module — while warnings, errors, and the enrolment code still print. The code
  is written straight to `/dev/console` by `corium-apid`, not through the kernel
  log, so lowering the printk level cannot hide it. cloud-init's output is left
  on the console on purpose, so a first boot that goes wrong still says so.

### Fixed

- **The pairing code and the attempt limit are now per boot, as documented.**
  Both lived in the daemon's memory, so restarting `corium-apid` — which
  systemd does after a crash — minted a second code while the first was still
  printed on the screen above it, leaving an operator with two codes and no way
  to tell which one worked. Typing the wrong one costs an attempt, and there
  are five. The count reset too, so the limit that makes a forty-bit code
  sufficient promised five guesses per boot and delivered five per process.
  Both now live in `/run`, which survives a restart, is emptied by the reboot,
  and never reaches a disk.

- **A node bootstrapped before 0.2.0 reported itself as never bootstrapped.**
  The role and cluster are read from `/var/lib/corium/node.json`, which only
  0.2.0 writes — so a node upgraded from 0.1.0 said it ran no Kubernetes,
  refused to hand over a kubeconfig, and would have been **skipped by
  `cctl upgrade`**, whose health gate asks exactly that. That is every machine
  in service on the day this ships. Such a node is now recognised by the
  bootstrap marker it has always written, with its role derived from the k0s
  unit that was installed — the same signal the greenboot check uses.

- **A bad `ha.authPassFrom` now says `ha.authPassFrom`.** Every problem with a
  secret source was reported as `join.tokenFrom` whichever key it was reached
  through, which sent you to a line that was not the one at fault.

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

[Unreleased]: https://github.com/Corium-OS/Corium/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Corium-OS/Corium/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Corium-OS/Corium/releases/tag/v0.1.0
