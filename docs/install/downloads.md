# Downloads

Each release publishes four artefacts, and all of them are signed:

| Artefact | Where | Use it for |
|---|---|---|
| Installer ISO | `ghcr.io/corium-os/corium-iso` | Bare metal. Installs without an operator |
| qcow2 disk | `ghcr.io/corium-os/corium-qcow2` | Proxmox, KVM, libvirt, and the import paths of most clouds |
| OS image | `ghcr.io/corium-os/corium` | A node upgrade, `bootc install`, derived images |
| `cctl` | Attached to the release page | Node management from your own machine |

> The release page of each version gives its exact coordinates and digests:
> [latest release](https://github.com/Corium-OS/Corium/releases/latest).
> They change with each release. Below, replace `0.5.0` with your version.

---

## Install `cctl`

`cctl` runs on your machine, never on a node. The release publishes archives
for linux and macOS, on amd64 and arm64. There is no Windows build.

With [mise](https://mise.jdx.dev):

```bash
mise use -g 'github:Corium-OS/Corium[exe=cctl]@0.5.0'
```

Keep the quotes and the version. Both are necessary:

- The quotes are for your shell. zsh reads `[...]` as a glob. Without quotes,
  the command fails with `no matches found` before mise runs.
- The version is necessary because an install with no version gets the newest
  tag, and this includes release candidates. mise also refuses a release that
  is younger than its `age` setting allows. So for a short time after a release,
  you cannot install it (`no versions found ... matching date filter`).

To install it by hand instead:

```bash
version=0.5.0
os=linux          # darwin on macOS
arch=amd64        # arm64 on Apple silicon, and on 64-bit Arm linux
base=https://github.com/Corium-OS/Corium/releases/download/v${version}

curl -fsSLO "${base}/cctl_${version}_${os}_${arch}.tar.gz"
curl -fsSLO "${base}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS    # shasum -a 256 on macOS
tar -xzf "cctl_${version}_${os}_${arch}.tar.gz"
```

You need `--ignore-missing` because `SHA256SUMS` lists every platform, and you
downloaded only one. Without it, the check fails on the three archives that you
do not have. That failure looks the same as a real failure.

You can also build it: `git clone https://github.com/Corium-OS/Corium.git && mise run
build` produces `bin/cctl` and `bin/corium-agent`.

---

## Get the ISO or the qcow2

The disk artefacts are in a registry, not on the release page. Three methods
give you the same bytes. Use the one that matches the tools you have.

```bash
# With oras. One command, and it names the file for you.
oras pull ghcr.io/corium-os/corium-iso:0.5.0
```

```bash
# Without oras. ghcr issues pull tokens anonymously for public packages.
token=$(curl -s "https://ghcr.io/token?scope=repository:corium-os/corium-iso:pull" | jq -r .token)
curl -L -H "Authorization: Bearer ${token}" -o corium-0.5.0-x86_64.iso \
  https://ghcr.io/v2/corium-os/corium-iso/blobs/sha256:<layer digest from the release notes>
```

```
# From a browser, no tooling. The release notes carry the link.
https://<cdn>/corium-0.5.0-x86_64.iso
```

The third method is a CDN mirror. It is a convenience, not the release. It can
be down. If it ever disagrees with the registry, the registry is correct. The
mirror never overwrites a file, because each file has its version in its name.
The mirror has no index that you can browse, so the link is in the release
notes of that version.

---

## Check what you got

You have two different questions, and each has a different answer.

Who built this, and from what? The signature answers this. It is attached to
the artefact in the registry. You do not need to get a key first:

```bash
cosign verify ghcr.io/corium-os/corium-iso@sha256:<manifest digest> \
  --certificate-identity-regexp 'https://github.com/Corium-OS/Corium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

If the verification passes, it names the workflow run, the commit and the tag
that produced the artefact. This information comes from a public transparency
log.

Did I receive those exact bytes? The hash in the release notes answers this:

```bash
sha256sum corium-0.5.0-x86_64.iso
# must equal the hash printed in the release notes
```

For `cctl`, use the signed checksum file. When you verify it, you verify every
binary that it lists:

```bash
curl -fsSLO "${base}/SHA256SUMS.sig"
curl -fsSLO https://raw.githubusercontent.com/Corium-OS/Corium/v${version}/build/files/usr/share/corium/cosign.pub
cosign verify-blob --key cosign.pub --signature SHA256SUMS.sig SHA256SUMS
```

A recent `cosign` prints `Flag --signature has been deprecated, please use
--bundle` before it gives the result. Ignore this message. The result is the
`Verified OK` on the next line. Do not use `--bundle` instead. It expects a
Sigstore bundle, and the release publishes a plain signature.

---

## Or build them yourself

`mise run artefacts` produces the qcow2, the raw disk and the ISO from any image
that you can pull, including an image that you changed. It needs a Linux host
and `sudo`, because the builder mounts the filesystem that it creates. See the
[quick start](../quickstart.md).

The release does not publish a raw disk image. It is approximately 5 GB,
and the ISO already covers bare metal.

---

## Why it is published this way

Nothing is attached to the GitHub release. A release asset must be smaller than
2 GiB, and the ISO is approximately 2.4 GB. The qcow2 fits. But if the
release publishes only the artefact that fits, readers think that the ISO was
forgotten. This limit also has a benefit. A release asset is an opaque blob,
and its provenance stops at the person who uploaded it. A registry artefact has
a digest address and a signature. The difference is between *this is the file we
meant to give you* and *this is a file*.

`cctl` is attached, because for `cctl` the same reasoning gives the opposite
result. It is three megabytes, a person downloads it to set up a workstation,
and the common installers read release assets, not registries.

There is no `.sha256` file next to the download, because it proves nothing.
A checksum that comes from the same place as its file is only as trustworthy as
that place. The hash in the release notes is different. It is the digest of the
layer of the artefact, and the manifest that `cosign` signed names that digest.
The chain is complete:

```
cosign verify   ->  this manifest was produced by that workflow run
the manifest    ->  names this layer digest
sha256sum       ->  the file you hold has that digest
```

No link in the chain needs the download host to be honest. For this reason, you
can download 2.4 GB from the method that is fastest for you.

---

## Tools these routes want

No method needs all of these tools.

| Tool | Needed for |
|---|---|
| [mise](https://mise.jdx.dev) | The one-line `cctl` install, and artefact builds |
| `curl`, `tar` | Anything that you download by hand |
| `sha256sum` | A check of what you got. macOS: `shasum -a 256` |
| [`cosign`](https://github.com/sigstore/cosign) | Signature verification |
| [`oras`](https://oras.land) or `jq` | The registry methods |
