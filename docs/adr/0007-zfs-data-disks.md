# 7. ZFS covers data disks, in a variant image, not the base

Status: accepted

## Context

Nodes that keep local state on their data disks — a persistent-volume
provisioner, an image or build cache, scratch space — are the same nodes that
want what ZFS gives and `raid[]` does not: end-to-end checksums, transparent
compression, and snapshots. [Issue](https://github.com/Corium-OS/Corium/issues)
asked whether Corium could put ZFS on those data disks.

The request splits into two questions, exactly as software RAID did (see
[ADR-0003](0003-software-raid-scope.md)).

**Data disks.** Extra disks to be pooled and mounted somewhere. This can be done
at first boot, and it is what this ADR adds.

**The root filesystem.** By the time the `corium:` block is read, root is
deployed, mounted, and running the process reading it. Nothing there can move it
onto ZFS. Root on ZFS additionally needs the module in the initramfs and a
bootloader integration bootc has no declarative equivalent for. Out of scope,
for the same reasons a RAID root is.

But ZFS carries a cost RAID does not, and it lands on the image rather than the
schema.

- **Fedora ships no ZFS.** It is under the CDDL, which Fedora treats as
  incompatible with the GPL kernel. There is no package to install; the module
  must be compiled from source.
- **Fedora has no stable kABI.** A module built against one kernel will not load
  on another, and there is no kABI-tracking kmod as there is on RHEL. The module
  must be built against the *exact* kernel it runs on, and rebuilt on every
  kernel bump.

## Decision

`zfs[]` covers data disks. Pools are created by `corium-agent` at first boot,
before k0s is installed, on the same footing as `raid[]`: devices are refused if
they already hold data, an existing pool is adopted rather than rebuilt, and a
`RequiresMountsFor` drop-in stops k0s starting before the datasets are mounted.

The ZFS kernel module and userspace are **not in the base image**. They ship in
a derived variant, `deploy/zfs/`, built on demand — the same shape as the
appliance image. The agent code (`internal/bootstrap/zfs.go`) is in the base
binary; only the module and tools it drives are variant-only.

## Why the module is baked, not built at runtime

The kABI problem has exactly one clean answer on an immutable OS, and it is the
one bootc already implies: build the module against the image's kernel, at image
build time, and ship the two together. An image is one kernel; the module in it
is that kernel's module; an upgrade replaces both at once by digest. There is no
window in which a running kernel and its ZFS module disagree, because they are
never updated independently.

DKMS — rebuild the module on the running machine after each kernel update — is
the mutable-host answer and the wrong one here. Root is read-only, so there is
nowhere to build; and DKMS keeps its state under `/var`, which on a bootc node is
machine state seeded once, not a build tree. OpenZFS itself recommends
precompiled kmod packages over DKMS for fleets. So the variant compiles OpenZFS
into a `kmod` RPM in a throwaway build stage and installs it; the module lands in
`/usr/lib/modules/<kver>/extra`, part of the read-only image.

## Why a variant image, not the base image

The base image is deliberately minimal (see the `Containerfile` package list).
ZFS would add three things every node that never touches it should not carry:

- **Weight.** The module and userspace, on an image whose reason to exist is
  being small and replaceable.
- **Build fragility.** Every kernel bump becomes a compile that can fail when
  OpenZFS does not yet support the new kernel. Making that a gate on *every*
  Corium build would couple the whole project's release cadence to OpenZFS's
  kernel support. As a variant, it gates only the nodes that opted in.
- **A licensing choice.** Compiling and shipping a CDDL module in a GPL kernel
  image is a decision an operator should make on purpose, not one baked into the
  artefact everyone downloads.

Deriving the variant `FROM` the base image is what makes the kABI match free:
`rpm -q kernel` in the build names the exact kernel the module must target, so
the compile is against the kernel the image already ships.

## Why data-disk ZFS earns its place over cloud-init

The same three reasons `raid[]` does, verbatim in spirit (see ADR-0003):

- **Ordering.** A `runcmd` calling `zpool` races the kubelet. Lose the race and
  containerd writes into the directory on the root disk that the dataset is then
  mounted over — invisible afterwards, still consuming root. Corium creates
  pools before k0s and writes `RequiresMountsFor`, so Kubernetes refuses to start
  rather than run without its storage.
- **Refusing to destroy data.** A device already carrying a filesystem, a
  partition table, or another pool's or array's label stops the bootstrap.
  `zpool create` by hand takes `-f` and does not ask; Corium makes `wipe: true`
  the explicit consent and defaults to stopping. A device may belong to a pool
  or an array, never both, and that is checked at validation.
- **Idempotency.** A second bootstrap imports and adopts an existing pool rather
  than recreating it, and reconciles dataset properties rather than duplicating
  anything.

## Consequences

A node's data pool survives a reboot (imported by scanning the attached disks,
`cachefile=none`, so it follows its disks onto replacement hardware) but not the
loss of the node, which is the model the rest of Corium is built around.

Declaring `zfs[]` on a plain base image is a first-boot error — `loading the zfs
kernel module: ...` — not a silent no-op. Asking for a pool the image cannot
build should fail loudly.

The variant's build tracks OpenZFS's kernel support. When a Fedora bump outruns
the pinned OpenZFS release, the fix is a `ZFS_VERSION` bump, not a base-image
change. If Fedora ever ships ZFS, or bootc grows a declarative root-on-ZFS path,
this decision should be revisited; the schema and the variant both have room.

## Alternatives considered

**ZFS in the base image, behind a build ARG.** A single `Containerfile` with a
conditional kmod stage. Rejected: it complicates the one file every build reads
for a feature most nodes do not use, and a stage-selection trick to skip the
compile when the ARG is off is exactly the kind of cleverness that image is kept
free of. A separate `deploy/zfs/` matches the appliance precedent and keeps the
base `Containerfile` a straight read.

**DKMS on the node.** Rejected above: no writable root to build on, and `/var`
is seeded state, not a build tree.

**Document a `zpool` recipe, add no field.** Rejected for the reason ADR-0003
rejected the same option for RAID: it leaves the ordering problem unsolved, and
the ordering problem is the one that silently corrupts a node.

**Put the data-disk provisioner on the RAID path instead.** `raid[]` already
exists and covers striping and mirroring. Rejected as a *replacement*, not as an
alternative to keep: mdadm gives a block device a filesystem is laid on, with no
checksums and no snapshots, and the workloads that specifically want ZFS want
those. The two coexist; a node picks per set of disks.
