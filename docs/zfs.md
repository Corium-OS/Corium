# ZFS on data disks

Corium can build ZFS pools from the data disks of a node. You declare them in
the same `corium:` block as the rest of the configuration. Corium creates them at
first boot, before k0s. Use this storage feature when a data disk holds data
that needs checksums, compression, or snapshots. Examples are a local persistent
volume, an image cache, or a build tree. [`raid[]`](raid.md) gives a plain block
device. ZFS gives a volume manager and a filesystem in one.

Two things make ZFS different from RAID. Know them before you start.

- **It needs the ZFS image.** ZFS is a kernel module that Fedora does not ship,
  so it is not in the base image. Build the variant from
  [`deploy/zfs/`](../deploy/zfs/README.md) and run your nodes on that image.
- **It does not cover the root disk.** Like RAID, it is for data disks only.
  Root on ZFS is a different and harder problem. See
  [ADR-0007](adr/0007-zfs-data-disks.md).

---

## Build the image first

```bash
IMAGE=ghcr.io/corium-os/corium:0.5.0 mise run zfs-image
```

[deploy/zfs/README.md](../deploy/zfs/README.md) gives the reasons, the build
caveats, and the steps to take when a new kernel is newer than the pinned
OpenZFS release supports. The rest of this page assumes a node that runs that
image.

If you declare `zfs[]` on a plain base image, Corium does not ignore it. The node
stops at first boot with `loading the zfs kernel module: ...`. A pool that you
asked for and that Corium cannot build is an error.

---

## A pool

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

At first boot, the node does these steps:

1. It creates `tank` as a mirror.
2. It sets `compression=lz4`, so every dataset inherits it.
3. It creates `tank/data` and mounts it at `/var/lib/corium/data`.
4. It writes a drop-in, so k0s waits for that mount.

Every field is in the [configuration reference](reference.md#316-zfs).

### Pools, vdevs and datasets

A **pool** is the unit of storage. It contains one or more **vdevs**, and it
stripes data across all of them. The redundancy of a pool is the redundancy of
its least redundant vdev. The pool survives only while every vdev survives. A
vdev is a group of disks at one redundancy level:

| `type` | Redundancy | Minimum disks | Like |
|---|---|---|---|
| `stripe` (default) | none | 1 | RAID 0 |
| `mirror` | any one survivor | 2 | RAID 1 |
| `raidz` | one failure | 2 | RAID 5 |
| `raidz2` | two failures | 3 | RAID 6 |
| `raidz3` | three failures | 4 | — |

A **dataset** is a filesystem in the pool, with its own mount point and
properties. A pool with no datasets is a single filesystem at the mount point of
the pool. With datasets, one pool can hold several filesystems. They share the
capacity of the pool, but they can have different values for `recordsize`,
`quota`, `compression`, and other properties.

### Set `ashift` at creation

`options` are pool properties, and the most important one is `ashift`.
`ashift: "12"` sizes the pool for 4K-sector disks, which are almost all disks
today. It is **fixed for the life of the pool**, and you cannot change it later.
Set it yourself. Do not let ZFS guess it from a disk that can report wrong
values.

Corium sets `filesystemOptions` on the root dataset of the pool, and every
dataset under it inherits them. Put `compression` here, so you set it once.

### Use `/dev/disk/by-id`

This is the same as for RAID. The kernel gives names (`/dev/sdb`) in discovery
order. This order can change between boots, and it can be different from what
you expect on a *first* boot. Creating a pool destroys data, so name the
hardware with `/dev/disk/by-id/...`, not the enumeration order.

### It refuses to destroy data

If a device already has a filesystem, a partition table, or the label of
another pool or array, the bootstrap stops:

```
device /dev/sdb already holds a filesystem or array member signature (TYPE=ext4);
refusing to overwrite it. Set wipe: true on this array to consent to erasing
these devices
```

`wipe: true` on the pool gives the consent. It is off by default, for the same
reason as on RAID. A node that does not finish its bootstrap costs you an
afternoon. A disk that is erased without warning cannot be recovered.

A device can belong to a pool **or** a RAID array, never both. If you list a
device in both, validation rejects it before anything touches a disk. Without
this check, the one that ran first at boot wins and corrupts the other.

If you run the bootstrap again on a node where the pool already exists, it
**imports and adopts** the pool and does not rebuild it. It reconciles dataset
properties and does not duplicate anything.

### Why this is not just cloud-init

cloud-init has no ZFS support. The only alternative is a `runcmd` that calls
`zpool`. It gets the same three things wrong as a `mdadm` `runcmd`
(see [software RAID](raid.md#why-this-is-not-just-cloud-init)):

**The pool exists before k0s starts.** A `runcmd` runs at the same time as the
kubelet. If the kubelet is first, containerd writes into the directory on the
*root* disk where the dataset mounts later. After the mount, you cannot see
these files, but they still fill the root disk. Corium creates pools before k0s
and writes a drop-in, so the k0s unit waits for the mount:

```ini
# /etc/systemd/system/k0scontroller.service.d/10-corium-zfs.conf
[Unit]
RequiresMountsFor=/var/lib/corium/data
```

**It refuses to destroy data**, as above. `zpool create` by hand takes `-f`.

**It is idempotent** (safe to run twice). A second bootstrap adopts the pool and
does not try to create it again.

---

## Checking a pool

```bash
zpool status tank
zpool list
zfs list
findmnt /var/lib/corium/data
```

`zpool status` shows each vdev and its state. A healthy mirror reports `ONLINE`
for the pool and for both members. If a disk fails, the pool reports `DEGRADED`
and continues to serve data.

## Replacing a failed disk

```bash
# Swap the failed device for its replacement, by stable path.
sudo zpool replace tank /dev/disk/by-id/<failed> /dev/disk/by-id/<new>

# Watch it resilver.
zpool status tank
```

Corium does not read the `corium:` block again after the first boot. The
configuration does not undo a repair that you do by hand, and the repair needs
no configuration change.

## Snapshots and scrubs

ZFS gives you two things that no other feature here gives. You run both by
hand. You do not configure them:

```bash
sudo zfs snapshot tank/data@before-upgrade   # instant, copy-on-write
sudo zpool scrub tank                         # verify every checksum
```

Run a scrub on a schedule, with a systemd timer or a cron job in your cluster.
A scrub finds silent disk corruption, and ZFS repairs it from redundancy before
you notice it.

---

## What ZFS is not, here

**It is not a backup.** A snapshot is on the same pool as the data. If you lose
the pool, you lose its snapshots too. Snapshots are for *undo*, not for a
hardware failure. `zfs send` to another location is a backup. A local snapshot
is not.

**It is not cluster redundancy.** If you lose a node, its workloads stop, even
if its pool was a mirror. If you need the data after the loss of the node, keep
it in a location that survives the node. Examples are object storage, a
replicated volume, or a backup that you restored from at least once.

**It is not the root filesystem.** It is for data disks. See
[ADR-0007](adr/0007-zfs-data-disks.md).

A pool is useful for local state that is expensive to rebuild and that gains
from the checksums and compression of ZFS. Examples are a local persistent
volume behind a service that is already replicated, a large image or layer
cache, or a dataset that you snapshot before a risky change.

---

## Memory: cap the ARC

By default, the ZFS cache (the ARC) grows to half of the RAM. On a Kubernetes
node, the kubelet also wants to give that memory to pods, and the two compete.
Set a limit. A drop-in in the ZFS image, or a sysctl-style module option that
does not depend on `k0s`, keeps ZFS in a budget:

```bash
# e.g. 2 GiB, set via the module option
echo 'options zfs zfs_arc_max=2147483648' | sudo tee /etc/modprobe.d/zfs.conf
```

On an immutable node, put this setting in the image, as a file under
`/usr/lib/modprobe.d` in your ZFS variant. Do not write it by hand. In the
image, it survives upgrades.

---

## See also

- [Configuration reference §3.17](reference.md#317-zfs)
- [Building the ZFS image](../deploy/zfs/README.md)
- [ADR-0007: ZFS covers data disks](adr/0007-zfs-data-disks.md)
- [Software RAID](raid.md): the other storage feature, and when to prefer it
- [OpenZFS documentation](https://openzfs.github.io/openzfs-docs/)
