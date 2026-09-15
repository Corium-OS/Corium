# Upgrades

Kubernetes ships with the operating system, so upgrading either means booting a
new image. One version axis, one mechanism, one rollback path.

Everything on this page was run on a three-controller cluster rather than
inferred; the numbers are measured.

---

## The model

A Corium node runs an image. Upgrading replaces that image and reboots. There
is no in-place package upgrade, no `k0s` binary to swap, and no second
mechanism that can move the Kubernetes version independently of the OS.

That last point is why [k0s Autopilot is not used](features.md#out-of-scope).
Autopilot upgrades Kubernetes by replacing the k0s binary on disk; Corium's
lives in the read-only `/usr`. Two mechanisms able to move the version on their
own is a worse position than one that cannot.

### What survives

| Path | Across an upgrade |
|---|---|
| `/usr` | Replaced wholesale. It is the image |
| `/etc` | Three-way merged: your edits survive, image defaults update around them |
| `/var` | Untouched. `/var/lib/k0s`, container storage, the cluster CA, your data |

Verified on a live node: after switching image and rebooting, the cluster CA
was still in place, the node kept its name, and `corium-bootstrap.service` did
not run — the marker in `/var/lib/corium` survived, so the node did not try to
bootstrap itself a second time.

---

## Upgrading a node

```bash
# See what is running and what, if anything, is staged.
sudo bootc status

# Pull a newer build of the image this node already tracks.
sudo bootc upgrade

# Or move it to a different image or tag.
sudo bootc switch ghcr.io/corium-os/corium:main
```

Neither reboots by default. They stage a deployment for the next boot, which is
what makes the maintenance window yours to choose:

```
Queued for next boot: ghcr.io/corium-os/corium:main
  Version: c41716d
  Digest: sha256:38c2194904b1de400a6f2366760c85dae5e45bb7f5f500612bb428817e850bb6
```

Add `--apply` to reboot immediately, or reboot when you are ready.

### Only the difference is transferred

Measured on a real switch between two builds of this image:

```
layers already present: 65; layers needed: 11 (214.0 MB)
Deploying...done (15 seconds)
```

214 MB moved for an image of roughly 2.3 GB, because the layers the two builds
share were already on disk. This is the practical argument for an OS that is an
OCI image: upgrades are incremental for the same reason application images are,
with no bespoke delta format to maintain.

---

## Upgrading a cluster

Nodes are cattle, but the control plane still has a quorum to respect.

### One node at a time

```bash
# 1. Stop scheduling and move the workloads off.
kubectl drain <node> --ignore-daemonsets --delete-emptydir-data

# 2. Stage the new image and reboot into it.
ssh <node> sudo bootc switch ghcr.io/corium-os/corium:<tag>
ssh <node> sudo systemctl reboot

# 3. Wait for it to come back Ready, then let it take work again.
kubectl wait --for=condition=Ready node/<node> --timeout=5m
kubectl uncordon <node>
```

A node that has been drained and rebooted comes back in about 45 seconds and
rejoins on its own. It does not re-bootstrap: the marker in `/var/lib/corium`
is what stops it.

### Controllers

With three controllers you can lose one and keep etcd quorum, so upgrade them
one at a time and wait for `Ready` in between. Never take two out of three down
together — that is quorum lost and an API server that stops accepting writes.

With five, two at a time is survivable. With two, there is no quorum to lose
gracefully and the cluster will be unavailable during each reboot.

If you use [HA](reference.md#38-ha), rebooting the controller holding the
virtual IP moves it to another controller. Clients pointed at the VIP reconnect
on their own; the failover was verified by hard-stopping the holder.

---

## Rolling back

```bash
sudo bootc rollback
sudo systemctl reboot
```

Rollback is instant and downloads nothing — `Next boot: rollback deployment`.
It reorders boot entries between the deployment you are running and the
previous one, both of which are already on disk.

It is also symmetric: rolling back makes the image you left the new rollback
target, so running it again returns you to where you were. Verified in both
directions on a live controller.

```bash
sudo bootc status   # shows booted, staged and rollback
```

Two things rollback does **not** undo:

- **Anything written to `/var`.** A Kubernetes version that migrated etcd data
  does not un-migrate because you booted an older image. Read the k0s release
  notes before skipping Kubernetes minor versions.
- **Cluster state.** Rolling back one node does not roll back the cluster.

---

## Choosing what to track

| Tag | Moves | For |
|---|---|---|
| `ghcr.io/corium-os/corium:main` | On every push to `main` | Following development |
| `ghcr.io/corium-os/corium:<commit>` | Never | Pinning a node to an exact build |
| `ghcr.io/corium-os/corium@sha256:...` | Never | Pinning by digest, the strongest form |

Production nodes should track a tag that does not move under them, or a digest.
`bootc status` always reports the digest actually booted, whatever the tag said
at the time.

### Nothing updates itself

Corium masks `bootc-fetch-apply-updates.timer`. A node will not pull a new
image and reboot on its own, because a Kubernetes node that reboots unprompted
is an outage nobody scheduled. Upgrades are something you do, drain-aware and
in an order you chose.

---

## Verifying what you are about to boot

Images published from `main` are signed with
[cosign](https://docs.sigstore.dev/), keyless: there is no private key, and the
signing identity is the GitHub Actions workflow itself. The signature is
attached to the image digest rather than to a tag, because tags move and a
signature on a moving tag says nothing about what it points at now.

```bash
cosign verify ghcr.io/corium-os/corium:main \
  --certificate-identity-regexp 'https://github.com/Corium-OS/Corium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Two caveats, both stated plainly because a signature is exactly the kind of
thing people assume more from than it delivers.

**The verification command above is not yet confirmed to work end to end.** The
signature is definitely published — the registry holds a signature artefact for
the current digest, as an OCI index under the referrers fallback tag — but
`cosign verify` did not complete in our testing. Cosign v3 changed how
signatures are stored, moving from the older `<digest>.sig` tag to OCI
referrers, and consumers expecting the old layout need
`--new-bundle-format=false` at signing time. If verification matters to you,
test it before relying on it, and please report what you find.

**Signing is not enforcing.** A stock Corium node ships the default
`/etc/containers/policy.json`, which is `insecureAcceptAnything`: the image is
signed, and nothing on the node checks that signature at pull time. A node will
happily boot an unsigned image today.

To make a node enforce it, ship a policy requiring a sigstore signature for
this repository and bake the trust root into your own derived image — a
deliberate, reviewable change. See
[bootc: image signatures](https://bootc.dev/bootc/security.html).

---

## Upgrading Kubernetes specifically

The Kubernetes version is whatever `build/k0s.lock` pins, so upgrading it means
changing that file, rebuilding, and rolling the new image out as above.

```
K0S_VERSION=v1.36.4+k0s.0
K0S_SHA256_amd64=...
```

CI checks that the image really ships the version the lock file names, so a
lock file and an image cannot silently disagree.

Before skipping a Kubernetes minor version, read the
[k0s release notes](https://docs.k0sproject.io/stable/releases/): k0s follows
upstream Kubernetes, which does not support skipping minors on the control
plane.
