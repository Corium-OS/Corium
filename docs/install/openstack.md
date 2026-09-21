# OpenStack

A node on an OpenStack cloud, from the published qcow2. Verified on OVH Public
Cloud; nothing below is specific to that provider beyond the network name.

Corium boots UEFI only, so the image has to be registered as such. That is the
one thing worth getting right before anything else.

## Before you start

- An OpenStack project and the `openstack` client.
- A flavor whose disk is at least the image's virtual size — 10 GiB. The
  filesystem grows to the flavor's disk on first boot.
- A network that gives the instance an address you can reach.
- The Corium qcow2, downloaded and checked. [Downloads](downloads.md) has
  where it lives and how to check what you got.
- An SSH key pair. Step 2 registers the public half with the cloud, and step 3
  puts the same key in the instance's configuration.

## 1. Register the image

```bash
openstack image create corium-0.3.3 \
  --file corium-0.3.3-x86_64.qcow2 --disk-format qcow2 --container-format bare \
  --property hw_firmware_type=uefi \
  --property hw_machine_type=q35 \
  --property hw_disk_bus=virtio \
  --property os_type=linux
```

`hw_firmware_type=uefi` is not optional. Without it the instance is booted with
a BIOS firmware and never finds a bootloader.

Glance can sometimes import straight from a URL, which saves uploading a
gigabyte and a half from your laptop:

```bash
openstack image create corium-0.3.3 --import \
  --import-method web-download --uri https://<the URL from the release notes>
```

Check first — `openstack image import-info` lists what the cloud supports. OVH
offers only `glance-download`, so there the file goes up from your machine.

## 2. Key and firewall

```bash
openstack keypair create --public-key ~/.ssh/id_ed25519.pub corium
openstack security group create corium
openstack security group rule create --proto tcp --dst-port 22 --remote-ip 0.0.0.0/0 corium
openstack security group rule create --proto tcp --dst-port 6443 --remote-ip 0.0.0.0/0 corium
```

The keypair is convenience only. What actually creates the user is the
`users:` block in the configuration below, so the two should carry the same
key.

## 3. Launch it

The instance reads its configuration from the user data, so write that first.
Save it as `corium.yaml` in the directory you run the next command from:

```yaml
#cloud-config
corium:
  role: single
  cluster:
    name: homelab
ssh_pwauth: false
```

Add the `users:` block from
[Proxmox step 3](proxmox.md#3-describe-the-node), carrying the key you
registered in step 2. It is the same block; OpenStack changes nothing about
it. [`examples/single-node.yaml`](../examples/single-node.yaml) is a complete
document if you would rather start from a file.

**The `corium:` block arrives through the OpenStack metadata service**, not
through a config drive. cloud-init reads it from `169.254.169.254`, and Corium
picks it up from cloud-init like any other source. Nothing needs to be baked
into the image.

```bash
openstack server create corium \
  --image corium-0.3.3 --flavor <a flavor with 4 GB or more> \
  --key-name corium --security-group corium \
  --network <your external network> \
  --user-data corium.yaml --wait
```

## 4. Check it

`--wait` returns once the instance is ACTIVE. Ask the cloud for its address,
then log in as the user the configuration created:

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

SSH answered about forty seconds after the instance went ACTIVE, and the node
reached `Ready` a few minutes later.

Two things that come from the cloud rather than from Corium. The hostname is
whatever OpenStack gave the instance — the agent logs `keeping the hostname
already set` and leaves it alone, so set `node.name` if you want to choose.
And the filesystem grew from the image's 10 GiB to the flavor's disk on the
first boot, without being asked.

## 5. When you are done

```bash
openstack server delete corium --wait
openstack image delete corium-0.3.3
openstack security group delete corium
openstack keypair delete corium
```

## What to read next

- [Downloads](downloads.md) — fetching an artefact and checking it
- [cctl](../cli.md) — managing this node without SSH: its state, its journals,
  its upgrades, and a kubeconfig
- [Configuration](../reference.md) — every field of the `corium:` block
