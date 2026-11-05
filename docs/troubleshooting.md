# Troubleshooting

This page lists failures by what you see, not by their cause. Each entry tells
you what to look at first and links to the page that covers it in full.

## Read the journal first

The journal answers almost every first-boot question:

```bash
journalctl -u corium-bootstrap
```

If the management API is enabled, you can read it from your workstation
instead:

```bash
cctl logs <node> --unit corium-bootstrap
```

The agent validates the whole configuration before it changes anything, and it
reports all problems at the same time. So one read usually shows the full list.

---

## The node boots but no cluster appears

The journal says that there is no Corium configuration. The node found no
configuration in any of the four sources. It booted as an ordinary machine and
reported this. This is a supported result, not an error. Make sure that your
platform delivered the user-data, and that a standalone document has no
`corium:` wrapper. See [where configuration comes from](reference.md).

The node has no account to log in to. cloud-init creates the login user. A node
that boots without configuration has no user and no password. You can still
reach it. Under [ADR 11](adr/0011-api-on-by-default.md), it serves its
enrolment route and shows a pairing code on the console. `cctl enroll --config`
can then tell it what it is, including the `users:` that gives you a login. If
you have no console, reprovision the node with a configuration that declares a
user.

`cctl` says that the node is not enrolled. The node waits for a claim.
`api.enabled: true` with no CA is maintenance mode. Such a node holds its
bootstrap until somebody runs `cctl enroll` with the pairing code from its
console. If you want the node to start without a claim, give it
`api.operatorCA` instead. See [cctl](cli.md).

Two nodes have the same name. Corium derives a stable name from the machine
ID. If you cloned a disk *after* its first boot, the clone has the same machine
ID. Clear `/etc/machine-id` on the clone, or set `node.name` explicitly.

---

## The node is `NotReady` and stays there

You set `cni: custom`. This is expected. Nothing installed a network yet, and
the cluster waits for you. See [Cilium](cilium.md) for a full example.

In other cases, look at the CNI DaemonSet:

```bash
sudo k0s kubectl -n kube-system describe pod
```

---

## Networking

Images do not pull, but everything else works. The cause is almost always DNS.
A static address that you set through a hypervisor usually has no resolver.
For example, `ipconfig0` in Proxmox has no field for one. Set a nameserver. The
symptom is misleading. The node answers ping, SSH works, and the API answers,
but Kubernetes stops with `lookup quay.io: Try again`. See
[Proxmox](install/proxmox.md).

A node is `Ready`, but logs, exec and port-forward fail across an overlay. The
kubelet registered the address of the physical NIC, not the overlay address.
Set `nodeAddress: true` on the overlay interface. Corium sets this at
bootstrap, so you must reprovision the node. See
[WireGuard overlay](wireguard-overlay.md).

Large payloads fail, but small payloads work. This is an MTU problem, usually
under an overlay. See [WireGuard overlay](wireguard-overlay.md).

A WireGuard peer never completes a handshake. The endpoint is unreachable, or
the UDP port is closed. WireGuard does not answer a peer that it cannot
authenticate, so you get no error. The only sign is a missing handshake.

---

## Joining a cluster

Joining controllers wait forever. The usual cause is an empty or expired token.
A controller token is a cluster-admin credential with a short expiry. Make sure
that the file that the joining node reads is not zero bytes. See
[HA cluster](install/ha-cluster.md).

A node joined the wrong cluster. If a configuration source fails for a reason
other than "absent", the search stops. This rule exists to prevent this
problem. So a source answered with the wrong content, and did not fail. Read
the order of precedence in the [configuration reference](reference.md).

---

## Installing

The install from the ISO repeats forever. The boot order must be disk first,
ISO second. An empty disk has no UEFI boot entry, so the firmware goes to the
ISO, and the ISO installs. After the install, the disk has an entry, and the
firmware boots it. If the ISO is first, the node installs itself again at each
reboot. On a physical machine, this is the boot order of the firmware. See
[bare metal](install/baremetal.md).

A PC that you installed from the ISO boots to a login prompt with no account.
No configuration reached it. So `corium-agent` left it unconfigured, and Corium
creates no default user. You can still continue. The node serves its enrolment
route, and the console shows a pairing code above the prompt.
`cctl enroll <address> --code <code> --config node.yaml` gives it a role and a
login. A `CIDATA` seed stick and a reboot also work, because the agent wrote no
bootstrap marker. See
[bare metal](install/baremetal.md#if-you-install-without-a-seed).

An interrupted install leaves nothing that can boot. Anaconda erases the disk
early. Do not stop the VM during an install.

`crun: pivot_root: Invalid argument` in a rescue system. The rescue system runs
from a ramfs, so `bootc install to-disk` through podman cannot work. Write the
qcow2 to the disk instead. See [Rescue mode](install/rescue.md).

The machine does not survive the loss of its first disk. A RAID root needs
manual steps that Anaconda does not do. See [Software RAID](raid.md).

---

## Upgrades and images

An upgrade is refused as unsigned. `cctl upgrade` refuses an image that the
signing policy of the node accepts unsigned. A registry that you push to
yourself falls under the permissive default of the policy. This stays true
until you sign the image and declare it in the policy. See
[Building your own image](derived-images.md).

Nodes refuse every image that you build. A node enforces the policy that it
runs now, not the policy in the image that it moves to. Roll out the policy
first, then the images that need it. See
[Building your own image](derived-images.md).

A node came up broken and went back to the previous image without help.
greenboot did this, and it is the expected behavior. See [Upgrades](upgrades.md).

Content that you wrote to `/var` disappeared after an upgrade. The installer
writes `/var` once, at install time, and never again. Declare state directories
in `tmpfiles.d` instead. See [Concepts](concepts.md).

An edit in `/etc` stopped following the image. OSTree does a three-way merge on
`/etc`. If you edit a file by hand, your version stays. This is the contract,
not a bug. See [Concepts](concepts.md).

---

## Reading an API refusal

`cctl` shows the HTTP status of the node. [cctl](cli.md) tells you what to look
at for each code. It also explains why a node signs its own certificate, and
what a fingerprint mismatch means.

---

## Still stuck

- [Concepts](concepts.md): the model under Corium, if the behavior seems random
- [Feature support](features.md): whether Corium supports what you want to do
- [Changelog](https://github.com/Corium-OS/Corium/blob/main/CHANGELOG.md): what
  each release changed, and what is known to be broken
