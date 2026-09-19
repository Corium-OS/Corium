# A Corium image with ZFS

The base Corium image has no ZFS. This derives one that does, for nodes whose
data disks you want to manage as ZFS pools — checksummed, compressed, snapshot-
capable local storage behind a persistent-volume provisioner or an image cache.

It is built here rather than published for the same reason the appliance image
is: it is a variant with costs the base image should not impose on every node.
Read [docs/adr/0007-zfs-data-disks.md](../../docs/adr/0007-zfs-data-disks.md)
for the reasoning; the essentials are below.

---

## Why it is not in the base image

OpenZFS is a kernel module, and two facts about it decide everything here.

- **Fedora does not ship it.** ZFS is under the CDDL, which Fedora treats as
  incompatible with the GPL kernel, so there is no package to `dnf install`. The
  module has to be compiled from source.
- **Fedora has no stable kABI.** A module compiled against one kernel will not
  load on another, and unlike RHEL there is no kABI-tracking guarantee to paper
  over the difference. So the module must be built against the *exact* kernel it
  will run on, and rebuilt whenever that kernel changes.

Both facts fit bootc rather than fighting it: an image is one kernel, and this
recipe bakes the matching module into it, versioned together by digest and
replaced wholesale on upgrade. That is cleaner than DKMS on a mutable host,
which rebuilds at runtime and writes state into `/var`. But it is build weight
and build fragility — every kernel bump is a recompile that can fail if OpenZFS
does not yet support the new kernel — and a node that does not use ZFS should
not carry it. Hence a variant, opt in.

---

## Building it

```bash
# Derived from whichever Corium release you are running.
IMAGE=ghcr.io/corium-os/corium:0.2.0 mise run zfs-image

# Pin the OpenZFS release when the default does not support the base kernel.
IMAGE=ghcr.io/corium-os/corium:0.2.0 ZFS_VERSION=2.3.4 mise run zfs-image

# An installer ISO or a qcow2 from it, like any other artefact.
IMAGE=localhost/corium-zfs:dev mise run artefact-qcow2
```

Both need a Linux host with podman, like every other artefact task.

If the build fails in `configure` complaining about the kernel, the OpenZFS
release you asked for predates the base image's kernel. Raise `ZFS_VERSION` to a
release that supports it.

The image you build is **not signed**, exactly as the appliance image is not;
see [deploy/appliance/README.md](../appliance/README.md) for what that means for
`bootc upgrade` and how to add your own signing scope.

---

## Using it

Declare pools in the `corium:` block, on the same footing as `raid[]`. They are
created by `corium-agent` at first boot, before k0s, and k0s is made to wait for
their mounts.

```yaml
#cloud-config
corium:
  role: controller+worker
  zfs:
    - name: tank
      options:
        ashift: "12"
      filesystemOptions:
        compression: lz4
      vdevs:
        - type: mirror
          devices:
            - /dev/disk/by-id/wwn-0x5000c500a0b1c2d3
            - /dev/disk/by-id/wwn-0x5000c500a0b1c2d4
      datasets:
        - name: data
          mountPoint: /var/lib/corium/data
```

The full field reference is in
[docs/reference.md §3.16](../../docs/reference.md#316-zfs), and the guide with
the reasoning and the safety rules is [docs/zfs.md](../../docs/zfs.md).

> The `zfs[]` block only works on an image built from this recipe. On the base
> image the module is absent, so `corium-agent` stops at first boot with
> `loading the zfs kernel module: ...` rather than silently doing nothing. That
> is deliberate: a pool that was asked for and cannot be built is an error, not
> a shrug.

---

## What you get, and what you do not

The image adds the ZFS kernel module, the `zpool`/`zfs` userspace, and the
OpenZFS systemd units, with import done by **scanning** the attached disks
(`zfs-import-scan`) rather than from a cached device list — so a pool follows its
disks onto replacement hardware.

It does **not** put the root filesystem on ZFS. Root on ZFS needs the module in
the initramfs and a bootloader integration bootc does not have a declarative
path for, and it is out of scope for the same reason `raid[]` does not cover the
root disk: by the time the configuration is read, root is already mounted. See
the ADR.
