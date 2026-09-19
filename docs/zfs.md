# ZFS on data disks

Corium can build ZFS pools from a node's data disks, declared in the same
`corium:` block as everything else and created at first boot, before k0s. It is
the storage feature to reach for when a data disk holds something that wants
checksums, compression or snapshots — a local persistent volume, an image cache,
a build tree — where [`raid[]`](raid.md) gives a plain block device and ZFS gives
a volume manager and filesystem in one.

Two things make ZFS different from RAID, and both are worth knowing before you
start.

- **It needs the ZFS image.** ZFS is a kernel module Fedora does not ship, so it
  is not in the base image. Build the variant from
  [`deploy/zfs/`](../deploy/zfs/README.md) and run your nodes on that.
- **It does not cover the root disk.** Like RAID, this is data disks only. Root
  on ZFS is a different problem with a harder answer; see
  [ADR-0007](adr/0007-zfs-data-disks.md).

---

## Build the image first

```bash
IMAGE=ghcr.io/corium-os/corium:0.2.0 mise run zfs-image
```

The reasoning, the build caveats, and what to do when a kernel bump outruns the
pinned OpenZFS release are all in
[deploy/zfs/README.md](../deploy/zfs/README.md). The rest of this page assumes a
node running that image.

Declaring `zfs[]` on a plain base image is not silently ignored: the node stops
at first boot with `loading the zfs kernel module: ...`, because a pool that was
asked for and cannot be built is an error.

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

At first boot the node creates `tank` as a mirror, sets `compression=lz4` so
every dataset inherits it, creates `tank/data` mounted at `/var/lib/corium/data`,
and writes a drop-in so k0s waits for that mount.

Every field is in the [configuration reference](reference.md#316-zfs).

### Pools, vdevs and datasets

A **pool** is the unit of storage. It is made of one or more **vdevs**, and it
stripes across all of them: a pool's redundancy is that of its least redundant
vdev, and it survives only as long as every vdev does. A vdev is a group of
disks at one redundancy level:

| `type` | Redundancy | Minimum disks | Like |
|---|---|---|---|
| `stripe` (default) | none | 1 | RAID 0 |
| `mirror` | any one survivor | 2 | RAID 1 |
| `raidz` | one failure | 2 | RAID 5 |
| `raidz2` | two failures | 3 | RAID 6 |
| `raidz3` | three failures | 4 | — |

A **dataset** is a filesystem within the pool, with its own mount point and
properties. A pool with no datasets listed is a single filesystem mounted at the
pool's mount point; datasets let one pool carry several filesystems that share
its capacity but differ in `recordsize`, `quota`, `compression` and the rest.

### Set `ashift` at creation

`options` are pool properties, and the one that matters most is `ashift`.
`ashift: "12"` sizes the pool for 4K-sector disks, which is almost everything
now. It is **fixed for the life of the pool** and cannot be changed afterwards,
so it is worth setting deliberately rather than letting ZFS guess from a disk
that may misreport.

`filesystemOptions` are set on the pool's root dataset and inherited by every
dataset under it — this is where `compression` belongs, so you set it once.

### Use `/dev/disk/by-id`

Exactly as with RAID: kernel names (`/dev/sdb`) are handed out in discovery
order, which is not stable across boots and may not be what you assumed on a
*first* boot. Creating a pool is destructive, so name the hardware —
`/dev/disk/by-id/...` — not the enumeration order.

### It refuses to destroy data

A device that already carries a filesystem, a partition table, or another pool's
or array's label stops the bootstrap:

```
device /dev/sdb already holds a filesystem or array member signature (TYPE=ext4);
refusing to overwrite it. Set wipe: true on this array to consent to erasing
these devices
```

`wipe: true` on the pool is the consent, off by default, for the same reason it
is on RAID: a node that refuses to finish bootstrapping is an afternoon; a disk
silently consumed is not recoverable afterwards.

A device may belong to a pool **or** a RAID array, never both. Listing one in
each is rejected at validation, before anything touches a disk — otherwise
whichever ran first at boot would win and corrupt the other.

Re-running bootstrap on a node whose pool already exists **imports and adopts**
it rather than rebuilding it, and reconciles dataset properties rather than
duplicating anything.

### Why this is not just cloud-init

cloud-init has no ZFS support, so the honest alternative is a `runcmd` calling
`zpool`, and it gets the same three things wrong that a `mdadm` `runcmd` does
(see [software RAID](raid.md#why-this-is-not-just-cloud-init)):

**The pool exists before k0s starts.** A `runcmd` races the kubelet; lose the
race and containerd writes into the directory on the *root* disk that the dataset
is about to be mounted over, where it is invisible afterwards while still filling
root. Corium creates pools before k0s and writes a drop-in so the k0s unit waits
for the mount:

```ini
# /etc/systemd/system/k0scontroller.service.d/10-corium-zfs.conf
[Unit]
RequiresMountsFor=/var/lib/corium/data
```

**It refuses to destroy data**, as above. `zpool create` by hand takes `-f`.

**It is idempotent.** Bootstrapping twice adopts the pool rather than trying to
recreate it.

---

## Checking a pool

```bash
zpool status tank
zpool list
zfs list
findmnt /var/lib/corium/data
```

`zpool status` shows each vdev and its state; a healthy mirror reports `ONLINE`
for the pool and both members. A pool degraded by a failed disk keeps serving
while it reports `DEGRADED`.

## Replacing a failed disk

```bash
# Swap the failed device for its replacement, by stable path.
sudo zpool replace tank /dev/disk/by-id/<failed> /dev/disk/by-id/<new>

# Watch it resilver.
zpool status tank
```

The `corium:` block is not consulted again after the first boot, so a repair
done by hand is not undone by the configuration, and none of this needs a
configuration change.

## Snapshots and scrubs

Two things ZFS gives you that nothing else here does, both operator-run rather
than configured:

```bash
sudo zfs snapshot tank/data@before-upgrade   # instant, copy-on-write
sudo zpool scrub tank                         # verify every checksum
```

A scrub on a schedule is worth setting up (a systemd timer, or your cluster's
own cron) — it is how ZFS turns silent disk corruption into something it repairs
from redundancy before you notice.

---

## What ZFS is not, here

**It is not a backup.** A snapshot lives on the same pool as the data; a pool
lost takes its snapshots with it. Snapshots are for *undo*, not for surviving the
hardware. `zfs send` to somewhere else is a backup; a local snapshot is not.

**It is not cluster redundancy.** Losing a node takes its workloads down whether
or not its pool was a mirror. If the data matters beyond the node, it wants to be
somewhere that survives the node — object storage, a replicated volume, or a
backup you have restored from at least once.

**It is not the root filesystem.** This is data disks. See
[ADR-0007](adr/0007-zfs-data-disks.md).

The case where a pool earns its place is local state that is expensive to rebuild
and benefits from what ZFS checks and compresses: a local persistent volume
behind something already replicated, a large image or layer cache, a dataset you
snapshot before a risky change.

---

## Memory: cap the ARC

ZFS's cache, the ARC, will by default grow to half of RAM. On a Kubernetes node
that is memory the kubelet also wants to hand to pods, and the two will compete.
Cap it. A drop-in in the ZFS image, or a `k0s`-independent sysctl-style module
option, keeps ZFS to a budget:

```bash
# e.g. 2 GiB, set via the module option
echo 'options zfs zfs_arc_max=2147483648' | sudo tee /etc/modprobe.d/zfs.conf
```

On an immutable node this belongs in the image (a file under `/usr/lib/modprobe.d`
in your ZFS variant) rather than written by hand, so it survives upgrades.

---

## See also

- [Configuration reference §3.16](reference.md#316-zfs)
- [Building the ZFS image](../deploy/zfs/README.md)
- [ADR-0007: ZFS covers data disks](adr/0007-zfs-data-disks.md)
- [Software RAID](raid.md) — the other storage feature, and when to prefer it
- [OpenZFS documentation](https://openzfs.github.io/openzfs-docs/)
