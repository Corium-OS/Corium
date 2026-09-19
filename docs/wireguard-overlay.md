# A WireGuard overlay between hosts

Corium has no native field for a host WireGuard interface. There is a
[proposal to add one](adr/0006-host-wireguard-overlay.md), but until it is
accepted — and for overlays that are not the cluster's own transport, even if it
is — you build the interface the way you build anything the `corium:` block does
not cover: with the escape hatches every node always has, `write_files` and
`runcmd`. This page is that recipe.

It is worth being clear about what this earns you and what it does not, because
the honest answer shapes when to use it. A WireGuard overlay gives every node an
address in one private range, so hosts that share no network — different sites,
different providers — can still reach each other over an encrypted link. That is
a *host* concern, separate from the CNI: k0s can already encrypt pod-to-pod
traffic, but that assumes the hosts underneath can find each other, which across
providers they cannot. The overlay is what lets them.

Three things this recipe cannot do cleanly, all covered below in full. It runs
only where cloud-init runs, so a node configured without a cloud-init datasource
— bare metal, PXE, an appliance reading `/etc/corium/config.yaml` — never
executes it at all (section 1). It puts the private key in cleartext in your
instance metadata (section 5). And it gives you no reliable way to make the
kubelet register the *overlay* address as the node's address (section 3). The
first rules the recipe out entirely off-cloud; the other two are the reason the
[native field is proposed](adr/0006-host-wireguard-overlay.md): if the overlay is
meant to be the cluster's own transport, read section 3 before you rely on it.

---

## 1. What you need before you start

**The module is in the image; the tools are not — you add them.** The kernel
carries `wireguard.ko` present-but-unloaded (it loads on demand the first time
`wg-quick` brings an interface up), but `wireguard-tools` is not in the base
image, so `wg`, `wg-quick` and the `wg-quick@.service` template are absent. You
cannot add them at boot — `/usr` is read-only and cloud-init's `packages:` is
disabled on this OS — so it is an image change: derive an image and install the
one package, following [building your own image](derived-images.md) for the
signing that lets nodes accept the upgrade.

```dockerfile
FROM ghcr.io/corium-os/corium:0.2.0

# wg, wg-quick, and the wg-quick@.service template the recipe enables.
RUN dnf install -y --setopt=install_weak_deps=False wireguard-tools \
    && dnf clean all \
    && rm -rf /var/cache/* /var/lib/dnf /var/log/dnf* /var/log/hawkey.log

RUN bootc container lint --fatal-warnings
```

Everything below assumes your nodes run an image with `wireguard-tools` present.
The [proposed native field](adr/0006-host-wireguard-overlay.md) would make Corium
ship the package by default, present-but-inert the way it ships `sshd`; until
that lands, this one line is yours to add.

**This recipe needs cloud-init.** `write_files` and `runcmd` are cloud-init
modules; they run only on a node that has a cloud-init datasource. On bare metal,
PXE, or an appliance that carries its `corium:` block in
`/etc/corium/config.yaml` or on the kernel command line, cloud-init may not run
at all, and then none of this executes. That is the case the
[native field](adr/0006-host-wireguard-overlay.md) is meant for and this recipe
is not: a `corium:` field is read from the whole configuration source chain,
where a `runcmd` is read from cloud-init alone. If your nodes are off-cloud, this
page will not help you — the field is what you want, and saying so on the issue
is how it gets built.

---

## 2. A plain host overlay

This is the clean case: an encrypted network between hosts, used for something
other than the cluster's own control-plane traffic — cross-host database
replication, a private admin network, a service that should never touch the
public internet. Ordering against k0s does not matter here, so the recipe is just
the interface.

```yaml
#cloud-config
#
# The WireGuard config carries this node's private key. Anything that can read
# this instance's metadata can read it. That is the cost of doing this in
# cloud-init rather than through a secret reference; see section 5.
write_files:
  - path: /etc/wireguard/wg0.conf
    permissions: '0600'
    owner: root:root
    content: |
      [Interface]
      Address = 10.10.0.2/24
      ListenPort = 51820
      PrivateKey = REPLACE_WITH_THIS_NODES_PRIVATE_KEY

      [Peer]
      PublicKey = REPLACE_WITH_PEER_PUBLIC_KEY
      Endpoint = gateway.example.com:51820
      AllowedIPs = 10.10.0.0/24
      # Needed when this node sits behind NAT and must keep the path open.
      PersistentKeepalive = 25

runcmd:
  - [ systemctl, enable, --now, wg-quick@wg0.service ]
```

`enable` rather than `start` is the point: `wg-quick@wg0.service` comes back on
every reboot, not just this boot. `/etc/wireguard` is the only place `wg-quick`
reads from, so the config has to live there; it is written `0600` because it
holds the key.

Confirm it came up with `wg show` on the node.

---

## 3. An overlay used as the cluster transport

If the cluster itself runs over the overlay — nodes joining across sites, the API
server reachable only on the private range — two more things have to be true, and
they are where this stops being a copy-paste job.

**The interface has to be up before k0s starts, on every boot.** On the first
boot cloud-init's `runcmd` runs during `cloud-final.service`, and
`corium-bootstrap.service` — which installs and starts k0s — is ordered after it,
so the overlay is already up. On later boots there is no such guarantee unless
you write it down: order the k0s unit after the interface. The unit is
`k0sworker.service` on a worker and `k0scontroller.service` on a controller, and
it is installed by `corium-agent` at runtime, but a drop-in placed here is picked
up when the unit appears:

```yaml
write_files:
  # k0scontroller.service on a controller or controller+worker node.
  - path: /etc/systemd/system/k0sworker.service.d/10-wireguard.conf
    permissions: '0644'
    content: |
      [Unit]
      After=wg-quick@wg0.service
      Requires=wg-quick@wg0.service
```

`Requires` and not just `After` so that a node whose overlay failed to come up
does not start k0s and half-join a cluster on the wrong address — the same
posture Corium takes for a RAID mount.

**The node has to register its overlay address, and here the escape hatch runs
out.** For a controller, point clients and joining nodes at the overlay by
setting the endpoint in the `corium:` block — this reaches the API server's
`externalAddress` and its certificate SANs, which is supported and enough for
what talks *to* the control plane:

```yaml
corium:
  role: controller+worker
  cluster:
    name: overlay
    endpoint: 10.10.0.1          # the overlay address, not the public one
```

What no Corium knob reaches is the kubelet's own node address — the `INTERNAL-IP`
in `kubectl get node -o wide`. Corium only pins it for an HA controller, and k0s
takes it as a command-line kubelet flag that neither the `k0s.patch` escape hatch
nor a worker profile can set. Left alone, the kubelet picks the address on the
interface holding the default route, which is your public NIC, not `wg0`. The
node then looks healthy and is unreachable from across the overlay for logs,
exec, port-forward and metrics — the failure this project wrote
`internal/bootstrap/nodeip.go` to prevent for the VRRP virtual IP.

Your options, in order of how much they cost:

- **Check, then decide.** Run `kubectl get node -o wide` after the node joins. If
  it registered the overlay address anyway — which happens when the overlay *is*
  the routed path — you are done.
- **Route the default through the tunnel.** Set `AllowedIPs = 0.0.0.0/0` (and let
  `wg-quick` manage the route) so the kubelet's auto-detection picks the overlay
  address. This sends *all* the node's traffic through WireGuard, which is often
  more than you wanted and worth doing deliberately.
- **Wait for the field.** Making the overlay address the registered node address,
  before k0s, without routing everything through it, is exactly what the
  [proposed `wireguard:` field](adr/0006-host-wireguard-overlay.md) exists to do,
  and it is the one part of this that a recipe cannot do well.

---

## 4. Reboots and re-runs

`corium-agent` bootstraps a node once and never again — the marker in
`/var/lib/corium` sees to that. The overlay is not the agent's; it is
`wg-quick@wg0.service`, enabled, and it comes up on its own on every boot. That
is why the recipe enables a service rather than running `wg` from `runcmd`: a
`runcmd` is a first-boot event, and an interface that only exists on first boot
is an interface that vanishes at the first reboot.

`/etc/wireguard/wg0.conf` lives in `/etc`, which OSTree three-way merges on
upgrade. An edit you make on the node by hand survives, but it also means a
change you ship in a later image will not reach a file the node has already
diverged from — the normal `/etc` contract.

---

## 5. The private key

The recipe above writes the private key into the cloud-config document, which
means it sits in cleartext wherever that document does: the seed ISO, the
provider's user-data, the metadata service every process on the node can query.
Corium keeps join tokens, VRRP passwords and operator CAs *out* of that place on
purpose, through secret references resolved at boot — and a WireGuard key is no
less a secret than any of them.

There is no way to close this gap from inside cloud-init alone, because the tool
that reads the key, `wg-quick`, reads it from a file and knows nothing of secret
stores. The nearest you can do by hand is fetch the key at boot from somewhere
authenticated and assemble the config before bringing the interface up — which is
reimplementing, less carefully, the secret handling the native field would give
you. If the key not being in metadata matters to you, that is a reason to want
[the field](adr/0006-host-wireguard-overlay.md) rather than this recipe.

At the very least: generate a distinct key per node, keep the metadata that
carries it as private as the platform allows, and rotate it if the metadata is
ever exposed.

---

## The mistakes, in the order people make them

**Reaching for a `runcmd` on an off-cloud node.** No cloud-init datasource means
no `write_files` and no `runcmd`, so on bare metal or PXE this recipe silently
does nothing. That is not a bug to work around; it is the boundary of the recipe.
Use the [native field](adr/0006-host-wireguard-overlay.md) there.

**`start` instead of `enable`.** The overlay works until the first reboot, then
silently does not, and the node drops off the network with nothing in the journal
pointing at why.

**Assuming the node registered the overlay address.** It usually did not.
Section 3 — check with `kubectl get node -o wide`, do not assume.

**The key in a shared image.** A private key baked into a derived image, or into
one seed ISO reused across nodes, is the same key on every node. Generate one per
node and deliver it as per-node metadata, not as part of the image.

**A hostname endpoint that only resolves over the overlay.** If a peer's
`Endpoint` is a name, and the DNS that resolves it is itself reachable only
across the tunnel, the interface cannot come up. Use an address for the endpoint
that bootstraps the overlay.

---

## When to prefer the native field

Use this recipe for an overlay that is not the cluster's transport, on a node
that has cloud-init anyway, or to try the idea out before the field lands. Prefer
the [proposed field](adr/0006-host-wireguard-overlay.md) — and say so on the
issue if you need it — when the overlay *is* the cluster transport, or when the
node has no cloud-init datasource. That is where the recipe's three sharp edges
are: nodes with no cloud-init never run it, the kubelet registers the wrong
address, and the private key sits in metadata. Those three are what the field is
designed to own, and what a recipe cannot.
