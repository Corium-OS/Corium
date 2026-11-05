# Quick start

This page takes you from nothing to a working Kubernetes node in four steps. A
fifth step shows how to manage the node without SSH.

If you are in a hurry, this is the short version:

```bash
oras pull ghcr.io/corium-os/corium-qcow2:0.5.0   # download a ready-made disk
printf '#cloud-config\ncorium:\n  role: single\n' > node.yaml
# boot the disk with node.yaml as cloud-init user-data
```

The rest of this page explains each step. The last section covers the mistakes
that cost the most time.

---

If you want to understand the model before you type anything, read
[concepts](concepts.md) first.

## Before you start

Every release publishes a ready-made, signed disk image. So the quick path needs
no build host. You need only two things:

- **A place to run the node**: Proxmox, KVM/libvirt, or any cloud that accepts
  a qcow2 or an ISO.
- **A tool to download it**: [`oras`](https://oras.land), `curl`, or a browser.
  All three work on macOS, Windows, and Linux, and none of them needs `sudo`.

You do not need `podman`, a Linux host, or 20 GB of build space. You need them
only if you change the image and rebuild it yourself. The
[build from source](#build-from-source) section at the end covers that path.

---

## 1. Get a bootable disk

Pull the artefact for the platform where the node will run:

| Artefact | Pull it with | Use it for |
|---|---|---|
| qcow2 disk | `oras pull ghcr.io/corium-os/corium-qcow2:0.5.0` | Proxmox, KVM, libvirt |
| Installer ISO | `oras pull ghcr.io/corium-os/corium-iso:0.5.0` | Bare metal. Installs unattended. See [bare metal](install/baremetal.md) |

The tag above is only an example. **The
[release page](https://github.com/Corium-OS/Corium/releases/latest) of each
version gives its exact coordinates and digests.** They change with every
release, so the release publishes them, and this page does not.

If you do not have `oras`, `curl` or a browser download also works. See
[downloads](install/downloads.md). That page also shows how to make sure that
the file you received is the file that the release published:

```bash
cosign verify ghcr.io/corium-os/corium-qcow2:0.5.0 \
  --certificate-identity-regexp 'https://github.com/Corium-OS/Corium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The ISO is unattended. It deploys the image that it contains, with no prompts
and no kickstart to write.

If you changed the image and need your own disk, build a qcow2, a raw disk, or
an ISO from it. See [build from source](#build-from-source).

---

## 2. Write a node configuration

This is a complete single-node cluster:

```yaml
#cloud-config
corium:
  role: single

users:
  - name: core
    groups: [wheel]
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... you@example.com
```

`role` is the only required field. A node that gets its role over the API can
leave out even `role`. This lets a fleet share one cloud-config
([§3.2](reference.md#32-role)). All other fields have a default. The document
stays an ordinary cloud-config, so `write_files`, `runcmd`, and the other
modules continue to work.

If you have a checkout of the repository, validate the file before you boot
anything:

```bash
mise exec -- go run ./cmd/corium-agent validate node.yaml
```

Validation works offline and reports all problems at the same time. So you do
not find your mistakes one reboot at a time.

The four roles:

| Role | What it is |
|---|---|
| `single` | A self-contained cluster. Cannot gain nodes later |
| `controller` | Control plane only. Runs no workloads |
| `controller+worker` | Control plane that also accepts workloads |
| `worker` | Workloads only. Joins an existing cluster with a token |

See [examples](examples.md) for workers, add-ons, a custom CNI, and HA.

---

## 3. Boot it

### Proxmox

```bash
# on the Proxmox node
VMID=140 \
DISK_IMAGE=/path/to/disk.qcow2 \
CLOUD_CONFIG=/path/to/node.yaml \
IP_CONFIG="ip=192.168.0.190/24,gw=192.168.0.1" \
NAMESERVER=192.168.0.1 \
  bash deploy/proxmox/create-vm.sh

qm start 140
```

To install from the ISO, use `deploy/proxmox/create-vm-iso.sh`. It takes the
same variables, plus `ISO=local:iso/corium-install.iso`.

### Anywhere else

Boot the disk and give `node.yaml` to the platform as cloud-init user-data.
Corium probes NoCloud, ConfigDrive, OpenStack, EC2, Azure, GCE, Hetzner, VMware,
and OVF. So one image works on all of them.

For a NoCloud seed ISO:

```bash
printf 'instance-id: node-01\nlocal-hostname: node-01\n' > meta-data
cp node.yaml user-data
genisoimage -output seed.iso -volid cidata -joliet -rock user-data meta-data
```

### Without cloud-init

For a PC in front of you, install from the ISO with a NoCloud seed on a second
USB stick. The [bare metal](install/baremetal.md) page covers that from start to
end. For bare metal with no seed device, for PXE, or for a preconfigured
appliance, put the configuration where the agent can find it. The agent tries
the sources in this order, and the first source that answers wins.

| Source | For |
|---|---|
| `/etc/corium/config.yaml` | An operator's answer for this machine |
| cloud-init | Clouds and hypervisors |
| `corium.config=` on the kernel command line | PXE and netboot |
| `/usr/share/corium/config.yaml` | A default baked into a derived image |

Outside a cloud-config, write the schema alone, with no `corium:` wrapper:

```yaml
role: worker
cluster:
  name: edge
join:
  tokenFrom:
    url: https://secrets.example.com/corium/worker-token
```

---

## 4. Check it worked

```bash
ssh core@192.168.0.190

systemctl status corium-bootstrap    # what the agent did, and why if it failed
sudo k0s kubectl get nodes
sudo k0s kubectl get pods -A
```

A single node needs about one minute from power-on to `Ready`. The API answers
long before the node registers. So wait for `Ready`.

To use the cluster from your workstation:

```bash
ssh core@192.168.0.190 sudo k0s kubeconfig admin > kubeconfig
KUBECONFIG=./kubeconfig kubectl get nodes
```

k0s selects the address in that file, and it is the address of the node itself.
That address is correct for a single node. It is wrong for a cluster with a
virtual IP. In that case, the kubeconfig points at one controller, and it stops
working the first time that controller stops. Make sure that the `server:` field
is correct before you rely on the file. Step 5 removes this problem.

---

## 5. Manage it without SSH (optional)

All the steps above work over SSH. But SSH is a poor fit for an immutable OS,
because almost nothing that you type in the shell persists. Corium ships a
management API and a client called `cctl`. They cover the tasks that you
actually need, for example: read the status of a node, restart k0s, upgrade the
node, and get a kubeconfig.

The API is **on unless you turn it off**. A node with no `api:` block serves its
enrolment route until it bootstraps. Because of this, you can still reach a
machine that you installed from the ISO with no configuration. After the
bootstrap, a node with no operator CA has nobody to authenticate, so it binds no
port. The four steps above produced that state, one minute after they
finished.

Install `cctl` on your own machine. Each release includes archives for Linux and
macOS, on amd64 and arm64:

```bash
mise use -g 'github:Corium-OS/Corium[exe=cctl]@0.5.0'
```

You must keep the quotes and the version. See
[downloads](install/downloads.md#install-cctl) for the reason, and for how to
install `cctl` by hand.

Make the operator CA. Nodes trust its certificate. Its key stays on your
machine, and `cctl` never sends it anywhere:

```bash
cctl pki init
cctl pki issue --role admin
```

`pki init` prints the certificate in a form that you can paste. Add it to the
node configuration from step 2. It is a certificate, not a secret, so you can
put it in cloud-init in clear text:

```yaml
corium:
  role: single
  api:
    operatorCA: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
```

Provision the node again with that configuration. The node claims itself at
boot. Then, on your workstation, run:

```bash
cctl status 192.168.0.190          # role, image digest, k0s, greenboot, uptime
cctl logs 192.168.0.190 --unit k0scontroller --since 15m
cctl kubeconfig 192.168.0.190 > kubeconfig
```

On the first call, `cctl` asks you to compare the fingerprint of the node with
the fingerprint in the node journal. Then `cctl` remembers it. A node signs its
own certificate, so the fingerprint identifies the node, not its name. If the
cluster has a virtual IP, `cctl kubeconfig` points the file at it. This removes
the problem from step 4.

[The `cctl` page](cli.md) covers every command, the three roles, and the tasks
that the API refuses on purpose.

---

## Where to go next

- **Add workers**: [`examples/worker.yaml`](examples/worker.yaml). Create a token
  on the controller with `k0s token create --role=worker --expiry=1h`. Or, if a
  controller runs the API, let it create the token and print the full block with
  `cctl worker-config <controller> --name w-1`.
- **Highly available control plane**:
  [`examples/ha-controller-first.yaml`](examples/ha-controller-first.yaml), or
  `deploy/proxmox/create-ha-cluster.sh` to bootstrap all three at the same time.
  You need no load balancer and no certificates.
- **Add-ons**: declare Helm charts under `addons:`, and k0s installs them at
  bootstrap. You need no Helm binary and no in-cluster operator.
- **A different CNI**: [installing Cilium](cilium.md), from start to finish.
- **Anything Corium does not model**: Corium applies `k0s.patch` verbatim to the
  rendered `k0s.yaml`. So you can reach every k0s setting.
- **Every field in detail**: the [configuration reference](reference.md).
- **What is and is not supported**: [feature support](features.md). It includes
  everything that you can reach through the k0s passthrough.

### Upgrades

To upgrade the OS, you boot a new image. The [upgrades guide](upgrades.md)
shows how to do this on a cluster without loss of quorum. To change only the
Kubernetes version, see
[upgrading Kubernetes](upgrades.md#upgrading-kubernetes-specifically). This is
the short version of an OS upgrade:

```bash
sudo bootc upgrade --apply     # reboots into the new version
sudo bootc rollback            # if it went badly
```

If the node runs important workloads, drain it first.

---

## Troubleshooting

These failures cost the most time on this path.
[Troubleshooting](troubleshooting.md) lists every symptom for every install
path.

**The node boots and does nothing.** Read the journal with
`journalctl -u corium-bootstrap`. If you did step 5, use
`cctl logs <node> --unit corium-bootstrap`. A node with no Corium configuration
reports this and stops. That is a valid result: you get a host, not a Kubernetes
node.

**`cctl` says the node is not enrolled.** The node waits for an operator to
claim it. `api.enabled: true` with no CA is maintenance mode. In this mode, the
node holds its bootstrap until somebody runs `cctl enroll` with the pairing code
from its console. If you want the node to start without help, give it
`api.operatorCA` instead.

**The node is `NotReady` and stays there.** If you set `cni: custom`, this is
expected. No network is installed yet, and the cluster waits for you to install
one. If you did not set it, look at the CNI DaemonSet with
`kubectl -n kube-system describe pod`.

**Images do not pull, but everything else works.** The cause is almost always
DNS. A static address that you configure through a hypervisor usually has no
resolver. For example, Proxmox's `ipconfig0` has no field for one. So set
`NAMESERVER`. The symptom is misleading. The node answers ping, SSH works, and
the API answers. But Kubernetes hangs with `lookup quay.io: Try again`.

**Installing from the ISO loops forever.** The boot order must be **disk first,
ISO second**. An empty disk has no UEFI boot entry. So the firmware goes to the
ISO, which installs. After the install, the disk has an entry and boots first.
If the ISO is first, the node installs itself again on every reboot.

Do not stop the VM during an install. Anaconda erases the disk early. So an
interrupted install leaves nothing that can boot.

**Two nodes have the same name.** This must not happen. If the hostname is still
generic, Corium makes a stable name from the machine ID. If you cloned a disk
*after* first boot, the clone also has the same machine ID. Clear
`/etc/machine-id` on the clone, or set `node.name` explicitly.

---

## Build from source

All the steps above use a published image. You need this section only if you
changed the image and want a disk built from your own version.

To add your own agents, certificates, or drivers to a published image, you do
not need a source checkout. See [building your own image](derived-images.md).

This is the only part that needs a **Linux host** with `podman` and about 20 GB
of free disk. You also need [mise](https://mise.jdx.dev). It runs every command
below and installs the toolchain that they need. Run `mise install` once, in the
checkout.

You cannot build the disk images on macOS or Windows. The builder mounts the
root filesystem that it creates, to fill it. This needs a real Linux kernel. A
Linux VM works. The hypervisor that you deploy to also works, and it is often
the most convenient place.

```bash
podman --version     # 4.x or newer
nproc; free -g       # 2 cores and 4 GB are enough to build
```

The Go toolchain is **not** required. The container build compiles the agent.

### Build the OS image

```bash
mise run image
```

This produces an ordinary OCI image. You can inspect it like any other image:

```bash
podman run --rm localhost/corium:dev k0s version
podman run --rm localhost/corium:dev corium-agent version
```

The build ends with `bootc container lint`. If it reports warnings, read them.
They find real problems, for example content written to `/var` that will not
survive an upgrade.

To publish it:

```bash
REGISTRY=ghcr.io/you IMAGE_TAG=0.5.0-1 mise run push
```

### Turn it into a disk

Pick the artefact for the platform where the node will run:

| Command | Produces | Use it for |
|---|---|---|
| `mise run artefact-qcow2` | `output/qcow2/disk.qcow2` | Proxmox, KVM, libvirt |
| `mise run artefact-raw` | `output/image/disk.raw` | Bare metal, most cloud import paths |
| `mise run artefact-anaconda-iso` | `output/bootiso/install.iso` | Bare-metal installs |
| `mise run artefacts` | all three | |

Each command takes several minutes and needs `sudo`, because the builder runs
privileged. [image-builder](https://github.com/osbuild/image-builder) builds the
two disks. The `bootc-image-builder` entry point that image-builder still ships
builds the ISO, because it is the only one that can build that type. `mise.toml`
pins both by digest, and you can override them there. See
[ADR 9](adr/0009-image-builder.md).

Then go back to [step 2](#2-write-a-node-configuration) with the disk that you
built.
