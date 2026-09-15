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

## Health-gated rollback is not implemented

A node that boots an image where k0s does not start stays there. Nothing
notices, and nothing brings it back. Rolling back is a decision you make, with
`bootc rollback`.

This was attempted with [greenboot](https://github.com/fedora-iot/greenboot),
the usual answer on OSTree systems, and reverted. With greenboot enabled, nodes
rolled back on **every** reboot even when the health check passed:

```
Rollback to previous deployment completed successfully
corium: k0scontroller.service is running and answering
greenboot health-check passed.
```

The rollback is announced before the check runs, and a node walked backwards
one image per reboot until it reached the one it was installed with. Shipping
that would have made every upgrade unreliable in exchange for a safety net that
did not work, so it is out until the interaction between greenboot and bootc is
properly understood.

Until then: after an unattended upgrade, check that nodes came back. The
`download` policy exists partly for this reason — it keeps the reboot, and
therefore the moment of risk, under your control.

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

A release publishes a ladder of tags, so a node can choose how much movement it
accepts.

| Tag | Moves | You get |
|---|---|---|
| `corium@sha256:...` | Never | Exactly one image. The strongest pin |
| `corium:1.4.2` | Never | One release |
| `corium:1.4` | On patch releases | Fixes, no new behaviour |
| `corium:1` | On minor releases | New features, no breaking changes |
| `corium:latest` | On every release | Whatever is newest, including major versions |
| `corium:main` | On every push to `main` | Development builds, unreleased |

Most clusters want `1.4` in production and `1` somewhere they can afford
surprises. `latest` crosses major versions, which is where breaking changes
live by definition.

Prereleases publish only their exact tag: `1.4.0-rc.1` never becomes `1.4` or
`1`, so a node following a stable tag will not pick up a release candidate.

`bootc status` always reports the digest actually booted, whatever the tag
said at the time — which is what you want in an incident.

### Unattended upgrades

A node does nothing on its own by default. A Kubernetes node that reboots
unprompted is an outage nobody scheduled, so `bootc-fetch-apply-updates.timer`
is masked in the image.

Two levels are available when you want more:

```yaml
corium:
  upgrades:
    automatic: download      # none (default) | download | apply
    schedule: "Mon *-*-* 03:00:00"   # systemd OnCalendar, default daily
```

| Policy | What the node does | Reboots itself |
|---|---|---|
| `none` | Nothing. The default | No |
| `download` | Stages a newer image, leaves it queued for the next boot | **No** |
| `apply` | Stages it and reboots | **Yes** |

**`download` is the one most clusters want.** The fetch and the deployment
happen unattended, so the reboot you schedule becomes near-instant — the
expensive part is already done. You still choose the moment, drain first, and
go one node at a time.

`apply` drains the node before rebooting: it cordons, evicts the pods, reboots
into the staged image, and uncordons once k0s is back. A node upgrading itself
therefore reschedules its workloads rather than killing them.

Two behaviours worth knowing, because both are deliberate:

- **A drain that cannot finish cancels the upgrade.** A pod disruption budget
  refusing an eviction is the system working, not a fault. The node uncordons
  itself, stays on its current image with the new one still staged, and tries
  again at the next tick. Forcing a reboot past a PDB would defeat the point of
  having one.
- **A plain worker reboots undrained.** Draining needs cluster admin
  credentials, and only a node running a control plane has them locally. On
  `worker` nodes, `apply` behaves as it did before. Use `download` there and
  drive the reboot from somewhere that can talk to the API.

`schedule` takes any [systemd OnCalendar](https://www.freedesktop.org/software/systemd/man/systemd.time.html)
expression. A randomised delay of up to an hour is applied on top, so a fleet
does not arrive at the registry in lockstep, and missed checks are caught up
after a node has been off rather than waiting for the next window.

Check what a node has staged:

```bash
sudo bootc status
systemctl list-timers 'corium-upgrade-*' 'bootc-*'
```

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

Verified output looks like this:

```
Verification for ghcr.io/corium-os/corium:main --
The following checks were performed on each of these signatures:
  - The cosign claims were validated
  - Existence of the claims in the transparency log was verified offline
  - The code-signing certificate was verified using trusted certificate
    authority certificates
```

If `cosign verify` hangs with no output at all, it is probably your registry
credential helper waiting on something — a locked macOS keychain does this.
Running it with an empty `DOCKER_CONFIG` is a quick way to tell:

```bash
mkdir -p /tmp/emptycfg && echo '{}' > /tmp/emptycfg/config.json
DOCKER_CONFIG=/tmp/emptycfg cosign verify ghcr.io/corium-os/corium:main \
  --certificate-identity-regexp 'https://github.com/Corium-OS/Corium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Nodes enforce it

Images are signed **twice**, because the two signatures answer different
questions.

| Signature | Proves | Used by |
|---|---|---|
| Keyless | Which workflow run built this image, recorded in a public transparency log | A human, running `cosign verify` |
| Key | That the image came from this project | The node, at pull time |

A Corium node ships a policy requiring the key signature for
`ghcr.io/corium-os/corium`, with the public key at
`/usr/share/corium/cosign.pub`. An image from that repository that is not
signed is refused:

```
Source image rejected: A signature was required, but no signature exists
```

The policy is deliberately narrow. Everything else stays permissive, because
k0s pulls its own images from quay.io and docker.io — a blanket policy would
break the cluster rather than secure it.

Verify it against a node yourself:

```bash
cosign verify --key /usr/share/corium/cosign.pub ghcr.io/corium-os/corium:main
```

**Why two signatures rather than one.** A node cannot enforce the keyless one:
`containers-policy.json` matches a signer by `subjectEmail`, and a GitHub
Actions certificate carries a URI instead, so enforcement fails with
`Required email ... not found (got [])`. That is a limitation of
`containers-image`, not a configuration mistake — worth knowing before you
spend an afternoon on it.

See [bootc: image signatures](https://bootc.dev/bootc/security.html).

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
