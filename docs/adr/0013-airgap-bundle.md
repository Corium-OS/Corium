# 13. The air-gap bundle travels with the Kubernetes version, as its own signed artefact

Status: accepted

Amends ADR 10 (what a Kubernetes version is made of) and adds decision 18.

## Context

A node with no route to a public registry can run k0s, but not its workloads. kube-proxy, CoreDNS, the konnectivity agent, the CNI and the pause image are container images the kubelet would normally pull. k0s's answer is the air-gap bundle: an OCI archive of exactly those images, published as a release asset per version and per architecture. k0s watches `<data-dir>/images` and imports every bundle it finds there into containerd when the worker starts ([air-gap install](https://docs.k0sproject.io/stable/airgap-install/), checked 2026-11-21). The bundle is several hundred megabytes (422 MB for `v1.36.6+k0s.0` on amd64), and it is per k0s release: the images in it must match the k0s the node runs.

That last fact is what ADR 10 changed. Before it, `docs/features.md` could say "bake a bundle into the image with a `COPY`", because the image's k0s was the only k0s a node ran. Since ADR 10 the k0s version is no longer the image's. A bundle baked into the image matches the floor only, and a node that sets `kubernetes.version` to another version in the window runs a k0s whose system images are not in that bundle. The node then pulls them, which is what an air-gapped node cannot do, and the failure appears after the version change, not at install.

Two more facts from the same page, also checked 2026-11-21. The bundle is only needed on nodes that run a kubelet: "copy the bundle only to the worker nodes, controller nodes don't use it". And `spec.images.default_pull_policy: Never` is offered as an optional hard guarantee, because images already in containerd are used without a pull.

## Decision

A Kubernetes version is made of two artefacts: the extension that fills `/usr/bin/k0s`, and the bundle that fills `/var/lib/k0s/images`. Both travel with the version, not with the image.

### 1. The bundle is its own signed OCI artefact, in a sibling repository

`k0s-extensions.yml` publishes, for every version by architecture pinned in `build/k0s.lock`, both the extension and the bundle under the same tag scheme (`<version with + replaced by _>-<arch>`). The bundle lives in the repository beside the extension's: `ghcr.io/corium-os/k0s-airgap` by default, and `<mirror>-airgap` for a custom `kubernetes.mirror`. The mapping is mechanical in both directions (`internal/k0s.BundleReference`), so a mirror serves a version through two sibling repositories and never through a second field in the configuration.

The bundle does not go inside the sysext. That would add about 420 MB to every node that pins a version, connected or not, and the bundle is not `/usr` content anyway: k0s reads it from `/var/lib/k0s/images`, so a `/usr`-only extension would need a symlink or a copy into `/var` regardless.

### 2. The same trust chain as the extension

The upstream bundle is downloaded from the k0s GitHub release, and its sha256 is checked against the fourth column of `build/k0s.lock`. That column pins the digest GitHub publishes for the release asset; the publishing workflow verifies the downloaded bytes against it before signing anything. The artefact is then pushed with `oras`, signed by digest with cosign twice (keyless plus the key signature a node's `policy.json` can enforce), and pulled on the node with `skopeo copy` under the same `policy.json` as the extension. `policy.json` gains a `sigstoreSigned` rule for `ghcr.io/corium-os/k0s-airgap`, and a test pins that rule to the shipped key so the two cannot drift apart the way they once did.

### 3. Pulled with the extension, through `kubernetes.mirror`, before anything stops

The trigger is the extension pull itself, not a new field. `kubernetes.mirror` without `kubernetes.version` is already refused by validation, so "a mirror is set" and "a non-floor version is pulled" are the same event, and the bundle travels with exactly the version the extension carries. AGENTS.md §11 asks before a new key exists, and no new key is needed. The accepted cost is that a connected node pinning a version also pulls about 420 MB, but the kubelet uses those preloaded images either way, so on a connected node the bundle is the same images arriving earlier.

The pull happens at the same point as the extension's: before `k0s install` at bootstrap, and before the drain on a day-two swap. A bundle pull that fails (a mirror that does not carry the sibling `k0s-airgap` repository, an unsigned artefact, a full disk) aborts the version change while the node is still in service, named by the pull that failed. A node must never drain, swap its version, and only then discover it has no images.

### 4. Placed in `/var/lib/k0s/images` as a real file, before k0s starts, on every role with a kubelet

`single`, `controller+worker` and `worker` get the bundle; a controller-only node does not, which is what upstream says. It is written as a real file, not a symlink into the store: k0s imports what it finds in the directory, and a dangling link imports as nothing. Both call sites run before the service starts, because k0s imports the bundles present in the directory when the worker starts.

### 5. A version change removes the previous version's bundle

`ClearBundles` deletes every `k0s-airgap-bundle-*.tar` in `/var/lib/k0s/images` except the running version's, at the same point `ClearStaged` clears `/var/lib/k0s/bin`. Each bundle is several hundred megabytes, and `/var` filling with one tarball per minor is a slow disk failure nobody would diagnose. Only files carrying our naming are removed. The directory is k0s's, and a bundle an operator placed by hand under another name stays.

### 6. What this record deliberately does not do

- The floor case stays open. A node that sets no `kubernetes.version` and can reach no registry at all still has no bundle, and making every image about 420 MB heavier for it is refused. The answer is a variant image (a `deploy/airgap/` built like `deploy/zfs/`, which `COPY`s the floor's bundle and links it into `/var/lib/k0s/images` at bootstrap). That is follow-up work, recorded here so "the floor is air-gapped too" is a decision rather than an accident.
- `default_pull_policy: Never` stays a `k0s.patch` choice. Upstream marks it optional; images preloaded from the bundle are used without a pull. A site that wants the hard guarantee sets it through the escape hatch.
- Helm add-ons are not in the bundle. An air-gapped node still needs its charts and their images from the internal registry. The docs say so, so `addons` does not look air-gap ready.

## Consequences

GHCR carries one artefact of about 400 MB per version by architecture in the window: twenty artefacts, roughly 8 GB today. A retention policy on `k0s-airgap` that keeps only the current window is part of the follow-up.

A mirror must carry both repositories. That is now the contract of `kubernetes.mirror`, and a mirror serving only `k0s` fails the bundle pull with the reference named. The reference documentation says so.

A day-two version change on an air-gapped worker downloads the next minor's bundle before the drain, several hundred megabytes over the internal registry, on the path that already downloads the extension. The swap's timeout already sits above a download of that size.

The failure mode the issue describes is closed at its source: the bundle that reaches a node is the one for the version the node runs, delivered and verified by the same mechanism as the binary, and the node refuses to change version rather than run images it does not have.

## Alternatives considered

**Bundle inside the sysext.** Rejected on two facts: it is not `/usr` content, and it adds about 420 MB to every node that pins a version. See decision 1.

**Bake the floor's bundle into every image.** Rejected: about 420 MB on every image for the one case a mirror already serves, and the image is not where a per-version artefact belongs since ADR 10. The variant image keeps this option open where it is actually wanted.

**A new `kubernetes.airgap: true` field.** Considered and declined for now. The trigger "an extension is being pulled" is already exactly "a mirror is set and a version is pinned", and AGENTS.md §11 requires maintainers to agree to a new key. If the 420 MB on connected pinning nodes ever becomes a real cost, that field is where it goes; the fetch sites are already the two places the extension is pulled.

**`spec.images` passthrough only.** Remains the answer for sites that can reach an internal registry with the images mirrored. This record is for the ones that cannot.
