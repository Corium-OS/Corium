# A WireGuard overlay between hosts

Give every node an address in one private range, so hosts that share no network
— different sites, different providers — can reach each other over an encrypted
link, and a cluster can run across them.

This is a *host* concern, separate from the CNI. k0s can encrypt pod-to-pod
traffic, but that assumes the hosts underneath already find each other. The
overlay is what lets them.

Declare it with the `wireguard:` field in the `corium:` block. Every field is
specified in the [configuration reference](reference.md#312-wireguard); this page
is the walkthrough.

**Use this page if** nodes must reach each other across sites or providers.
**Skip it if** your nodes already share a network — the overlay buys you nothing
and costs you an MTU.

---

## Before you start

- **An image newer than 0.2.0.** The `wireguard:` field and the
  `wireguard-tools` package both land in the next release; on 0.2.0 neither is
  present, and you need the escape hatch at the end of this page instead. From
  then on the tools ship present but inert until an interface is declared, and
  the kernel module is in-tree and loads on demand.
- **One reachable endpoint.** At least one node needs a UDP port other nodes can
  dial. Nodes entirely behind NAT can join, but they must dial out to one that
  is not.
- **A UDP port open** on that node — `51820` by convention.

---

## 1. Generate a key pair per node

Run this once per node, on your workstation:

```bash
wg genkey | tee node-a.key | wg pubkey > node-a.pub
chmod 600 node-a.key
```

You should get two files: a 44-character base64 private key, and its public key.
The public key is not a secret and goes in every peer's configuration. The
private key never leaves the node it belongs to.

> **Generate a distinct key per node.** One key reused across nodes — baked into
> a derived image, or shipped on one seed ISO used everywhere — is one identity
> for the whole fleet, and revoking it revokes all of them.

---

## 2. Declare the overlay

Each node describes its own address and lists the others as peers. A two-node
overlay, `10.10.0.1` dialling out to `10.10.0.2`:

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

`persistentKeepalive` matters only behind NAT, where it holds the path open; 25
seconds is the usual value.

`allowedIPs` is what gets routed to that peer, and two peers may not claim
overlapping ranges. A `/32` per peer is the explicit form; `10.10.0.0/24` on a
single hub peer works when everything else sits behind it.

### Keep the private key out of metadata

`privateKeyFrom` resolves the key at first boot from an HTTPS endpoint or a file,
the same way a join token does. An inline `privateKey` is accepted and is a
liability: it sits in cleartext wherever the cloud-config does — the seed ISO,
the provider's user-data, the metadata service every process on the node can
query.

Corium writes `/etc/wireguard/<name>.conf` mode `0600`, enables
`wg-quick@<name>`, and never echoes a private or preshared key in an error.

---

## 3. Make the overlay the cluster transport

If the cluster itself runs over the overlay, two more things have to be true.

**Set `nodeAddress: true`** on the interface whose address other nodes should
reach this node at:

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

Without it the kubelet registers whatever address sits on the physical NIC. The
node then looks healthy and is unreachable across the overlay for logs, exec,
port-forward and metrics. At most one interface may set `nodeAddress`; on a
dual-stack interface the first address listed is the one registered.

**Set `cluster.endpoint` to the overlay address.** That reaches the API server's
`externalAddress` and its certificate SANs, so joining nodes and clients dial the
overlay rather than the public NIC.

Corium brings the interface up before k0s and orders the k0s service to require
it, so a node whose overlay failed does not half-join a cluster on the wrong
address.

---

## 4. Verify

On the node:

```bash
sudo wg show
```

Expect a `peer:` block per peer with a recent `latest handshake`. A peer with no
handshake has never been reached — check the endpoint and the UDP port.

From the cluster, confirm the node registered the overlay address:

```bash
sudo k0s kubectl get nodes -o wide
```

The `INTERNAL-IP` column should show the `10.10.0.x` address, not the public one.
If it shows the public address, `nodeAddress: true` is missing or sits on the
wrong interface.

---

## Troubleshooting

**No handshake with a peer.** The endpoint is unreachable, or the UDP port is
closed. WireGuard is silent by design: it answers nothing to a peer it cannot
authenticate, so there is no error to read — only an absent handshake.

**A hostname endpoint that only resolves over the overlay.** If a peer's
`endpoint` is a name, and the DNS that resolves it is reachable only across the
tunnel, the interface cannot come up. Use an IP address for the endpoint that
bootstraps the overlay.

**The node registered the wrong address.** `nodeAddress: true` is missing. It
cannot be fixed by editing the node: `--node-ip` is settled at bootstrap, so the
node needs reprovisioning.

**Pods fail on large payloads while small ones work.** The overlay's encapsulation
costs MTU. Set `mtu` on the interface, and make sure the CNI's MTU sits below it.

**An edit to `/etc/wireguard/wg0.conf` survived an upgrade and you did not want
it to.** `/etc` is three-way merged by OSTree: a file you have edited by hand
stops tracking the image's version. That is the normal `/etc` contract.

---

## Validation

These are rejected before anything is brought up, with every problem reported at
once: a bad or duplicate interface name, an address with no prefix length, a port
or MTU out of range, a key that is not a 32-byte base64 value, more than one
`nodeAddress`, a peer with no public key or a duplicate one, an endpoint that is
not `host:port`, a missing or malformed `allowedIPs`, and two peers claiming
overlapping ranges.

Check a document before booting anything:

```bash
corium-agent validate node.yaml
```

---

## The escape hatch

For an overlay you would rather keep outside the `corium:` block entirely, the
cloud-init route still works on any node with a datasource: a `write_files`
config at `/etc/wireguard/wg0.conf` mode `0600`, and
`systemctl enable --now wg-quick@wg0`. Use `enable`, not `start`, or the
interface vanishes at the first reboot.

It buys you nothing the field does not do better, and it costs you the three
things the field owns: it never runs on a node with no cloud-init datasource, it
cannot make the kubelet register the overlay address, and it puts the private key
in metadata. [ADR 6](adr/0006-host-wireguard-overlay.md) covers the reasoning.

---

## Next

- [Stretched cluster](install/stretched-cluster-wireguard.md) — a cluster split
  across two sites, end to end
- [Configuration reference](reference.md#312-wireguard) — every `wireguard:` field
- [HA cluster](install/ha-cluster.md) — three controllers, if the overlay carries
  a control plane
