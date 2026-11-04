# OpenStack

This page installs a node on an OpenStack cloud, from the published qcow2. We
tested it on OVH Public Cloud. Only the network name below is specific to that
provider.

Corium boots in UEFI mode only, so you must register the image as UEFI. Get
this one item correct before anything else.

## Before you start

- An OpenStack project and the `openstack` client.
- A flavor with a disk at least as large as the virtual size of the image,
  10 GiB. On first boot, the filesystem grows to the size of the flavor disk.
- A network that gives the instance an address that you can reach.
- The Corium qcow2, downloaded and checked. [Downloads](downloads.md) tells you
  where it is and how to check what you got.
- An SSH key pair. Step 2 registers the public key with the cloud, and step 3
  puts the same key in the configuration of the instance.

## 1. Register the image

```bash
openstack image create corium-0.5.0 \
  --file corium-0.5.0-x86_64.qcow2 --disk-format qcow2 --container-format bare \
  --property hw_firmware_type=uefi \
  --property hw_machine_type=q35 \
  --property hw_disk_bus=virtio \
  --property os_type=linux
```

`hw_firmware_type=uefi` is required. Without it, the instance boots with a
BIOS firmware and never finds a bootloader.

Some Glance installations can import the image directly from a URL. Then you do
not upload 1.5 GB from your laptop:

```bash
openstack image create corium-0.5.0 --import \
  --import-method web-download --uri https://<the URL from the release notes>
```

Before you use this, run `openstack image import-info`. It lists the import
methods that the cloud supports. OVH offers only `glance-download`, so on OVH
you upload the file from your machine.

## 2. Key and firewall

```bash
openstack keypair create --public-key ~/.ssh/id_ed25519.pub corium
openstack security group create corium
openstack security group rule create --proto tcp --dst-port 22 --remote-ip 0.0.0.0/0 corium
openstack security group rule create --proto tcp --dst-port 6443 --remote-ip 0.0.0.0/0 corium
```

The keypair is only a convenience. The `users:` block in the configuration
below creates the user. So the keypair and the `users:` block must contain the
same key.

## 3. Launch it

The instance reads its configuration from the user data, so write the user data
first. Save it as `corium.yaml` in the directory where you run the next command:

```yaml
#cloud-config
corium:
  role: single
  cluster:
    name: homelab
ssh_pwauth: false
```

Add the `users:` block from
[Proxmox step 3](proxmox.md#3-describe-the-node), with the key that you
registered in step 2. It is the same block, and OpenStack does not change it.
If you want to start from a file,
[`examples/single-node.yaml`](../examples/single-node.yaml) is a complete
document.

The `corium:` block comes through the OpenStack metadata service, not through a
config drive. cloud-init reads it from `169.254.169.254`, and Corium gets it
from cloud-init like any other source. You do not need to put anything into the
image.

```bash
openstack server create corium \
  --image corium-0.5.0 --flavor <a flavor with 4 GB or more> \
  --key-name corium --security-group corium \
  --network <your external network> \
  --user-data corium.yaml --wait
```

## 4. Check it

`--wait` returns when the instance is ACTIVE. Get its address from the cloud,
then log in as the user that the configuration created:

```bash
openstack server show corium -f value -c addresses
ssh core@<the address>
```

```console
$ sudo cloud-init query --format '{{ v1.platform }} / {{ v1.subplatform }}'
openstack / metadata (http://169.254.169.254)

$ sudo k0s kubectl get nodes -o wide
NAME         STATUS   ROLES           AGE    VERSION       INTERNAL-IP
corium-rc7   Ready    control-plane   7m2s   v1.36.4+k0s   57.130.75.237

$ df -h /sysroot
/dev/vda4   48G  3.3G   43G   8% /sysroot
```

SSH answered approximately forty seconds after the instance became ACTIVE. The
node became `Ready` a few minutes later.

Two results come from the cloud, not from Corium. First, the hostname is the
name that OpenStack gave the instance. The agent logs `keeping the hostname
already set` and does not change it. If you want to choose the name, set
`node.name`. Second, on the first boot, the filesystem grew from the 10 GiB of
the image to the size of the flavor disk, without a request.

## 5. When you are done

```bash
openstack server delete corium --wait
openstack image delete corium-0.5.0
openstack security group delete corium
openstack keypair delete corium
```

## What to read next

- [Downloads](downloads.md): download an artefact and check it
- [cctl](../cli.md): manage this node without SSH, with its state, its
  journals, its upgrades, and a kubeconfig
- [Configuration](../reference.md): every field of the `corium:` block
