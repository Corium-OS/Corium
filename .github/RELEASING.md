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

### The download mirror

The ISO is also copied to `s3.thoughtless.eu/corium-releases`, which exists
only so the release notes can offer a link someone can click. It is
self-hosted, and the release path treats it accordingly:

- The step is `continue-on-error`. A mirror that is down costs a release
  nothing.
- It fetches a byte back as an anonymous client before advertising the URL,
  because an advertised link that 403s is worse than no link.
- The notes mention a mirror only when one exists, and say plainly that the
  registry is the release and the mirror is a convenience.

Anonymous read comes from a bucket policy, not from a canned ACL -- the
storage accepts `--acl public-read` and silently discards it. If the mirror
ever starts refusing anonymous reads, that policy is the first thing to check:

```bash
aws --endpoint-url https://s3.thoughtless.eu \
  s3api get-bucket-policy --bucket corium-releases
```

CI writes with a `corium-ci` account scoped to that bucket alone -- it cannot
read the other buckets on that storage, and cannot create new ones. Do not
replace it with the storage's root credentials.

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
  first tag is what makes `git describe --tags` in the Makefile meaningful.
- The image carries `org.opencontainers.image.version` matching its tag.
- `podman run --rm <image> k0s version` matches `build/k0s.lock`.
- `python3 website/sync-docs.py` produces no diff.
