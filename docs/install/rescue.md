# Rescue mode

A rescue system is the live environment a provider boots instead of your disk
when you ask for one. It is the usual way onto a dedicated server that has no
remote console worth using, and it is enough to install Corium: the image
installs itself, and the rescue only has to run it.

There are two routes below, and the rescue system decides which one you get,
not you. Route A is three commands. Route B exists because some rescue systems
cannot run a container at all, and OVH's is one of them.

## Before you start

- The machine booted into its rescue system from the provider's panel, and a
  shell on it.
- The target disk identified. Both routes overwrite one whole disk.
- A public SSH key, and the configuration you want the node to have. A
  dedicated server has no metadata service, so both routes seed those by hand.
- For route B, the Corium qcow2 on the rescue system, and `qemu-img`, `sgdisk`,
  `growpart`, `resize2fs` and `setfattr` available there. See
  [downloads](downloads.md) for the qcow2 and how to check what you got.

## Which route

One command decides it:

```bash
findmnt -n /
```

`rootfs rootfs` means the rescue runs entirely from a ramfs, and route A cannot
work there. No podman flag works around it: `pivot_root` is refused when the
current root is the initial rootfs, so every container fails with

```
Error: OCI runtime error: crun: pivot_root: Invalid argument
```

Take route B in that case. A real filesystem — `tmpfs`, `ext4`, `overlay` —
means containers will run, so take route A, and any failure you hit there is
something else.

OVH's rescue is ramfs, which makes the case this page is most often read for a
route B case.

## Route A — install with podman

`bootc install to-disk` runs from inside the Corium image and writes a complete
disk: partition table, ESP, bootloader, and the ostree deployment. Nothing has
to be converted or resized afterwards.

### 1. Write the disk

> **Warning.** `--wipe` destroys everything on the target disk, and it is not
> recoverable. Confirm which disk you mean first: a rescue system numbers disks
> in its own order and not necessarily the one the installed system will use,
> and a machine delivered with several disks usually has the OS on the small
> NVMe and the data on the large ones.

```bash
lsblk -o NAME,SIZE,TYPE,MODEL
```

```bash
podman run --rm --privileged --pid=host \
  -v /dev:/dev -v /var/lib/containers:/var/lib/containers \
  --security-opt label=type:unconfined_t \
  ghcr.io/corium-os/corium:0.3.0 \
  bootc install to-disk --wipe /dev/nvme0n1
```

### 2. Seed a configuration and a user

Do this before the reboot. A dedicated server has no metadata service and no
seed device, so cloud-init finds nothing and the node comes up unconfigured —
`no corium configuration found, leaving node unconfigured` in the agent's
journal. Worse, **the login user is created by cloud-init too**, so a node that
boots without configuration has no account to SSH into. Corium ships no root
account, and its sshd drop-in sets `PermitRootLogin no`.

The cheapest fix is a kernel argument, which `bootc install` will write for
you — so it goes into step 1 rather than after it:

```bash
  bootc install to-disk --wipe \
    --karg corium.config=https://boot.example.com/node-01.yaml \
    /dev/nvme0n1
```

That covers Corium's own configuration but not the user. To get both, drop a
NoCloud seed onto the installed system instead — cloud-init looks for one in
`/var/lib/cloud/seed/nocloud` before it gives up, and `ds-identify` finds it
without any extra configuration.
[Route B writes one](#2-write-the-nocloud-seed), SELinux labels and all; mount
the root filesystem `bootc install` created and do the same there.

### 3. Reboot out of rescue

Disable rescue mode in your provider's panel and reboot. Leaving rescue enabled
boots the rescue again, and the installed system never runs.

## Route B — when the rescue cannot run a container

The way through is the published qcow2, which is the same installed system
`bootc install` would have produced, laid out for a 10 GiB disk. Write it, then
grow the root partition to the disk you actually have.

### 1. Write the qcow2 and grow the root

> **Warning.** `qemu-img convert` writes straight to the raw device: it
> destroys everything on the target disk, and it is not recoverable. Confirm
> which disk you mean first: a rescue system numbers disks in its own order and
> not necessarily the one the installed system will use, and a machine
> delivered with several disks usually has the OS on the small NVMe and the
> data on the large ones.

```bash
lsblk -o NAME,SIZE,TYPE,MODEL
```

```bash
apt-get install -y qemu-utils        # or the distribution's equivalent

qemu-img convert -O raw -p corium-0.3.0-x86_64.qcow2 /dev/nvme0n1
sync && partprobe /dev/nvme0n1

sgdisk -e /dev/nvme0n1               # move the backup GPT to the end of the real disk
growpart /dev/nvme0n1 4
e2fsck -fp /dev/nvme0n1p4
resize2fs /dev/nvme0n1p4
```

Partition 4 is the root filesystem; `lsblk -o NAME,SIZE,FSTYPE,LABEL` confirms
it by its `root` label. The resize preserves the filesystem UUID, so the
bootloader entries written into the image keep pointing at the right place.

### 2. Write the NoCloud seed

This is what gives the node a configuration and an account to log in with, and
it has to be in place before the reboot. Mount the root filesystem and write
the seed into the deployment's `/var`, which in an ostree system lives at
`ostree/deploy/default/var`:

```bash
mount /dev/nvme0n1p4 /mnt/root
mkdir -p /mnt/root/ostree/deploy/default/var/lib/cloud/seed/nocloud
cd /mnt/root/ostree/deploy/default/var/lib/cloud/seed/nocloud

cat > meta-data <<'EOF'
instance-id: node-01
local-hostname: node-01
EOF

cat > user-data <<'EOF'
#cloud-config
users:
  - name: core
    groups: [wheel, adm, systemd-journal]
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... you@example.com

corium:
  api:
    enabled: true
EOF
```

That `corium:` block names no role on purpose. A dedicated server is exactly
the machine you have not decided about yet — its disks, its name and often its
role are things you settle once you can see it — and a document with no role
holds the bootstrap until you send one:

```bash
cctl enroll <address>:7443 --code <code> --config node-01.yaml
```

Write `role: single` here instead and the node builds a single-node cluster the
moment you claim it, with a name derived from the machine ID and no array under
it. That is a legitimate thing to want, and `cctl` asks before it does it — but
undoing it is `cctl reset` and a second install's worth of waiting. See
[configuration §3.2](../reference.md#32-role).

The extra groups are this page's only departure from the `users:` block the
other install pages use — `adm` and `systemd-journal` so the account can read
the journals on a machine you may have no other way into. See
[Proxmox step 3](proxmox.md#3-describe-the-node) for the plainer version.

> **Warning.** SELinux is the trap here. Corium runs enforcing, and files
> created from a rescue system that does not know about SELinux carry no label
> at all, which cloud-init is not allowed to read. A node that boots with an
> unlabelled seed behaves exactly like a node with no seed at all, which makes
> this the failure most likely to cost an afternoon.

Label them by hand:

```bash
for p in ../seed . meta-data user-data; do
  setfattr -n security.selinux -v "system_u:object_r:cloud_var_lib_t:s0" "$p"
done
```

`getfattr -n security.selinux --only-values user-data` should print
`system_u:object_r:cloud_var_lib_t:s0`.

### 3. Leave rescue mode

Unmount everything, **disable rescue mode in the provider's panel**, and
reboot. Leaving rescue enabled boots the rescue again, and the installed system
never runs.

---

## OVH dedicated servers

OVH's rescue (`rescue12-customer`) runs from a ramfs, so route B is the one
that works. It also has no overlayfs and no `pids` cgroup controller, which
produce their own errors first and send you looking in the wrong direction:

| What you see | Cause |
|---|---|
| `'overlay' is not supported over ramfs` | no overlayfs in the rescue kernel |
| `the requested cgroup controller 'pids' is not available` | kernel built without it; `--pids-limit=0` gets past it |
| `crun: pivot_root: Invalid argument` | the real blocker — ramfs root |

Those first two are worth chasing only far enough to reach the third.

### The UEFI boot entry

The qcow2 carries both the removable path `EFI/BOOT/BOOTX64.EFI` and
`EFI/fedora/shimx64.efi`, so a UEFI firmware finds a bootloader without help.
Adding an explicit entry costs nothing and removes the question. Do it after
route B's step 1 and before you leave rescue mode:

```bash
apt-get install -y efibootmgr
efibootmgr -c -d /dev/nvme0n1 -p 2 -L "Corium" -l '\EFI\fedora\shimx64.efi'
```

Recent hardware is UEFI, and `[ -d /sys/firmware/efi ] && echo UEFI` from the
rescue says so for the machine in front of you.

### Networking

OVH hands out the server's address over DHCP, and the image ships no
`NetworkManager` connection profile, so the interface comes up on its own.
Nothing has to be configured for the node to be reachable at the address the
panel shows.

The BLS entry includes `console=ttyS0`, so the boot is visible over OVH's IPMI
serial console if it does not come back.

## What to read next

- [Downloads](downloads.md) — fetching an artefact and checking it
- [Quick start](../quickstart.md) — the configuration surface, including the
  sources Corium reads when there is no cloud-init
- [Configuration](../reference.md) — every field of the `corium:` block
