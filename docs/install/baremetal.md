# A desktop PC, from a USB stick

A spare tower or mini-PC turned into a single-node Kubernetes cluster. Two USB
sticks: one installs the operating system, the other tells the node what it is.

For a dedicated server reached through a provider's rescue system, see
[rescue mode](rescue.md); for a VM, [Proxmox](proxmox.md) or the
[quick start](../quickstart.md).

> Unlike [Proxmox](proxmox.md), no CI run backs this page — nothing in the
> project boots the ISO on physical hardware. If your machine disagrees with a
> step here, that is worth an issue.

## Before you start

- **A 64-bit x86 machine that boots UEFI.** Corium boots UEFI only.
- **A whole disk you are willing to lose.** The installer wipes its target. If
  the machine has more than one disk, disconnect the others.
- **Two USB sticks.** 4 GB or more for the ISO, which is about 2.4 GB; any size
  for the seed.
- **Wired ethernet.** Corium configures no wireless.
- **4 GB of RAM and 32 GB of disk**, 8 GB of RAM more comfortably.
- **A public SSH key.**

## How the install is shaped

Two separate things happen:

1. **The ISO lays down the operating system.** Unattended — Anaconda deploys the
   bootc image embedded in it, with no prompts and no kickstart to write. It
   never asks what the node is.
2. **cloud-init configures the node on its first real boot.** That is where the
   `corium:` block and your login account come from.

A desktop PC has no metadata service for step 2, so you supply it from a
**NoCloud seed**: a USB stick whose filesystem is labelled `CIDATA`. It is the
first datasource Corium probes, and the only offline one. Installing without one
is allowed, and [recoverable](#if-you-install-without-a-seed).

---

## 1. Write the ISO to a stick

Fetch `corium-0.4.0-x86_64.iso` and check it — [downloads](downloads.md) covers
all three routes and the verification.

**`dd` to the wrong device destroys that device.** Run the listing first:

```bash
# linux
lsblk -o NAME,SIZE,MODEL,TRAN
sudo dd if=corium-0.4.0-x86_64.iso of=/dev/sdX bs=4M status=progress oflag=sync
```

```bash
# macOS
diskutil list
diskutil unmountDisk /dev/diskN
sudo dd if=corium-0.4.0-x86_64.iso of=/dev/rdiskN bs=4m
```

`/dev/rdiskN` rather than `/dev/diskN` on macOS: the raw device is roughly an
order of magnitude faster for a write this size.

A graphical writer works too — Rufus, balenaEtcher, GNOME Disks — as long as it
writes the image verbatim. In Rufus that is **DD image mode**, not ISO mode.

## 2. Write the configuration seed

A complete single-node cluster:

```yaml
#cloud-config
corium:
  role: single

users:
  - name: core
    groups: [wheel]
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... you@example.com
```

`role` is the only required field. See [examples](../examples.md) for workers,
add-ons and a custom CNI, and the [reference](../reference.md) for every field.
From a checkout, validate it before you walk to the machine. Validation is
offline and reports every problem at once, and `mise exec` supplies the pinned
Go toolchain, so this works on a machine that has never installed Go:

```bash
mise exec -- go run ./cmd/corium-agent validate node.yaml
```

Two files on a filesystem labelled `CIDATA`. The label is what cloud-init looks
for; nothing else about the stick matters.

```bash
# linux. /dev/sdY is the *second* stick, not the one holding the ISO.
sudo mkfs.vfat -n CIDATA /dev/sdY1
sudo mount /dev/sdY1 /mnt

sudo cp node.yaml /mnt/user-data
printf 'instance-id: corium-01\nlocal-hostname: corium-01\n' | sudo tee /mnt/meta-data
sudo umount /mnt
```

```bash
# macOS
diskutil eraseDisk FAT32 CIDATA MBRFormat /dev/diskN

cp node.yaml /Volumes/CIDATA/user-data
printf 'instance-id: corium-01\nlocal-hostname: corium-01\n' > /Volumes/CIDATA/meta-data
```

Both at the root, both named exactly that. `meta-data` may be nearly empty but
must exist, or cloud-init rejects the seed.

## 3. Set up the firmware

Enter the setup screen — usually `Del`, `F2` or `F12` at power-on — and settle
three things:

| Setting | Value | Why |
|---|---|---|
| Boot mode | UEFI, CSM/legacy off | Corium boots UEFI only |
| Boot order | **Internal disk first, USB second** | See below |
| Secure Boot | Off, if the install does not boot | See below |

**Disk first, USB second** is not a typo. An empty disk has no UEFI boot entry,
so the firmware falls through to the stick and installs; afterwards the disk has
an entry and wins. With the stick first, the machine reinstalls itself on every
reboot, and the install being unattended, nothing on screen says so.

**Secure Boot** is unverified on bare metal. The image ships Fedora's signed
shim, so it may well work; if the machine refuses to boot after the install,
turn it off.

## 4. Install

Plug in **only the ISO stick** — one fewer disk for the installer to consider —
and power the machine on. Anaconda reboots when it is done, a few minutes on an
SSD. Pull the stick during that reboot.

## 5. Configure it

Plug in the seed stick and boot.

cloud-init finds `CIDATA`, creates your user, and `corium-bootstrap` reads the
`corium:` block and brings k0s up. A minute or so from power-on to a `Ready`
node. The console banner reports the node's role, its cluster and whether k0s is
running.

The seed stick can come out once the node is up, or stay in.

## 6. Check it worked

```bash
ssh core@<the node's address>
sudo k0s kubectl get nodes
```

For the kubeconfig and everything after it, carry on with the
[quick start](../quickstart.md#4-check-it-worked). Managing the node with
[`cctl`](../cli.md) is a separate step: the management API is off by default and
a node has to be claimed before it answers.

---

## If you install without a seed

The ISO installs fine on its own. What you get is a healthy host that is in no
cluster and that nobody can log into:

| | After the install |
|---|---|
| `corium-bootstrap` | Ran, found nothing, wrote no marker |
| `corium-apid` | Exited 78. The management API is off by default, so nothing listens on 7443 |
| k0s | Not started |
| Accounts | None. Corium creates no default user, and `PermitRootLogin` is `no` |

The console says as much, above a login prompt nobody can answer:

```
  This node is not part of a cluster.
  address   192.168.0.42
```

**The recovery is the seed stick.** Because no bootstrap marker was written,
nothing about the node is settled — plug a `CIDATA` stick in, reboot, and step 5
runs as if it were the first boot. Installing first and configuring afterwards
is a legitimate order, not a mistake to undo.

Two things that look like alternatives and are not:

- **`corium.config=<url>` on the kernel command line** does configure the node —
  it is the third source in the chain. It creates no account, so you end up in a
  cluster you still cannot log into. Worth it only pointing at a document that
  enables the API, which at least gives you `cctl`.
- **Waiting to be claimed**, the pairing-code flow from
  [`awaiting-config.yaml`](../examples/awaiting-config.yaml), needs a document
  saying `api.enabled: true`. That is a three-line seed, not no seed: with no
  configuration at all the API never starts, so no pairing code is ever printed.

A machine that boots and waits to be told everything is
[`deploy/appliance/`](https://github.com/Corium-OS/Corium/tree/main/deploy/appliance),
which bakes that document into `/usr/share/corium/config.yaml` as an image
default. It is not published — you build it — and a machine built from it
belongs to whoever reaches port 7443 first, so read its README before putting
one on a network you share.

---

## What goes wrong on a desktop that does not on a hypervisor

**It installs itself again on every boot.** The boot order. Disk first, USB
second, and pull the ISO stick once the install is done.

**The node comes up with no user and no cluster.** cloud-init did not find the
seed. Check the filesystem label is exactly `CIDATA`, that both `user-data` and
`meta-data` sit at the root of it, and that `user-data` starts with the
`#cloud-config` line. `sudo cloud-init query --all` on the node says which
datasource it settled on.

**The node has an address but resolves no names.** A static address configured
without a resolver. Leave the machine on DHCP for the first boot; if it has to
be static, it is cloud-init's `network:` block that sets it, and that block has
to carry a nameserver of its own.

**Nothing on the console after the firmware.** Almost always the boot mode:
a firmware in CSM/legacy finds no bootloader on a UEFI-only image, and says so
in a way that reads as a dead machine.

The rest is indexed by symptom in [troubleshooting](../troubleshooting.md).
