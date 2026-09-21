# 9. Build the artefacts with image-builder; bootc-image-builder is archived

Status: proposed

## Context

[Issue #11](https://github.com/Corium-OS/Corium/issues/11) points out that
every installable artefact Corium produces — the qcow2, the raw disk and the
installer ISO — goes through `bootc-image-builder`, and that the project was
archived on 2026-06-18. The facts, checked against the registries and the
upstream source rather than the announcement:

- `quay.io/centos-bootc/bootc-image-builder:latest`, which `mise.toml` and both
  workflows track, was last built on 2026-06-18 at 11:31 UTC from commit
  `a686afe` — "readme: migration notice", the archive commit. Its digest is
  `sha256:2b52843ea2bfda73b0a08d97e76b734393b1d3a804681b9fabb26723bd3a2f0b`
  and it is built on Fedora 42, which reached end of life on 2026-05-13 — a
  month before the image was built. The tag we follow stopped moving three
  months ago; nothing noticed because nothing broke.
- The code did not stop. It was merged into
  [`osbuild/image-builder`](https://github.com/osbuild/image-builder), which
  releases weekly (`v78.0.0` through `v83.0.0` between mid-August and
  2026-09-16) and publishes three images from one workflow, all on Fedora 44:
  `ghcr.io/osbuild/image-builder` and `ghcr.io/osbuild/image-builder-cli`
  (the same digest under two names), and
  `ghcr.io/osbuild/bootc-image-builder` — the same binary installed as
  `/usr/bin/bootc-image-builder`, with the old entry point, flags, and output
  layout. Each is tagged per release; `v83.0.0` of the compatibility image is
  `sha256:e7aadce6b3f5639cd47d83354791931ea219891a0d113c2fe74a0f0d352b165c`.
- The [deprecation notice](https://osbuild.org/docs/bootc/deprecation-notice/)
  commits to keeping that entry point for the life of RHEL 10 and dropping it
  in RHEL 11. It says the same of the `anaconda-iso` image type: deprecated,
  gone in RHEL 11, with the container-based installers as the forward path.

The issue asked three questions. Each was answered from the `image-builder`
source at `v83.0.0` and the documentation in its tree, because the published
pages describe the image types without saying what they do.

## Is `image-builder build --bootc-ref` a drop-in replacement?

**For the qcow2 and the raw disk, yes in substance and no in detail.** The
invocation becomes `image-builder build --bootc-ref <image> qcow2`, run from
`ghcr.io/osbuild/image-builder` with the same privileged flags and the same
mount of the root container store: the container must already be in local
storage, exactly as the release workflow arranges today by pulling first.
The root filesystem type is read from the container's bootc install
configuration — `/usr/lib/bootc/install/00-corium.toml`, per ADR 2 — so
`--bootc-default-fs` is not needed. The two things that differ:

- The output layout. `image-builder` writes
  `<distro>-<type>-<arch>/<distro>-<type>-<arch>.<ext>`, which for a bootc
  input is `bootc-based-qcow2-x86_64/bootc-based-qcow2-x86_64.qcow2`, where
  `bootc-image-builder` writes `qcow2/disk.qcow2`. `--output-dir` and
  `--output-name` restore today's paths, so `deploy/proxmox/` and the publish
  step, which only consume files, need not change.
- Warnings during manifest generation stop the build unless
  `--ignore-warnings` is passed. That is a feature — the release path should
  fail loudly — but it is a behaviour change to expect on first run.

**For the ISO, no.** The CLI refuses the type outright when given a bootc
reference:

    image type bootc "anaconda-iso" is not supported with image-builder,
    please consider switching to "bootc-installer" or use bootc-image-builder

The comment above that check says why: without the mTLS plumbing the old tool
set up, the behaviour would differ subtly, and upstream chose an error over a
quiet incompatibility. `anaconda-iso` is reachable only through the
compatibility entry point, on a code path (`bootc_legacy_iso`) upstream's own
image definitions annotate with "we want to get rid of it".

## Can `bootc-generic-iso` carry a custom kickstart?

**Not the way `[customizations.installer.kickstart]` does today, because the
generic ISO reads no build configuration at all.** In the source,
`manifestForGenericISO` is the one image function that is never handed the
blueprint. The ISO it produces is the container itself, converted to a
squashfs and booted live, with a GRUB menu taken from
`/usr/lib/image-builder/bootc/iso.yaml` *inside the container*. What that ISO
does when it boots is whatever the container does when it boots. Upstream's
reference is a `fedora-bootc` container with Anaconda installed by `dnf`, an
`iso.yaml` that boots it with `inst.stage2=`, and a kickstart at
`/usr/share/anaconda/interactive-defaults.ks` carrying a single
`bootc --source-imgref … --target-imgref …` line. Passing
`--bootc-installer-payload-ref` copies the image to be installed into the
squashfs's own container store, which is what makes the install work offline.

So the kickstart does not go away; it moves. It stops being a build input
passed to the builder and becomes a file in an installer container that
Corium has to build and own — a second `Containerfile` beside the appliance
and ZFS variants. The RAID root procedure in [`docs/raid.md`](../raid.md) is
still an Anaconda kickstart with the same content and the same three manual
steps; only its delivery changes, and it has to be verified again on two
disks, because it was verified against the ISO type this decision retires.

Two things follow that are easy to miss:

- Today's ISO is **unattended** because `bootc-image-builder` ships a default
  kickstart for `anaconda-iso` that installs to the first disk with no prompts.
  In the generic ISO that default is ours to write. It is a few lines, and it
  is the part the quick start and `deploy/proxmox/create-vm-iso.sh` depend on.
- `bootc-installer`, the other suggested type, *is* Anaconda-based and *does*
  honour the blueprint kickstart — the same `kickstart.New(customizations)`
  call the legacy path uses. But it too requires two containers: `--bootc-ref`
  must be an Anaconda installer environment, and the image to install goes in
  `--bootc-installer-payload-ref`. Upstream files it under "Historical" and
  says to prefer the generic type. It needs the installer container either
  way, so it saves nothing.

## Does pinning buy useful time?

**Pinning the Quay digest buys reproducibility and nothing else.** The tag is
already frozen; a digest makes that explicit and lets CI record which builder
produced an artefact, which the release workflow currently cannot. It does not
make the builder maintained, and its base is a Fedora release that no longer
receives updates.

**Pinning the compatibility image from `image-builder` buys the time.** It is
the same entry point, the same flags, and the same output paths, kept working
on purpose by the people who wrote it, on a current base, with a version
number to bump. It still builds `anaconda-iso`. That is exactly what is needed
while the ISO is rebuilt on the generic type: one change that stops the
dependency rotting, without touching the install path a release is verified
against.

## Decision

1. **Now: build with `ghcr.io/osbuild/bootc-image-builder`, pinned to a
   release tag and its digest, everywhere the builder is named** —
   `mise.toml`, the release workflow and the end-to-end workflow. Same
   invocation, same three artefact types, same files out. The `BIB`
   environment override stays, so the frozen Quay digest is a one-line
   fallback if the new image misbehaves. Bumping the pin is a reviewable diff
   with the digest in it, the way `build/k0s.lock` already treats the k0s
   binary.

2. **Next, as its own change: the qcow2 and the raw disk move to
   `image-builder build --bootc-ref`**, run from `ghcr.io/osbuild/image-builder`
   pinned the same way, with `--output-dir` and `--output-name` preserving
   `output/qcow2/disk.qcow2` and `output/image/disk.raw`. The end-to-end
   workflow builds a qcow2 and boots it, which is the test. After this the
   compatibility image is used for the ISO and nothing else.

3. **Then: an installer container, built as `bootc-generic-iso`.** A
   `Containerfile` deriving from the Fedora bootc base with Anaconda, an
   `iso.yaml`, and a default kickstart that installs the embedded Corium image
   to the first disk unattended. The image to install is passed with
   `--bootc-installer-payload-ref`, by digest, as today. The RAID root
   procedure becomes a variant of that container — a kickstart file swapped
   in at build time — and is re-verified by pulling a disk, as the first one
   was. The published ISO stays on `anaconda-iso` until the generic one has
   installed a node through the release verification list, including the
   Proxmox script and the RAID procedure. Not before: bare metal is a
   documented path with one artefact, and this is the only thing that
   exercises it.

Step 1 is in this change. Steps 2 and 3 are gated on their tests, not on a
date; RHEL 11 is the outer bound and is not the schedule.

## Consequences

Nothing changes for someone building or installing Corium today. The commands,
the artefacts and their paths are the same, and the published ISO installs
the same way.

The builder becomes a pinned dependency like the others. That is a small
recurring cost — a digest to bump, ideally with each Corium release — in
exchange for knowing which bytes produced an artefact and being able to say
so in the release job, which today prints the digest of whatever `:latest`
happened to be.

Step 2 changes the invocation in the same two places and carries the risk
that a warning `bootc-image-builder` tolerated becomes an error. That is the
kind of failure the end-to-end workflow exists to catch.

Step 3 adds a second container image to build and a verification item to the
release list. It also makes the installer *ours*: what the ISO does at boot is
in a file in this repository rather than in the builder's defaults, which is
where a project that documents a RAID root should have wanted it. The
kickstart section of `docs/raid.md` is rewritten when it lands, not before.

Upstream warns that a bootc system installed through Anaconda fails
`systemd-remount-fs.service` at boot
([bugzilla 2332319](https://bugzilla.redhat.com/show_bug.cgi?id=2332319)).
Today's ISO installs through Anaconda too, so this is either already the case
and harmless, or not reproduced here; either way it is checked when the new
ISO is, not assumed.

## Alternatives considered

**Keep tracking `quay.io/centos-bootc/bootc-image-builder:latest`.** It has
not moved since the archive commit and will not. A tag that is frozen today
and 404 tomorrow is the failure mode the issue describes, discovered during a
release. Rejected.

**Pin the Quay digest and stop there.** Reproducible, honest about what built
an artefact, and otherwise identical to the above: an end-of-life base and an
unmaintained binary. It is kept as the fallback the `BIB` override reaches
for, not as the decision.

**Move everything to `image-builder build --bootc-ref` in one step.** Not
possible: the CLI refuses `anaconda-iso`, and the replacement ISO needs an
installer container that does not exist yet. Doing the disks now and the ISO
later is the same end state with a test between the two.

**Use `bootc-installer` rather than `bootc-generic-iso` for the ISO**, because
it honours `[customizations.installer.kickstart]` and would keep
`docs/raid.md` closer to what it says today. It still requires an installer
container of our own, upstream marks it historical, and it couples the ISO to
Anaconda where the generic type leaves the choice to the container. Nothing
is saved. Rejected.

**Drop the ISO and publish only disks.** The ISO is the documented path onto
bare metal, the RAID root depends on it, and `deploy/proxmox/create-vm-iso.sh`
and the appliance image both produce one. Rejected.
