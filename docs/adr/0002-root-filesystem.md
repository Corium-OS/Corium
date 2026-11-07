# 2. Use ext4 for the root filesystem

Status: accepted

## Context

`fedora-bootc` ships an empty `/usr/lib/bootc/install/`, so it does not set the
type of the root filesystem. Every tool that installs the image needs this
type. Without it, `bootc-image-builder` fails with
`missing required info: DefaultRootFs`.

xfs was the first choice, for three reasons:

- The CoreOS and RHEL lineage use it for container hosts.
- It works well with the many small files of an overlay image store.
- It grows online.

The build of the image with xfs failed on the target host:

```
XFS (loop0p4): Superblock has unknown incompatible features (0xc0) enabled.
XFS (loop0p4): Filesystem cannot be safely mounted by this kernel.
```

`xfsprogs` on Fedora 44 creates filesystems with features that kernels older
than 6.10 refuse to mount. `bootc-image-builder` mounts the root filesystem on
the *build host* to populate it. Thus the build fails there, because Proxmox 8
runs Debian 12 with kernel 6.8.

This problem did not affect the disk itself. The disk would have booted
correctly, because the VM runs a current Fedora kernel. The problem was that
we could not produce the image.

## Decision

Declare `root-fs-type = "ext4"` in `/usr/lib/bootc/install/00-corium.toml`.

The image declares the type. The build command line does not pass it as
`--rootfs`. Thus the image describes itself, and every installer agrees on the
type without extra input.

## Consequences

The image builds on any host with a kernel that is still in service. This
includes the homelab Proxmox and the CI runners that people actually have.
ext4 grows online with `resize2fs`, so you can still resize the disk of a node.

For a Kubernetes node, the practical difference between ext4 and xfs is small.
It is not a good reason to limit who can build the image.
