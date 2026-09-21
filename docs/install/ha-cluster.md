# A highly available control plane

Three controllers, each a full member: they run etcd between them and share a
virtual IP that moves if one goes away. Clients talk to the virtual IP and never
know which machine answered.

This page builds one by hand, because the interesting part is not the commands
— it is why they happen in that order. There is a script at the end that does
all of it on Proxmox, and it will make more sense once you have read this.

## Before you start

- Three machines on one broadcast domain, each booted from the Corium image
  with one of the configurations below as its cloud-init user data — three VMs,
  three cloud instances, or three physical machines. [Proxmox](proxmox.md) and
  [OpenStack](openstack.md) each walk through creating one; the script at the
  end of this page creates all three.
- Five addresses on that segment: one per machine, and a spare for the virtual
  IP. The four decisions in the next section are about those.
- The Corium qcow2 or ISO for whichever platform you use — see
  [downloads](downloads.md).
- SSH to all three machines from wherever you run step 4, as the user in the
  `users:` block.
- `cctl`, for the last section only.
  [Downloads](downloads.md#installing-cctl) installs it.

## The shape

| | |
|---|---|
| Three controllers | `role: controller+worker`, or `controller` to keep workloads off them |
| etcd | `storage.type: etcd` — the default for controllers, and required for more than one |
| A virtual IP | Held by whichever controller currently wins a VRRP election |
| `cluster.endpoint` | The virtual IP, on every node |

Three is the smallest number that survives losing one: etcd needs a majority,
and two members have no majority without both.

## Decide four things first

**An address for each controller, and a fifth for the virtual IP.** All on the
same broadcast domain — VRRP is how the controllers agree on who holds it, and
it does not route.

**A virtual router ID**, 1–255, unique on that segment. Two clusters sharing an
ID will fight over each other's addresses. Omit it and k0s starts at 51, which
is fine until the day it is not.

**A VRRP password of at most eight characters.** keepalived silently truncates
anything longer, which would leave two controllers believing they share a
password they do not. Corium rejects a longer value rather than allow that.

**Whether your network carries multicast.** Most clouds do not. Without it, list
the other controllers under `unicastPeers`. On a flat LAN it is harmless to set
anyway.

## The problem this has to solve

A k0s cluster has no pre-shared secret. The first controller generates the
cluster certificate authority, and only once that exists can a join token be
minted. So the second and third controllers cannot be given a token in advance
— it does not exist when they boot.

Corium's answer is that they wait. Configure them with `join.tokenFrom.waitFor`
and they boot, find no token, and sit there until one appears. All three
machines can start at the same time; the token only has to arrive before the
timeout.

Nothing else is shared. No certificates, no CA, no key material in any
configuration file — k0s hands the CA to a joining controller over its join API
on port 9443, once the token has proved it should.

## 1. The first controller

```yaml
#cloud-config
corium:
  role: controller+worker
  cluster:
    name: homelab
    endpoint: 192.168.0.200        # the virtual IP, not this node's address
  ha:
    enabled: true
    virtualIP: 192.168.0.200/24    # with a prefix: keepalived needs it
    virtualRouterID: 51
    authPass: s3cr3t42             # eight characters or fewer
    unicastPeers:
      - 192.168.0.202
      - 192.168.0.203
  storage:
    type: etcd
```

Add the `users:` block from
[Proxmox step 3](proxmox.md#3-describe-the-node) — it is the same one, and
without it the machine boots with no account to SSH into.
[`examples/ha-controller-first.yaml`](../examples/ha-controller-first.yaml) is
the whole document in one commented file.

`endpoint` is the virtual IP because it ends up in the API server's certificate.
Point it at one controller's own address and the certificate stops matching the
moment the address moves.

## 2. The other two

The same file, plus a `join:` block, and `unicastPeers` listing the *other* two:

```yaml
  join:
    tokenFrom:
      file: /etc/corium/join-token
      waitFor: 20m
```

`waitFor` is what lets them boot before the token exists. Without it a missing
token is a hard failure and the node stops.
[`examples/ha-controller-join.yaml`](../examples/ha-controller-join.yaml) shows
the whole document.

## 3. Create them and start all three

Create each machine with its configuration as user data, then start it —
`qm start`, `openstack server create`, powering on hardware. Order does not
matter. The first controller forms the cluster; the other two wait.

## 4. Mint a token and deliver it

> **Warning.** An unexpired controller token is a cluster-admin credential.
> Whoever holds one can join a full control-plane member with read and write
> access to etcd. Use a short expiry, and prefer a secret store over instance
> metadata — `tokenFrom` also accepts a `url`, which is the better shape in
> production.

Once the first controller is answering, mint the token and keep it:

```bash
ssh core@192.168.0.201 sudo k0s status          # wait for this to succeed
TOKEN=$(ssh core@192.168.0.201 sudo k0s token create --role=controller --expiry=1h)
```

Then write it to `/etc/corium/join-token` on each of the other two, mode
`0600`:

```bash
for ip in 192.168.0.202 192.168.0.203; do
  ssh "core@${ip}" "sudo install -m600 /dev/stdin /etc/corium/join-token" <<< "$TOKEN"
done
```

Check `$TOKEN` is not empty before you ship it. An empty file is not an error
to a waiting controller: it keeps waiting for the whole of `waitFor` and says
nothing that points at the cause.

Each waiting controller notices the file, joins, and k0s ships it the CA.

## 5. Check it

```console
$ sudo k0s kubectl get nodes -o wide
NAME              STATUS   ROLES           VERSION       INTERNAL-IP
corium-7c17d087   Ready    control-plane   v1.36.4+k0s   192.168.0.201
corium-87025d9c   Ready    control-plane   v1.36.4+k0s   192.168.0.202
corium-5c5d785f   Ready    control-plane   v1.36.4+k0s   192.168.0.203

$ sudo k0s etcd member-list
{"members":{"corium-5c5d785f":"https://192.168.0.203:2380", ...}}
```

Each node registers **its own address**, not the virtual IP. That distinction is
the difference between a cluster that works and one that works until the first
failover.

The API answers on the virtual IP, which is what a kubeconfig should point at:

```console
$ curl -sk -o /dev/null -w '%{http_code}\n' https://192.168.0.200:6443/readyz
401
```

401 is the right answer: the server is there and declining an unauthenticated
request.

## 6. Watch it fail over

The address alone will not tell you which controller holds the virtual IP. ARP
will:

```bash
ip neigh flush dev <your bridge>
ping -c2 192.168.0.200 >/dev/null
ip neigh show 192.168.0.200          # compare the MAC against each node's
```

Stop that controller and ask again. On a real run the virtual IP moved within
five seconds, the API kept answering, etcd held quorum at two of three, and the
cluster carried on scheduling. Starting the controller again brought it back
`Ready` inside fifteen seconds.

## Doing all of that on Proxmox

[`deploy/proxmox/create-ha-cluster.sh`](https://github.com/Corium-OS/Corium/tree/main/deploy/proxmox)
performs every step above: it writes the three configurations, generates a VRRP
password at exactly eight characters, creates and starts the VMs, waits for the
first controller, mints a token and delivers it to the other two. Get it onto
the host the same way [Proxmox](proxmox.md#before-you-start) does, and run it
from `deploy/proxmox/`.

> **Warning.** Two things to know before you run it. `SSH_KEY` must be a key
> **this Proxmox host** holds the private half of, because the script SSHes
> from here to deliver the token — a key from your laptop fails at
> `Permission denied` with three VMs already built. And unlike `create-vm.sh`,
> it **destroys** any VM already holding one of those VMIDs.

```bash
DISK_IMAGE=/var/lib/vz/template/corium-0.3.3-x86_64.qcow2 \
SSH_KEY="$(cat ~/.ssh/id_ed25519.pub)" \
CLUSTER_NAME=homelab VIP=192.168.0.200 GATEWAY=192.168.0.1 \
NODE_IPS="192.168.0.201 192.168.0.202 192.168.0.203" \
VMIDS="142 143 144" \
  ./create-ha-cluster.sh
```

## When you are done

```bash
for id in 142 143 144; do qm stop "$id"; qm destroy "$id" --purge; done
rm -f /var/lib/vz/snippets/corium-14*.yaml
for ip in 192.168.0.201 192.168.0.202 192.168.0.203; do
  ssh-keygen -f /root/.ssh/known_hosts -R "$ip"
done
```

`qm destroy` leaves the snippets behind, and the host keeps the controllers' old
host keys — rebuilding onto the same addresses otherwise greets you with
`REMOTE HOST IDENTIFICATION HAS CHANGED`.

## Managing it afterwards

Two things about an HA cluster are easier with the [management API](../cli.md)
than by hand, and both are about the virtual IP.

A kubeconfig aimed at one particular controller stops working the first time
that controller does — which on a cluster built for exactly that eventuality is
an odd way to end up. `cctl kubeconfig <any controller>` points the file at the
virtual IP, because that is what the node recorded when it bootstrapped:

```bash
cctl kubeconfig 192.168.0.201 > kubeconfig
grep server: kubeconfig          # https://192.168.0.200:6443
```

And rolling three controllers is the case where going one at a time matters
most, since two of them are the quorum:

```bash
cctl upgrade 192.168.0.201 192.168.0.202 192.168.0.203 --image ghcr.io/corium-os/corium:<tag>
```

It stops at the first controller that does not come back, rather than taking
the second one down after it.

Both need `api.operatorCA` in each controller's configuration; the API is off
unless asked for.

---

## What to read next

- [cctl](../cli.md) — every command, the three roles, and what the API will not
  do
- [Configuration](../reference.md) — every field of `ha:` and `join:`
- [Upgrades](../upgrades.md) — moving a cluster to a new image without losing
  quorum
