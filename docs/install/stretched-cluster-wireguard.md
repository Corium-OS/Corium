# A stretched cluster with encrypted inter-node traffic

This page builds a cluster with nodes in two places, for example some on a cloud
and one at home. WireGuard encrypts the pod traffic between them on the wire.
The page builds the cluster on OpenStack (tested on OVH Public Cloud). It also
proves the encryption with tests, and does not only claim it.

In this example, the two "sites" are both OVH instances. Labels put them in two
*locations*, `ovh` and `home`, so you can see the layout without a real second
site. The method does not depend on that. You can replace the `home` node with a
machine anywhere that can reach the others over UDP, and the same configuration
works.

## The shape

| | |
|---|---|
| One controller | `role: controller`, location `ovh`. Runs no workloads |
| One worker | `role: worker`, location `ovh` |
| One worker | `role: worker`, location `home` |
| CNI | Calico, with node-to-node WireGuard encryption |
| Datastore | Embedded etcd, on the single controller |

The cluster has one controller, not three, on purpose. With a single
control-plane node, there is no etcd quorum to split across the WAN. If you grow
this to an HA control plane, keep all three controllers in **one** location.
Assume a quorum with two members on one side of a link and one member on the
other side. If the link fails, the quorum fails too. See
[the HA control plane](ha-cluster.md).

## Why Calico WireGuard, and not Kilo

Two tools can encrypt inter-node traffic here. This section explains the
choice.

[Kilo](https://kilo.squat.ai/) looks like the natural fit for this example,
because it models locations directly and can encrypt only the traffic between
locations. But at the "location" granularity, it elects one gateway per location
and sends cross-site traffic through that gateway. So each site has a hub.
Kilo's `full` mesh granularity is truly node-to-node. But then Kilo encrypts all
traffic, and the locations have no effect. The result is the same as with
Calico, but you need a `custom` CNI and manifests that you apply by hand.

Calico's own WireGuard support is a **direct node-to-node full mesh with no
hub**, and it fits Corium better. `network.cni: calico` is a modeled field. The
one switch that turns on encryption, `spec.network.calico.wireguard`, is
reachable verbatim through `k0s.patch`. You need no custom CNI, no add-on, and
no host WireGuard to manage. The kernel module ships in the Fedora base. Felix,
the Calico node agent, builds the `wireguard.cali` interface and creates its
keys.

So this page uses Calico WireGuard. Every workload path between nodes is
encrypted, and this includes the cross-site path. The locations stay as node
labels. You use them for placement and to read the output of
`kubectl get nodes`.

## Before you start

- An OpenStack project and the `openstack` client.
- The signed Corium qcow2 for your version. See [downloads](downloads.md).
- A flavor with 4 GB or more, and a disk of at least 10 GiB, the size of the
  image.
- A network that gives each instance an address that the others can reach. On
  OVH, `Ext-Net` gives a public address directly. The nodes use that address as
  their WireGuard endpoint.

## 1. Register the image (UEFI)

Corium boots only with UEFI. Verify the download before you trust it. First,
`cosign` shows who built it. Then `sha256sum` shows that you received the
correct bytes. [downloads](downloads.md) describes both. Then register the image
with the firmware property set:

```bash
openstack image create corium-0.5.0 \
  --file corium-0.5.0-x86_64.qcow2 --disk-format qcow2 --container-format bare \
  --property hw_firmware_type=uefi \
  --property hw_machine_type=q35 \
  --property hw_disk_bus=virtio \
  --property os_type=linux
```

`hw_firmware_type=uefi` is required. Without it, the instance boots a BIOS
firmware and never finds a bootloader. If your uplink is slow, download,
verify, and upload the image from a temporary instance in the same region, not
from your laptop. The bytes are the same, but the bandwidth is better.

## 2. Key and firewall

The security group must allow your own SSH and API access. It must also allow
the traffic that the nodes send **between themselves**. The encryption ports are
the two that people often forget:

| Port | Protocol | For |
|---|---|---|
| 51820 | UDP | WireGuard: the encrypted tunnel itself |
| 4789 | UDP | Calico VXLAN |
| 6443 | TCP | Kubernetes API |
| 9443 | TCP | k0s join API |
| 8132 / 8133 | TCP | konnectivity |
| 10250 | TCP | kubelet |
| 2379 / 2380 | TCP | etcd (between controllers) |

The simplest correct rule set opens SSH, ICMP, and the API to you. Then a rule
that points at the security group itself lets the members talk to each other:

```bash
openstack keypair create --public-key ~/.ssh/id_ed25519.pub corium-wg
openstack security group create corium-wg

# external: your access
openstack security group rule create --proto tcp --dst-port 22   --remote-ip 0.0.0.0/0 corium-wg
openstack security group rule create --proto tcp --dst-port 6443 --remote-ip 0.0.0.0/0 corium-wg
openstack security group rule create --proto icmp                --remote-ip 0.0.0.0/0 corium-wg

# internal: any member to any member, covering every port in the table above
openstack security group rule create --proto tcp  --remote-group corium-wg corium-wg
openstack security group rule create --proto udp  --remote-group corium-wg corium-wg
openstack security group rule create --proto icmp --remote-group corium-wg corium-wg
```

`--remote-group corium-wg` is the important part. It means "from any instance in
this group". So it allows WireGuard's UDP/51820 between the nodes, and you do
not have to list addresses that do not exist yet.

## 3. The controller

[`examples/stretched-controller-calico-wireguard.yaml`](../examples/stretched-controller-calico-wireguard.yaml)
is the full document. These are the important parts:

```yaml
corium:
  role: controller
  cluster:
    name: stretched
  network:
    cni: calico
  storage:
    type: etcd
  node:
    name: corium-wg-ctl
    labels:
      corium.dev/location: ovh
  k0s:
    patch:
      spec:
        network:
          calico:
            mode: vxlan
            wireguard: true      # <- turns on the encryption
            mtu: 1350            # <- must leave room for the WireGuard header
```

Boot it, and wait for k0s:

```bash
openstack server create corium-wg-ctl \
  --image corium-0.5.0 --flavor c3-4 \
  --key-name corium-wg --security-group corium-wg --network Ext-Net \
  --user-data stretched-controller-calico-wireguard.yaml --wait

ssh core@<controller-ip> sudo k0s status      # wait for "Role: controller"
```

A `role: controller` node runs no kubelet. So it does **not** appear in
`kubectl get nodes`. Calico's pods stay `Pending` until a worker joins, because
there is no node to schedule them on yet. Both results are expected. They are
not a failure.

Before any worker exists, you can make sure that the switch reached the
rendered configuration:

```console
$ ssh core@<controller-ip> sudo grep -A3 'calico:' /etc/k0s/k0s.yaml
        calico:
            mode: vxlan
            mtu: 1350
            wireguard: true
```

## 4. The workers

The CNI is a cluster-wide decision, and the controller already made it. So a
worker needs only its role, a name, a location label, and a join token. Create
the token on the controller:

```bash
ssh core@<controller-ip> sudo k0s token create --role=worker --expiry=2h
```

Now write one document for each worker:

1. Copy [`examples/stretched-worker.yaml`](../examples/stretched-worker.yaml) to
   `stretched-worker-ovh.yaml` and `stretched-worker-home.yaml`.
2. In each file, set `join.token` to the token that you created. An inline token
   is acceptable for a lab. In other cases, use `join.tokenFrom` with an HTTPS
   secret source.
3. Set the location label for each node: `ovh` in one file, `home` in the other.

> **Warning.** Treat these two files as secrets and do not commit them. The
> token is a live credential until it expires.

Then create both workers:

```bash
for loc in ovh home; do
  openstack server create corium-wg-w-$loc \
    --image corium-0.5.0 --flavor c3-4 \
    --key-name corium-wg --security-group corium-wg --network Ext-Net \
    --user-data stretched-worker-$loc.yaml --wait
done
```

The workers start as `NotReady`. They become `Ready` when the Calico DaemonSet
runs on them:

```console
$ ssh core@<controller-ip> sudo k0s kubectl get nodes -o wide -L corium.dev/location
NAME               STATUS   ROLES    VERSION       INTERNAL-IP     LOCATION
corium-wg-w-home   Ready    <none>   v1.36.4+k0s   57.130.75.118   home
corium-wg-w-ovh    Ready    <none>   v1.36.4+k0s   57.130.75.146   ovh
```

Each worker registers its own public address as its internal IP. The other nodes
reach it at that address. When WireGuard is up, that address is also the
endpoint of its tunnel.

## 5. Confirm the encryption is on

Calico enables WireGuard for each node and publishes the public key of each node
on its `Node` object. If the annotation is present, Felix created the keys for
the tunnel:

```console
$ ssh core@<controller-ip> sudo k0s kubectl get nodes \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.annotations.projectcalico\.org/WireguardPublicKey}{"\n"}{end}'
corium-wg-w-home  AVR4Qm9WDk1WASU+7TOTW/0rMQM78a4y20LP7EChTWc=
corium-wg-w-ovh   pR14xMuDCmEgr79raxLE12oMYVcZzwd2JKxDxMMVHX4=
```

The interface itself exists on each node, with the MTU that you set:

```console
$ ssh core@<worker-ip> sudo ip -d link show type wireguard
4: wireguard.cali: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1350 ...
    wireguard ...
```

### Work from your own machine

From this point, the commands run `kubectl` on your machine, not over SSH. Get a
kubeconfig once:

```bash
ssh core@<controller-ip> sudo k0s kubeconfig admin > stretched.kubeconfig
export KUBECONFIG=$PWD/stretched.kubeconfig
kubectl get nodes
```

k0s selected the address in that file. Before you rely on the file, make sure
that the `server:` field points to an address that you can reach.

The Corium image is minimal and ships no `wg` binary. To read the live tunnel
state (handshakes and byte counters) without a change to the read-only OS, run
`wg` from a pod. The pod must share the network namespace of the host and have
`NET_ADMIN`:

```bash
kubectl run wgtool --image=nicolaka/netshoot --overrides='
{"spec":{"hostNetwork":true,"nodeSelector":{"corium.dev/location":"home"},
"containers":[{"name":"wgtool","image":"nicolaka/netshoot","command":["sleep","3600"],
"securityContext":{"privileged":true,"capabilities":{"add":["NET_ADMIN"]}}}]}}'
kubectl exec wgtool -- sh -c 'apk add -q wireguard-tools; wg show'
```

## 6. Prove it, end to end

Put two pods in two different locations and make them talk. Save this as
`proof.yaml`:

```yaml
apiVersion: v1
kind: Pod
metadata: {name: web, labels: {app: web}}
spec:
  nodeSelector: {corium.dev/location: ovh}
  containers: [{name: nginx, image: nginx:1.27-alpine}]
---
apiVersion: v1
kind: Pod
metadata: {name: client}
spec:
  nodeSelector: {corium.dev/location: home}
  containers: [{name: net, image: nicolaka/netshoot, command: ["sleep","36000"]}]
```

Apply it and wait until both pods run:

```bash
kubectl apply -f proof.yaml
kubectl wait --for=condition=Ready pod/web pod/client --timeout=120s
```

**Connectivity**: the client on `home` reaches the web pod on `ovh`. Get the
address of the web pod from the cluster. Do not type it by hand:

```bash
WEB_IP=$(kubectl get pod web -o jsonpath='{.status.podIP}')
```

```console
$ kubectl exec client -- curl -s -o /dev/null -w '%{http_code}\n' "http://$WEB_IP"
200
$ kubectl exec client -- ping -c3 "$WEB_IP"
3 packets transmitted, 3 received, 0% packet loss
```

**The tunnel carried it**: on the `home` node, `wg show` names the address of
the `ovh` node as the peer endpoint. This shows a direct tunnel, not a relay.
It also shows a recent handshake and counters that are not zero:

```console
interface: wireguard.cali
  public key: AVR4Qm9WDk1WASU+7TOTW/0rMQM78a4y20LP7EChTWc=
  listening port: 51820
peer: pR14xMuDCmEgr79raxLE12oMYVcZzwd2JKxDxMMVHX4=
  endpoint: 57.130.75.146:51820
  allowed ips: 10.244.120.0/26
  latest handshake: 12 seconds ago
  transfer: 31.24 MiB received, 15.62 MiB sent
```

**Nothing is in the clear**: capture traffic on the underlay NIC while the pods
talk. Only WireGuard UDP/51820 goes between the two nodes. A filter for the
HTTP traffic of the pods (TCP/80) between the nodes captures nothing:

```console
$ kubectl exec wgtool -- tcpdump -nni enp3s0 -c4 'udp port 51820 and host 57.130.75.146'
IP 57.130.75.118.51820 > 57.130.75.146.51820: UDP, length 176
IP 57.130.75.146.51820 > 57.130.75.118.51820: UDP, length 704
...

$ kubectl exec wgtool -- timeout 8 tcpdump -nni enp3s0 'tcp port 80 and host 57.130.75.146'
0 packets captured
```

Note a limit: in this setup, WireGuard encrypts **workload** traffic, that is
pod-to-pod and pod-to-node. Some Kubernetes control traffic between nodes (the
kubelet API on TCP/10250) still goes over the underlay outside the tunnel. But
that traffic uses its own TLS, so it is not in clear text.

## Pitfalls

**MTU.** This is the most common problem. The 1500 B underlay carries the
Calico VXLAN header (50 B) and the WireGuard header (60 B). So the MTU for the
pods must be 1390 or less. This build sets `spec.network.calico.mtu: 1350` for
a margin. If you keep the default, large packets disappear while pings and
small requests work. This failure does not look like an MTU problem.

**Control-plane placement.** Keep the etcd quorum on one side of the WAN. With a
single controller, this is automatic. With three, put all three in one location.
If you split a three-member quorum two and one, the minority side stops working.
The majority side then stops if one more member fails.

**WireGuard ingress.** The whole setup depends on UDP/51820 between nodes. If a
security group opens only TCP, every node has its keys and every annotation is
present, but no handshake ever completes. Open UDP/51820 (and Calico's
UDP/4789) between members. The `--remote-group` rule in step 2 does this.

## When you are done

```bash
for n in corium-wg-ctl corium-wg-w-ovh corium-wg-w-home; do
  openstack server delete "$n" --wait
done
openstack image delete corium-0.5.0
openstack security group delete corium-wg
openstack keypair delete corium-wg
```

## What to read next

- [OpenStack](openstack.md): a single node on this same cloud
- [HA control plane](ha-cluster.md): three controllers and a virtual IP, and
  where to keep them relative to a WAN
- [Configuration](../reference.md): `network.cni`, and `k0s.patch` for
  everything that Calico exposes and Corium does not name
