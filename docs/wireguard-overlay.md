# A WireGuard overlay between hosts

An overlay gives every node an address in one private range. With it, hosts
that share no network can reach each other over an encrypted link, for example
hosts at different sites or with different providers. A cluster can then run
across them.

The overlay is a *host* function, separate from the CNI. k0s can encrypt
traffic between pods, but only if the hosts can already reach each other. The
overlay lets them do that.

Declare the overlay with the `wireguard:` field in the `corium:` block. The
[configuration reference](reference.md#312-wireguard) specifies every field.
This page is the walkthrough.

Use this page if nodes must reach each other across sites or providers. If
your nodes already share a network, skip this page. The overlay gives you no
benefit, and it uses part of the MTU.

---

## Before you start

- An image of 0.3.0 or newer. The `wireguard:` field and the `wireguard-tools`
  package both arrived in 0.3.0. On 0.2.0, neither one is present, and you must
  use the escape hatch at the end of this page instead. From 0.3.0, the image
  ships the tools, but they do nothing until you declare an interface. The
  kernel module is in-tree and loads when it is needed.
- One reachable endpoint. At least one node needs a UDP port that the other
  nodes can connect to. Nodes that are fully behind NAT can join, but they must
  connect out to a node that is not.
- An open UDP port on that node. By convention, the port is `51820`.

---

## 1. Generate a key pair per node

On your workstation, run this once for each node:

```bash
wg genkey | tee node-a.key | wg pubkey > node-a.pub
chmod 600 node-a.key
```

You get two files: a private key of 44 base64 characters, and its public key.
The public key is not a secret, and it goes in the configuration of every peer.
The private key never leaves the node that it belongs to.

> Generate a different key for each node. If you use one key on many nodes, for
> example in a derived image or on one seed ISO for all nodes, the whole fleet
> has one identity. If you revoke that key, you revoke all of the nodes.

---

## 2. Declare the overlay

Each node describes its own address and lists the other nodes as peers. In this
two-node overlay, `10.10.0.1` connects out to `10.10.0.2`:

```yaml
#cloud-config
corium:
  role: controller+worker
  wireguard:
    - name: wg0
      address: 10.10.0.1/24
      listenPort: 51820
      privateKeyFrom:
        url: https://secrets.example.com/corium/node-a-wg0.key
      peers:
        - publicKey: "NODE_B_PUBLIC_KEY_BASE64"
          endpoint: node-b.example.com:51820
          allowedIPs: [10.10.0.2/32]
          persistentKeepalive: 25
```

`persistentKeepalive` is necessary only behind NAT, where it keeps the path
open. The usual value is 25 seconds.

`allowedIPs` is the range that WireGuard routes to that peer. Two peers must not
claim overlapping ranges. A `/32` for each peer is the explicit form. If all
other nodes are behind a single hub peer, `10.10.0.0/24` on that hub also
works.

### Keep the private key out of metadata

`privateKeyFrom` gets the key at first boot from an HTTPS endpoint or a file,
the same way as a join token. Corium accepts an inline `privateKey`, but it is a
risk. The key is then in cleartext everywhere the cloud-config is: on the seed
ISO, in the user-data of the provider, and in the metadata service that every
process on the node can query.

Corium writes `/etc/wireguard/<name>.conf` with mode `0600` and enables
`wg-quick@<name>`. It never shows a private or preshared key in an error.

---

## 3. Make the overlay the cluster transport

If the cluster itself runs over the overlay, two more conditions must be true.

Set `nodeAddress: true` on the interface whose address the other nodes must use
to reach this node:

```yaml
corium:
  role: controller+worker
  cluster:
    endpoint: 10.10.0.1          # the overlay address, not the public one
  wireguard:
    - name: wg0
      address: 10.10.0.1/24
      nodeAddress: true
      # ... listenPort, privateKeyFrom, peers as above
```

Without it, the kubelet registers the address of the physical NIC. The node
then looks healthy, but logs, exec, port-forward and metrics cannot reach it
across the overlay. Only one interface can set `nodeAddress`. On a dual-stack
interface, the kubelet registers the first address in the list.

Set `cluster.endpoint` to the overlay address. Corium uses it for the
`externalAddress` of the API server and for its certificate SANs. Joining nodes
and clients then connect over the overlay, not over the public NIC.

Corium starts the interface before k0s, and the k0s service requires the
interface. So if the overlay fails, the node does not join a cluster halfway on
the wrong address.

---

## 4. Verify

On the node, run:

```bash
sudo wg show
```

You must see one `peer:` block for each peer, with a recent `latest handshake`.
If a peer has no handshake, the node never reached it. Make sure that the
endpoint and the UDP port are correct.

From the cluster, make sure that the node registered the overlay address:

```bash
sudo k0s kubectl get nodes -o wide
```

The `INTERNAL-IP` column must show the `10.10.0.x` address, not the public one.
If it shows the public address, `nodeAddress: true` is missing or is on the
wrong interface.

---

## Troubleshooting

No handshake with a peer. The endpoint is unreachable, or the UDP port is
closed. WireGuard gives no error in this case. It does not answer a peer that it
cannot authenticate, so the only sign is a missing handshake.

A hostname endpoint that resolves only over the overlay. If the `endpoint` of a
peer is a name, and the DNS server for that name is reachable only through the
tunnel, the interface cannot come up. For the endpoint that starts the overlay,
use an IP address.

The node registered the wrong address. `nodeAddress: true` is missing. You
cannot correct this with an edit on the node. Corium sets `--node-ip` at
bootstrap, so you must reprovision the node.

Pods fail on large payloads, but small payloads work. The encapsulation of the
overlay uses part of the MTU. Set `mtu` on the interface, and make sure that the
MTU of the CNI is lower.

An edit to `/etc/wireguard/wg0.conf` stayed after an upgrade, and you did not
want it to. OSTree does a three-way merge on `/etc`. If you edit a file by hand,
it stops following the version in the image. This is the normal behavior of
`/etc`.

---

## Validation

Corium rejects these problems before it starts anything, and it reports all of
them at the same time:

- A bad or duplicate interface name
- An address with no prefix length
- A port or MTU out of range
- A key that is not a 32-byte base64 value
- More than one `nodeAddress`
- A peer with no public key, or with a duplicate one
- An endpoint that is not `host:port`
- A missing or malformed `allowedIPs`
- Two peers that claim overlapping ranges

Before you boot anything, validate the document:

```bash
corium-agent validate node.yaml
```

---

## The escape hatch

If you want to keep the overlay fully outside the `corium:` block, the
cloud-init method still works on any node with a datasource. Add a
`write_files` entry for `/etc/wireguard/wg0.conf` with mode `0600`, and run
`systemctl enable --now wg-quick@wg0`. Use `enable`, not `start`. With `start`,
the interface disappears at the first reboot.

This method gives you nothing that the field does not do better. It also loses
three functions that only the field has. It never runs on a node with no
cloud-init datasource. It cannot make the kubelet register the overlay address.
And it puts the private key in metadata.
[ADR 6](adr/0006-host-wireguard-overlay.md) gives the reasons.

---

## Next

- [Stretched cluster](install/stretched-cluster-wireguard.md): a cluster split
  across two sites, from start to finish
- [Configuration reference](reference.md#312-wireguard): every `wireguard:` field
- [HA cluster](install/ha-cluster.md): three controllers, if the overlay carries
  a control plane
