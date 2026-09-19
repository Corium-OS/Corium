# Releasing Corium

How a release is cut, and what has to be true before one is. This is a
maintainer document; it deliberately does not appear on the documentation
site.

---

## Versioning

Corium follows [semantic versioning](https://semver.org/spec/v2.0.0.html).
Tags are always `vX.Y.Z`, or `vX.Y.Z-rc.N` for a release candidate. Nothing
else is a release: a tag like `v1.4` would publish an image tag that collides
with a rung of the ladder and move it to an arbitrary commit.

**Corium is below 1.0.** Semantic versioning reserves `0.y.z` for initial
development, which means a minor release is allowed to change or remove
configuration an earlier one accepted. That permission is used sparingly and
never silently — a breaking change goes in the changelog under **Changed**,
with what to do about it — but it is the reason the ladder below 1.0 has no
major rung.

| Tag | Moves | Promises |
|---|---|---|
| `0.1.0` | Never | One release |
| `0.1` | On patch releases | Fixes, no new behaviour |
| `latest` | On every release | Nothing but recency |

`0` is not published, because "new features, no breaking changes" is a promise
`0.y.z` withholds. A `1` rung appears at 1.0, when it becomes true. The logic
lives in `.github/workflows/image.yml`; the user-facing version is
[docs/upgrades.md](../docs/upgrades.md#choosing-what-to-track).

Prereleases publish only their exact image tag. A release candidate can never
be picked up by a node following `0.1` or `latest`, which is what makes the
rehearsal below safe.

---

## Cutting a release

Tagging publishes and signs an image to a public registry, and moves `latest`.
None of it can be undone. Work through this in order.

1. **Write the changelog.** Entries accumulate under `## [Unreleased]` as work
   lands. Read it as a user would: does it say what changed for them, or what
   changed in the tree?
2. **Check the documentation still tells the truth.** The README status
   banner, the Kubernetes and k0s badges against `build/k0s.lock`, and
   anything in `docs/` that describes behaviour this release changed.
3. **Regenerate the site** if anything under `docs/` moved:
   `python3 website/sync-docs.py`, and commit the result. The Pages workflow
   fails without it — and it only runs on `main`, so a pull request will not
   warn you.
4. **Merge, with CI green.**
5. **Tag the release candidate**: `vX.Y.Z-rc.1`. This publishes one image tag
   and nothing else. It is also the first execution of the release job, so it
   is where a mistake in the release path shows up while it is still cheap.
6. **Work through the verification below, against that image.**
7. **Promote the changelog**: `## [Unreleased]` becomes `## [X.Y.Z] - date`,
   a fresh empty `## [Unreleased]` goes above it, and the link references at
   the bottom are updated. Merge it. The release job fails on a stable tag
   with no matching section, so this is not optional.
8. **Tag the release**: `vX.Y.Z`. This moves `latest`.

The tag build publishes three things, in order, each gated on the one before:
the signed image, the signed artefacts built from it (an installer ISO and a
qcow2, one bootc-image-builder run each -- it refuses to produce an ISO and a
disk image in the same request), and the GitHub Release that points at all of
them. The ISO build takes 20 to 35 minutes, so a release takes roughly an
hour of CI. If it fails, nothing announces a release that does not exist --
re-run the workflow, which is idempotent on all three.

### The download host

Both artefacts are also copied to a bunny.net storage zone and served from the
CDN pull zone in front of it, so the release notes can offer a link rather
than a command. The release path treats it as optional:

- The step is `continue-on-error`. A download host that is unreachable costs a
  release nothing, and the notes offer a link only for an artefact whose
  upload and read-back both succeeded.
- It reads a byte back as an anonymous client before advertising anything,
  retrying a few times because that request is also the CDN's first sight of
  the object. An advertised URL that 403s is worse than no URL.

**Nothing published there is ever overwritten, and that is load-bearing.** The
CDN does not watch its origin for changes: a replaced file keeps being served
from cache until it expires, and purging needs an account-wide API key this
workflow deliberately does not hold. Every artefact carries its version in its
name, so the situation cannot arise. Do not add a `latest` alias there without
solving the purge problem first.

CI uploads with the storage zone's own credentials -- the zone name is the S3
access key and the zone password the secret -- which reach that zone and
nothing else in the account. Repository secret `BUNNY_STORAGE_PASSWORD`;
repository variables `BUNNY_STORAGE_ZONE`, `BUNNY_REGION` and
`BUNNY_PULL_ZONE_URL`.

**The zone name must stay a variable.** GitHub drops job outputs containing a
secret's value, and the zone is named after the project -- as a secret it
silently emptied every output that spelled it. An access key id is public by
construction anyway; the password is the half that matters.

Debugging an upload with curl: the S3 endpoint is
`de-s3.storage.bunnycdn.com`, but the native API for the same region is plain
`storage.bunnycdn.com` -- `de.storage.bunnycdn.com` does not resolve.

The zone password grants delete as well as write, so it can erase past
releases. bunny.net offers no write-without-delete password; accepted risk.

Two settings on the pull zone that are not obvious:

- **Optimize for Video Delivery** (cache slicing) must be on, or `Range`
  requests are only honoured for content already cached. Without it a 2.4 GB
  download cannot resume.
- **Token Authentication** must stay off, or every URL needs a signature.

The storage zone's S3 compatibility can only be enabled when the zone is
created. If it is ever recreated, that box has to be ticked at the time; there
is no way to add it afterwards.

The account is prepaid: at zero balance the downloads stop while the registry
keeps working. Storage is pennies, and the bill is essentially egress at about
a cent per gigabyte.

---

## Verification before a release

CI covers the parts a machine can check: tests with the race detector, lint,
`go mod tidy`, every example under `docs/examples/` and `deploy/proxmox/`
still parsing, the rendered `k0s.yaml` in the reference still matching what the
agent produces, the image building with `bootc container lint` clean, and the
k0s binary matching `build/k0s.lock`.

None of that has ever caught this project's worst bugs. A systemd ordering
cycle that deleted the bootstrap job, every node registering as `fedora`, an
HA controller claiming the virtual IP as its own address — all three passed
review and unit tests and only appeared on a running machine. So the list
below is about machines, and it runs against the **release candidate image**,
not against a local build.

| What | Why it is on the list | Roughly |
|---|---|---|
| Signature enforcement on a node | Only testable once an image is published and signed. cosign v2 writes the `<digest>.sig` tag a node's policy looks for; v3 does not, and nothing in CI would notice a tooling bump breaking it | 10 min |
| A single node reaching `Ready` | The cheapest end-to-end signal there is | 15 min |
| Upgrading *into* the candidate | Exercises staging, finalisation, and the marker in `/var` that stops a node re-bootstrapping | 20 min |
| `upgrades.automatic: apply` driven by its timer | The drain, reboot and uncordon have been verified by hand. The systemd wiring that is supposed to drive them has not | 30 min |
| Health-gated rollback | Reported working once, proved a false positive, then fixed. It is worth exactly as much as the last time it was observed, and no more | 40 min |
| Three controllers with VIP failover | Hard-stop the holder: the VIP must answer from another controller's MAC, etcd must keep quorum, and the API must stay up | 1 h, 3 VMs |
| Installing from the anaconda ISO | The documented path onto bare metal, and nothing else exercises it | 45 min |
| Software RAID on spare disks | Assembled before k0s starts, surviving a reboot, and refusing to touch a disk that holds data | 20 min |
| The published ISO installs | Download it the way the release notes say to, verify the signature, and install from it. It is built in CI from the signed image, on a path no local build exercises | 45 min |
| The published qcow2 boots | Feed it to `deploy/proxmox/create-vm.sh` as `DISK_IMAGE`. This is the same artefact the HA check below consumes, so doing that one covers this | 15 min |

### What is expensive, and skipped on purpose

Saying this plainly is the point of the list; a checklist that implies more
coverage than it has is worse than a short one.

- **A root filesystem on RAID, verified by pulling a disk.** Two hours, a
  hand-written kickstart and a second ESP built in `%post`. It was verified
  once. It is an install-time layout outside the `corium:` surface, so a
  release that does not touch the Containerfile does not move it, and it is
  not re-run every time.
- **Bare metal.** Every verification this project has ever recorded was on a
  Proxmox virtual machine. The ISO install is the same path a physical machine
  takes, which is the argument for believing it works, but it is an argument
  and not a measurement.
- **Every cloud-init datasource but Proxmox's.** Eight are listed as
  supported on the strength of cloud-init supporting them.
- **Calico, Helm add-ons, air-gapped bundles, and `join.tokenFrom` over HTTPS
  with an auth file.** Modelled and rendered correctly, never run in anger.

### Things a machine can check, that CI does not

- `corium-agent version` reports the release, not `dev` or a bare SHA. The
  first tag is what makes `git describe --tags` in the `build` task meaningful.
- The image carries `org.opencontainers.image.version` matching its tag.
- `podman run --rm <image> k0s version` matches `build/k0s.lock`.
- `mise run docs-sync` produces no diff.
- **`mise x "github:Corium-OS/Corium[exe=cctl]@X.Y.Z" -- cctl version` reports
  the release.** This is the one item here that cannot be checked before a tag
  exists: the archives are attached by the release job, and whether an installer
  picks the right one out of four is a question about the file names it sees. A
  release candidate is where that gets answered. `mise x` rather than
  `mise use -g`, so that checking a candidate does not put it in your own
  global tool set.

  Quote it and pin it, and the release notes must too. zsh globs `[...]`, so an
  unquoted command dies in the shell with `no matches found` before mise sees
  it. And the version is required twice over: mise's `github` backend resolved
  a bare `github:Corium-OS/Corium[exe=cctl]` to `0.2.0-rc.4` while that was the
  newest tag, prerelease or not; and mise refuses a release younger than its
  `age` setting allows, so a just-published version reports `no versions found
  ... matching date filter` until it is old enough. Both were found by a reader
  of the 0.2.0 notes, after release.
- `sha256sum --check --ignore-missing SHA256SUMS` passes against a downloaded
  archive, and `cosign verify-blob --key cosign.pub --signature SHA256SUMS.sig
  SHA256SUMS` verifies.
