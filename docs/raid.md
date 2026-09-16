# Software RAID

Corium can build software RAID arrays from a node's spare disks, declared in the
same `corium:` block as everything else. Putting the **root filesystem** on RAID
is a different problem with a different answer, and this page covers both.

---

## Two problems, not one

| | Spare disks | Root filesystem |
|---|---|---|
| Declared in `corium:` | yes, `raid[]` | no |
| When it happens | first boot | install time |
| Install paths | every one | ISO / bare metal only |
| Mechanism | `corium-agent` runs `mdadm` | Anaconda Kickstart |

The split is not a design preference, it is arithmetic. By the time anything in
the `corium:` block is read, the root filesystem is already deployed, mounted,
and running the process doing the reading. Nothing at that point can move it
onto an array. A redundant root has to be arranged by whatever laid the disk
down in the first place, which means the installer.

---

## Spare disks

```yaml
#cloud-config
corium:
  role: controller+worker
  raid:
    - name: data
      level: 1
      devices:
        - /dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi1
        - /dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi2
      filesystem: ext4
      mountPoint: /var/lib/corium/data
```

At first boot the node builds `/dev/md/data`, formats it, mounts it, writes an
`/etc/fstab` entry so later boots mount it too, and records the array in
`/etc/mdadm.conf` so it reassembles.

Every field is in the [configuration reference](reference.md#310-raid).

### Use `/dev/disk/by-id`

Kernel names — `/dev/sdb`, `/dev/sdc` — are handed out in discovery order. That
order is not guaranteed to be the same on the next boot, and on a *first* boot
it may not be the order you assumed when you wrote the configuration. Building
an array is destructive, so naming the wrong disk is expensive.

`/dev/disk/by-id/...` names the hardware. Use it.

### It refuses to destroy data

A device that already carries a filesystem, a partition table, or another
array's metadata stops the bootstrap:

```
device /dev/sdb already holds a filesystem or array member signature (TYPE=ext4);
refusing to overwrite it. Set wipe: true on this array to consent to erasing
these devices
```

`wipe: true` is the consent. It is off by default because the failure modes are
not symmetric: a node that refuses to finish bootstrapping is an afternoon, and
a disk that was silently consumed is not recoverable by any amount of care
afterwards.

Re-running bootstrap on a node whose array already exists **adopts** it. It is
not rebuilt, and a filesystem already on it is not reformatted.

### Why this is not just cloud-init

cloud-init has no RAID support. Its `disk_setup` module does partitions and
filesystems, and its documentation has said for years that mdadm support is
anticipated. So the honest alternative to this feature is a `runcmd` block
calling `mdadm` by hand, and there are three things that gets wrong.

**The array exists before k0s starts.** This is the one that matters. `runcmd`
runs late, and races the kubelet. Lose that race and containerd writes its state
into the directory on the *root* disk that the array is about to be mounted
over. The mount then hides it. The data is still there, consuming root disk
space, invisible to everything that will ever look for it — and the node reports
a full disk with no obvious cause.

Corium builds arrays before k0s is installed, and writes a drop-in so the k0s
unit itself waits for the mount:

```ini
# /etc/systemd/system/k0scontroller.service.d/10-corium-raid.conf
[Unit]
RequiresMountsFor=/var/lib/corium/data
```

The fstab entry is `nofail`, so a node whose array did not come back still boots
and can be logged into. Kubernetes is what refuses to start, loudly, instead of
running without its storage.

**It refuses to destroy data**, as above. A hand-written `mdadm --create` does
not ask.

**It is idempotent.** Bootstrapping twice adopts the array rather than rebuilding
it, and rewrites its own fstab line rather than appending a second one.

### Checking an array

```bash
cat /proc/mdstat
sudo mdadm --detail /dev/md/data
findmnt /var/lib/corium/data
```

A healthy mirror shows `[UU]`. A degraded one shows `[U_]`, and keeps serving
reads and writes while it does.

### Replacing a failed disk

```bash
# 1. Remove the failed member.
sudo mdadm --manage /dev/md/data --remove /dev/disk/by-id/<failed>

# 2. Physically replace it, then add the new one.
sudo mdadm --manage /dev/md/data --add /dev/disk/by-id/<new>

# 3. Watch it rebuild.
cat /proc/mdstat
```

Declaring a `spares:` list does step 1 and 2 on its own, the moment a member
fails.

The `corium:` block is not consulted again after the first boot, so none of this
needs a configuration change — and editing the configuration will not undo a
manual repair.

---

## Root filesystem on RAID

This is an install-time decision, taken from the ISO. It is not available on the
qcow2 or cloud-image paths, because those ship a disk layout that is already
decided.

> **This works. It was verified end to end on a two-disk machine, including
> pulling the first disk and booting from the second.** It is not something
> Corium models in the `corium:` block, because by the time that block is read
> the root filesystem is already mounted — but the procedure below is tested,
> not inferred.
>
> Anaconda will not give you a redundant boot on its own. Three manual steps
> below are what make the difference between a mirrored filesystem and a
> machine that survives losing a disk.

### What is already in place

The image needs nothing added:

- `mdadm` ships in `fedora-bootc`.
- `mdadm` and its systemd units are **in the initramfs**, which is what lets an
  array be assembled early enough to mount root from it.
- Anaconda's installer runtime carries `mdadm`, and its storage module processes
  `part` and `raid` regardless of which payload installs the OS.

### `/boot` on RAID1 is fine

Anaconda's GRUB2 backend accepts `mdarray` for `/boot`, at RAID1 only, with
metadata `0.90`, `1.0` or `1.2`. Members must be partitions rather than whole
disks. `GRUB2.install()` then loops over the array's members and installs to
every backing disk, so a mirrored `/boot` really is bootable from either.

### The ESP is where it comes apart

Anaconda *permits* `/boot/efi` on RAID1 — `platform.py` lists `mdarray` with
`PLATFORM_RAID_LEVELS: [RAID1]` and `PLATFORM_RAID_METADATA: ["1.0"]`, and
blivet forces metadata 1.0 for that mount point specifically, because firmware
cannot read 1.1 or 1.2. So the obvious layout is not rejected at install time.

It fails later, on bootc specifically. `bootupd` works out which partition the
ESP is by reading `/sys/class/block/<name>/partition`; an md device has no such
attribute, and the installation stops:

```
error: boot data installation failed: installing component EFI:
Updating EFI firmware variables: Adding new EFI boot entry:
Failed to read /sys/class/block/md125/partition: No such file or directory
```

The recommended shape is therefore **one independent, non-RAID ESP per disk**,
which is the model `bootupd` is built around: it walks from `/boot` to the
physical disks and finds the ESP colocated with each. A RAID1 `/boot` hands it
both disks for free.

### Kickstart

`bootc-image-builder` embeds a kickstart into an `anaconda-iso` build. It adds
the `ostreecontainer` line itself, and **nothing else** — partitioning, network,
locale and users are all yours to supply. A `[customizations.installer.kickstart]`
block cannot be combined with other installer customizations.

```toml
# config.toml
[customizations.installer.kickstart]
contents = """
text --non-interactive
zerombr
clearpart --all --initlabel --disklabel=gpt
ignoredisk --only-use=sda,sdb

# One ESP, on the first disk. The second one is built in %post, because
# Anaconda has no kickstart idiom for an unmounted ESP.
part /boot/efi --fstype=efi --size=600 --ondisk=sda

# /boot mirrored, metadata 1.2, members are partitions.
part raid.11 --size=2048 --ondisk=sda --fstype=mdmember
part raid.12 --size=2048 --ondisk=sdb --fstype=mdmember
raid /boot --level=RAID1 --device=boot --fstype=ext4 raid.11 raid.12

# Root mirrored. Sized rather than grown, so space is left at the end of the
# second disk for the ESP that %post creates there.
part raid.21 --size=16384 --ondisk=sda --fstype=mdmember
part raid.22 --size=16384 --ondisk=sdb --fstype=mdmember
raid / --level=RAID1 --device=root --fstype=ext4 raid.21 raid.22

lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --device=link --activate --onboot=on
reboot
"""
```

Note `--fstype=mdmember` on the members, which Anaconda requires, and
`--device=<name>` rather than `--device=md0`: it is a name, not a device node.
There is no `--metadata` option; blivet picks it.

### The three steps Anaconda leaves to you

An install with the kickstart above boots fine — and then does **not** survive
losing the first disk. Each of these was found by pulling the disk and watching
what happened.

**1. Build a second ESP and register it.** Anaconda creates one ESP, on the
first disk. It says so itself during the install:

```
boot loader stage2 device boot is on a multi-disk array, but boot loader
stage1 device sda1 is not. A drive failure in boot could render the system
unbootable.
```

It means it. Without a second ESP the firmware reports
`No bootable option or device was found` and stops.

There is no kickstart idiom for a second, unmounted ESP — `part none
--fstype=efi` is taken literally as a mount point called `none`, and the install
dies binding `/mnt/sysimagenone`. So leave room on the second disk by sizing the
last RAID member instead of growing it, and build the ESP in `%post`:

```bash
%post --nochroot
sgdisk --new=3:0:+600M --typecode=3:EF00 /dev/sdb
partprobe /dev/sdb; sleep 2
mkfs.vfat -F32 /dev/sdb3
mkdir -p /tmp/esp2 && mount /dev/sdb3 /tmp/esp2
cp -a /mnt/sysimage/boot/efi/. /tmp/esp2/
sync; umount /tmp/esp2
efibootmgr --create --disk /dev/sdb --part 3 \
  --label "Fedora (mirror)" --loader '\EFI\fedora\shimx64.efi'
%end
```

**2. Do the rest outside `%post`.** On a bootc install, `%post` writes into
`/mnt/sysimage/etc` and `/mnt/sysimage/var` **do not survive**. Disk-level work
does, which is why the block above is only `sgdisk`, `mkfs` and `efibootmgr`. A
`sudoers` drop-in and a log file written the same way both vanished silently.

**3. Add `nofail` to `/boot/efi` in `/etc/fstab`.** This is the step that is easy
to miss, because the machine boots perfectly until the day the first disk dies —
and then lands in emergency mode with healthy, mounted, mirrored filesystems.
The reason:

```
$ systemctl show boot-efi.mount -p RequiredBy -p WantedBy
RequiredBy=local-fs.target
WantedBy=
```

fstab mounts `/boot/efi` by the UUID of the *first* disk's ESP. Lose that disk
and the mount fails; because it is `RequiredBy` rather than `WantedBy`,
`local-fs.target` fails with it and systemd drops to emergency mode. Adding
`nofail` flips it:

```
RequiredBy=
WantedBy=local-fs.target
```

`/etc` is persistent and three-way merged on bootc, so this edit survives image
upgrades.

### Verified

With all three in place, the first disk detached, and the machine booted from
the second alone:

```
md126 : active raid1 sda2[1]
      2094080 blocks super 1.2 [2/1] [_U]     → /boot
md127 : active raid1 sda1[1]
      16759808 blocks super 1.2 [2/1] [_U]    → /sysroot

$ systemctl is-system-running
degraded
$ findmnt /boot/efi
(not mounted — its disk is gone)
```

The node came up, took SSH, and `bootc status` still answered. Arrays degraded
and serving, which is what a mirror is for.

### Rough edges that remain

- **`bootc-generic-growpart.service` fails on every boot.** It derives the
  partition from `/sys/class/block/<name>/partition`, which an md device does
  not have: `cat: /sys/class/block/md126/partition: No such file or
  directory`. Not fatal — the unit fails, the system reports `degraded`, and
  everything else works — but it is the same assumption that breaks `bootupd`
  when the ESP itself is on RAID ([bootc#947](https://github.com/bootc-dev/bootc/discussions/947)).
  Size the root array explicitly rather than relying on it to grow.
- **After a failure, `/boot/efi` is not mounted.** The surviving ESP is there
  and the machine boots from it, but fstab still names the dead one. Repoint it
  before the next `bootc upgrade`, or bootloader updates have nowhere to go.
- **ESP synchronisation on update was not tested here.** `bootupd` v0.2.28+
  updates every ESP it finds ([bootupd#855](https://github.com/coreos/bootupd/pull/855),
  and this image carries v0.2.35), but
  [bootupd#1076](https://github.com/coreos/bootupd/issues/1076) — secondary
  ESPs not receiving `grub.cfg` — is open. Re-verify by pulling a disk after
  your first upgrade, not just after the install.

### What does not apply

These are often quoted together as though root-on-RAID were hopeless. Read them
separately:

| Problem | Where | Applies? |
|---|---|---|
| `bootupctl` fails on an mdraid ESP | [bootc#947](https://github.com/bootc-dev/bootc/discussions/947) | **No.** That report put `/boot/efi` on RAID. The layout here deliberately does not |
| Installing across multiple parent devices | [bootc#1911](https://github.com/bootc-dev/bootc/pull/1911) | **No.** Shipped since bootc v1.15.1; this image carries v1.16.10 |
| Updating every ESP, not just the booted one | [bootupd#855](https://github.com/coreos/bootupd/pull/855) | **No.** Shipped in bootupd v0.2.28; this image carries v0.2.35 |
| A custom kickstart's partitioning silently ignored | [bib#695](https://github.com/osbuild/bootc-image-builder/issues/695) | Not seen here, but check `lsblk` rather than assume |

Fedora CoreOS solves this declaratively with Ignition's `boot_device.mirror`,
which builds the array in the initramfs and replicates `/boot` per disk. bootc
has no equivalent. That gap, rather than anything in this repository, is why
`raid[]` covers spare disks only.

### Worth weighing first

A cluster that tolerates losing a node does not need each node to tolerate
losing a disk. The cheaper answer to a dead disk is usually to reprovision the
node — which is the model the rest of Corium is built around, and which the
image-based upgrade path makes fast.

Root-on-RAID buys a surviving root filesystem. It does not, today, reliably buy
an uninterrupted boot.

---

## What RAID is not

**It is not a backup.** A mirror replicates `rm -rf` to both disks at the same
speed it replicates everything else. It protects against a disk failing, and
against nothing else.

**It is not cluster redundancy.** Losing a node takes its workloads down whether
or not its disks were mirrored. If the data matters, it wants to be somewhere
that survives the node — object storage, a replicated volume, or a backup you
have restored from at least once.

The case where an array earns its place is local state that is expensive to
rebuild and cheap to keep: a large image cache, a local persistent volume behind
something that is already replicated, scratch space you would rather not lose
mid-job.

---

## See also

- [Configuration reference §3.10](reference.md#310-raid)
- [Feature support](features.md)
- [mdadm(8)](https://man7.org/linux/man-pages/man8/mdadm.8.html)
- [Anaconda Kickstart reference](https://pykickstart.readthedocs.io/en/latest/kickstart-docs.html)
