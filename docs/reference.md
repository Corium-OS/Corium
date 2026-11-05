# Configuration reference

This page describes the configuration file: what it contains, where it comes
from, and what Corium does with it. This page follows `main`. The current stable
release is 0.5.0. A field that is newer than the release you run shows the
version that added it. The
[changelog](https://github.com/Corium-OS/Corium/blob/main/CHANGELOG.md) lists
these versions.

To find one field, use the [field index](#field-index) below. For a complete
working document, see [examples](examples.md). For a step-by-step guide, start
with the [quick start](quickstart.md). To learn which features Corium models and
which it passes to k0s unchanged, see [feature support](features.md).

Each description of k0s behavior on this page links to the
[official k0s documentation](https://docs.k0sproject.io/). Corium configures
upstream k0s and does not fork or patch it. If the two documents disagree, k0s is
correct and this page is out of date.

---

## Field index

This index lists every field in the schema, grouped by the block that contains
it. A sub-field of a list entry has `[]` in its name, as in an index expression.

### Root

| Field | Section |
|---|---|
| `role` | [§3.2 `role`](#32-role) |

### `cluster`

| Field | Section |
|---|---|
| `cluster.name` | [§3.3 `cluster`](#33-cluster) |
| `cluster.endpoint` | [§3.3 `cluster`](#33-cluster) |
| `cluster.subjectAltNames` | [§3.3 `cluster`](#33-cluster) |

### `kubernetes`

| Field | Section |
|---|---|
| `kubernetes.version` | [§3.20 `kubernetes`](#320-kubernetes) |
| `kubernetes.mirror` | [§3.20 `kubernetes`](#320-kubernetes) |

### `network`

| Field | Section |
|---|---|
| `network.podCIDR` | [§3.4 `network`](#34-network) |
| `network.serviceCIDR` | [§3.4 `network`](#34-network) |
| `network.cni` | [§3.4 `network`](#34-network) |

### `storage`

| Field | Section |
|---|---|
| `storage.type` | [§3.5 `storage`](#35-storage) |

### `join`

| Field | Section |
|---|---|
| `join.token` | [§3.6 `join`](#36-join) |
| `join.tokenFrom` | [§3.6 `join`](#36-join), [§3.16 Secret sources](#316-secret-sources) |

### `node`

| Field | Section |
|---|---|
| `node.name` | [§3.7 `node`](#37-node), [§4 Hostname](#4-how-the-hostname-is-settled) |
| `node.labels` | [§3.7 `node`](#37-node) |
| `node.taints` | [§3.7 `node`](#37-node) |
| `node.taints[].key` | [§3.7 `node`](#37-node) |
| `node.taints[].value` | [§3.7 `node`](#37-node) |
| `node.taints[].effect` | [§3.7 `node`](#37-node) |

### `addons`

| Field | Section |
|---|---|
| `addons[].name` | [§3.8 `addons`](#38-addons) |
| `addons[].chart` | [§3.8 `addons`](#38-addons) |
| `addons[].version` | [§3.8 `addons`](#38-addons) |
| `addons[].namespace` | [§3.8 `addons`](#38-addons) |
| `addons[].repository.name` | [§3.8 `addons`](#38-addons) |
| `addons[].repository.url` | [§3.8 `addons`](#38-addons) |
| `addons[].values` | [§3.8 `addons`](#38-addons) |

### `manifests`

| Field | Section |
|---|---|
| `manifests[].name` | [§3.19 `manifests`](#319-manifests) |
| `manifests[].files[].name` | [§3.19 `manifests`](#319-manifests) |
| `manifests[].files[].content` | [§3.19 `manifests`](#319-manifests) |

### `ha`

| Field | Section |
|---|---|
| `ha.enabled` | [§3.9 `ha`](#39-ha) |
| `ha.virtualIP` | [§3.9 `ha`](#39-ha) |
| `ha.interface` | [§3.9 `ha`](#39-ha) |
| `ha.virtualRouterID` | [§3.9 `ha`](#39-ha) |
| `ha.authPass` | [§3.9 `ha`](#39-ha) |
| `ha.authPassFrom` | [§3.9 `ha`](#39-ha), [§3.16 Secret sources](#316-secret-sources) |
| `ha.unicastPeers` | [§3.9 `ha`](#39-ha) |

### `upgrades`

| Field | Section |
|---|---|
| `upgrades.automatic` | [§3.10 `upgrades`](#310-upgrades) |
| `upgrades.schedule` | [§3.10 `upgrades`](#310-upgrades) |

### `backup`

| Field | Section |
|---|---|
| `backup.enabled` | [§3.18 `backup`](#318-backup) |
| `backup.schedule` | [§3.18 `backup`](#318-backup) |
| `backup.path` | [§3.18 `backup`](#318-backup) |
| `backup.keep` | [§3.18 `backup`](#318-backup) |

### `raid`

| Field | Section |
|---|---|
| `raid[].name` | [§3.11 `raid`](#311-raid) |
| `raid[].level` | [§3.11 `raid`](#311-raid) |
| `raid[].devices` | [§3.11 `raid`](#311-raid) |
| `raid[].spares` | [§3.11 `raid`](#311-raid) |
| `raid[].filesystem` | [§3.11 `raid`](#311-raid) |
| `raid[].mountPoint` | [§3.11 `raid`](#311-raid) |
| `raid[].wipe` | [§3.11 `raid`](#311-raid) |

### `zfs`

| Field | Section |
|---|---|
| `zfs[].name` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].vdevs[].type` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].vdevs[].devices` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].mountPoint` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].options` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].filesystemOptions` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].datasets[].name` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].datasets[].mountPoint` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].datasets[].properties` | [§3.17 `zfs`](#317-zfs) |
| `zfs[].wipe` | [§3.17 `zfs`](#317-zfs) |

### `wireguard`

| Field | Section |
|---|---|
| `wireguard[].name` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].address` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].listenPort` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].mtu` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].nodeAddress` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].privateKey` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].privateKeyFrom` | [§3.12 `wireguard`](#312-wireguard), [§3.16 Secret sources](#316-secret-sources) |
| `wireguard[].peers[].publicKey` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].peers[].endpoint` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].peers[].allowedIPs` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].peers[].persistentKeepalive` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].peers[].presharedKey` | [§3.12 `wireguard`](#312-wireguard) |
| `wireguard[].peers[].presharedKeyFrom` | [§3.12 `wireguard`](#312-wireguard), [§3.16 Secret sources](#316-secret-sources) |

### `api`

| Field | Section |
|---|---|
| `api.enabled` | [§3.13 `api`](#313-api) |
| `api.operatorCA` | [§3.13 `api`](#313-api) |
| `api.operatorCAFrom` | [§3.13 `api`](#313-api), [§3.16 Secret sources](#316-secret-sources) |
| `api.insecure` | [§3.13 `api`](#313-api), [`insecure`](#insecure) |
| `api.awaitConfig` | [§3.13 `api`](#313-api) |

### `k0s`

| Field | Section |
|---|---|
| `k0s.patch` | [§3.15 `k0s.patch`](#315-k0spatch--the-escape-hatch) |

### Secret sources

Each `…From` block accepts these four keys.

| Field | Section |
|---|---|
| `…From.url` | [§3.16 Secret sources](#316-secret-sources) |
| `…From.file` | [§3.16 Secret sources](#316-secret-sources) |
| `…From.authFile` | [§3.16 Secret sources](#316-secret-sources) |
| `…From.waitFor` | [§3.16 Secret sources](#316-secret-sources), [Waiting for a secret](#waiting-for-a-secret) |

---

## 1. Where the configuration comes from

On first boot, `corium-agent` searches four sources and uses the first source
that answers. The order goes from the most specific source to the most general
source. Thus a more specific answer always wins over a more general one.

| # | Source | Intended for |
|---|---|---|
| 1 | `/etc/corium/config.yaml` | An operator's answer for this one machine |
| 2 | cloud-init's merged document | Clouds and hypervisors |
| 3 | `corium.config=` on the kernel command line | PXE and netboot |
| 4 | `/usr/share/corium/config.yaml` | A default baked into a derived image |

Source 2 reads `/var/lib/cloud/instance/cloud-config.txt`. cloud-init merged
this document before Corium reads it. Thus cloud-init already resolved multipart
payloads and vendor-data.

Source 3 accepts a path or an `https://` URL:

```text
corium.config=/run/media/config.yaml
corium.config=https://boot.example.com/nodes/edge-01.yaml
```

If the command line contains the parameter more than once, the last one wins.
The kernel uses the same rule. Thus a value that you add at boot replaces a value
in the bootloader configuration. Corium accepts quoted values, so a path can
contain a space. A download of a configuration stops after 30 seconds, and the
maximum size is 1 MiB.

Corium treats an empty file as absent, not as an empty configuration. Otherwise, a
zero-byte file boots a node with no role.

If a source fails for any reason other than being absent, the search stops.
The intended configuration can exist but be unreachable. If Corium then uses a
default from the image, the node can join the wrong cluster without a message.

If no source answers, the node boots as an ordinary machine and writes this to
the journal. This result is supported. It is not an error.

### Two document shapes

The same schema can arrive in two shapes. Corium tells them apart by the keys
that are present.

Embedded: under `corium:` in a cloud-config, next to the keys of cloud-init:

```yaml
#cloud-config
corium:
  role: single
users:
  - name: core
```

Standalone: the schema alone at the top level. You use this shape in
`/etc/corium/config.yaml` or over PXE, where a cloud-config wrapper adds nothing:

```yaml
role: single
cluster:
  name: lab
```

Corium reads a document that contains both shapes as embedded. In a
cloud-config, a top-level `role:` almost always belongs to a different tool.

Corium handles unknown keys differently on each side of this boundary. At the
top level of a cloud-config, unknown keys belong to cloud-init, and Corium
ignores them. Inside the Corium schema, Corium rejects unknown keys, because
there they are typing errors. If Corium ignores such a key without a message, a
setting that you wrote never takes effect.

---

## 2. How it becomes a running node

```text
  source chain ──▶ parse ──▶ defaults ──▶ validate ──▶ hostname
                                                          │
                       k0s service ◀── k0s install ◀── render
```

| Step | What happens |
|---|---|
| Parse | Find the schema in the document and decode it in strict mode |
| Defaults | Fill the fields that are not set (§3.14). Idempotent (safe to run twice) |
| Validate | Report all problems at the same time, offline |
| Hostname | Set the name of the node before any step reads it (§4) |
| Secrets | Resolve `tokenFrom` / `authPassFrom` / `operatorCAFrom` |
| Claim | In maintenance mode, wait here until an operator enrolls the node (§3.13) |
| Render | Write `/etc/k0s/k0s.yaml`, then apply `k0s.patch` |
| Install | `k0s install …`, then start the service |
| Mark | Write `/var/lib/corium/bootstrapped` |

The pipeline depends on three properties:

Validation is offline and complete. It does not use the network or the
filesystem. Thus `corium-agent validate` gives the same answer on your
workstation and on the node. It reports all problems in one pass, so you do not
find your mistakes one reboot at a time.

Rendering is deterministic. The same input produces a byte-identical
`k0s.yaml`. Corium sorts map keys and does not use the random iteration order of
Go. This is important for HA, because each controller must render the same file.
The controllers share the virtual IP, the router ID and the VRRP password. If
they disagree, two controllers claim one address.

Bootstrap is idempotent. Corium writes the marker file last, and
`corium-bootstrap.service` does not start when the file exists. If a failure
occurs during the bootstrap, the node has no marker. Thus the next boot starts
again from a known point and does not continue from an unknown point. A second
bootstrap on a node that already joined a cluster destroys data, so this check
is intentional.

### What lands on the node

| Path | Contents |
|---|---|
| `/etc/k0s/k0s.yaml` | Rendered cluster configuration, mode `0600`. Controllers only |
| `/etc/k0s/join-token` | Resolved join token, mode `0600` |
| `/var/lib/corium/bootstrapped` | Marker. If it exists, the bootstrap is "already done" |
| `/etc/systemd/system/k0scontroller.service` | Written by `k0s install` |

Workers get no `k0s.yaml`. A worker gets its configuration from the control
plane that it joins.

Corium never writes secrets to the log. Corium writes tokens and passwords with
mode `0600`, and the journal refers to them only indirectly.

---

## 3. The schema

### 3.1 Root

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `role` | enum | conditional | — | Required unless the API is on and the node is waiting to be told (§3.2) |
| `cluster` | object | no | — | Identity and reachability (§3.3) |
| `kubernetes` | object | no | — | Which k0s version this node runs (§3.20) |
| `network` | object | no | — | Addressing and CNI (§3.4) |
| `storage` | object | no | — | Datastore (§3.5) |
| `join` | object | conditional | — | Required for `worker` (§3.6) |
| `node` | object | no | — | Node attributes (§3.7) |
| `addons` | list | no | — | Helm charts (§3.8) |
| `ha` | object | no | — | Control plane load balancing (§3.9) |
| `upgrades` | object | no | — | Unattended upgrades (§3.10) |
| `raid` | list | no | — | Software RAID on spare disks (§3.11) |
| `zfs` | list | no | — | ZFS pools on data disks. Needs the ZFS image (§3.17) |
| `wireguard` | list | no | — | Host WireGuard overlay interfaces (§3.12) |
| `api` | object | no | — | The management API, on by default since ADR 11 (§3.13) |
| `k0s` | object | no | — | Escape hatch (§3.15) |

### 3.2 `role`

| Value | Control plane | Workloads | Notes |
|---|---|---|---|
| `single` | yes | yes | Self-contained. You cannot add nodes later. k0s provisions it with SQLite and without the components that multi-node clusters need |
| `controller` | yes | no | Runs no kubelet, so no workload can run on it |
| `controller+worker` | yes | yes | You can add nodes. Corium passes `--no-taints`. Without this flag, the node runs no workloads and looks broken |
| `worker` | no | yes | Requires `join` |

Controllers have the labels `node-role.kubernetes.io/control-plane=true` and
`node.k0sproject.io/role=control-plane`. Use these labels to find them in
`kubectl get nodes`.

Without `--no-taints`, k0s adds the taint
`node-role.kubernetes.io/control-plane:NoSchedule` to a `controller+worker` node.
Corium always passes the flag, because a role named "controller+worker" that
runs no workloads misleads the operator. If you want the taint, add it through
`node.taints`.

`single` is the only choice in the schema that you cannot reverse. You can
change all other choices when you provision a node again. But you must rebuild a
single-node cluster to change it into a different type. If you can add a machine
in the future, use `controller+worker`. It costs nothing now, and you can add
nodes later.

#### Leaving it out

A document can omit `role`. In this case, the node waits until an operator
tells it its role:

```yaml
corium:
  api:
    enabled: true
```

The bootstrap waits until an operator sends a document with
`cctl enroll --config` or `cctl apply`. That document must name a role, because
it is the document that stops the wait. If the node gets no role when the wait
stops, it fails with
`the configuration this node was given does not name a role`.

If the document has no `api:` block, Corium refuses a missing role. The reason
is the same as for `awaitConfig`. The API is on by default, but that default is
not a request to wait. A document with no role and no `api:` block is almost
always a typing error in `role:`, not a node that must wait. Without this
refusal, the node waits forever with no message on the console.

If a document names a role, Corium builds that role. If the API is in
maintenance mode, Corium builds the role immediately when an operator claims the
node. For this reason, `cctl enroll` asks before it claims such a node. See
[cctl](cli.md#claiming-a-node-that-already-knows-what-it-is).

`single` also changes more than storage. In this mode, k0s disables
konnectivity and refuses control plane load balancing. For the same reason,
Corium rejects `ha.enabled` with `role: single`. Corium does this before the
node boots, not after.

See [k0s: configuration](https://docs.k0sproject.io/stable/configuration/).

### 3.3 `cluster`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | no | `corium` | Cosmetic, but it appears in the contexts of generated kubeconfigs |
| `endpoint` | string | conditional | — | The address that clients and joining nodes use. Corium adds it to the API certificate. Required when `ha.enabled` |
| `subjectAltNames` | list | no | — | More names in the certificate |

`endpoint` becomes `spec.api.externalAddress`. Set it to the load balancer, the
HA virtual IP, or the address of the only controller. In an HA cluster, do not
set it to one specific controller. That removes the benefit of HA.

### 3.4 `network`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `podCIDR` | CIDR | no | `10.244.0.0/16` | |
| `serviceCIDR` | CIDR | no | `10.96.0.0/12` | Must not overlap `podCIDR` |
| `cni` | enum | no | `kuberouter` | `kuberouter`, `calico`, `custom` |

`cni: custom` installs nothing. The node stays `NotReady` and pods stay
`Pending` until you install a network. This is expected. It is not a fault.
[Installing Cilium](cilium.md) gives one procedure, and
[`examples/custom-cni.yaml`](examples/custom-cni.yaml) is the document that it
boots from.

kube-router is the default of k0s. One component does networking, network
policy and service proxying. To tune any of the three, to change the kube-proxy
mode, or to enable dual-stack, use `k0s.patch`, not the `corium:` schema. See
[feature support](features.md#passthrough) and
[k0s: networking](https://docs.k0sproject.io/stable/networking/).

Corium rejects overlapping CIDRs. They produce a cluster that starts and then
sends traffic to the wrong place. This fault is very difficult to diagnose.

### 3.5 `storage`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `type` | enum | no | role-dependent | `etcd` or `sqlite` |

The default is `sqlite` for `single` and `etcd` for all other roles. Corium
rejects `sqlite` with `role: controller`, because k0s does not let other nodes
join a file-backed datastore. A second controller gives no error. It runs as a
separate single-controller cluster with its own state, and it gives no message.
Two machines that each think that they are the cluster is a worse failure than a
refusal. Thus Corium refuses.

You can run more than one controller on kine, but only with a network
datastore (MySQL, PostgreSQL). Use `k0s.patch`
(`spec.storage.kine.dataSource`) for this, not `storage.type`.

The `sqlite` value of Corium renders as `kine` in k0s. `kine` is the mechanism,
and `sqlite` is the datastore that you choose.

etcd runs inside the controllers, so you install nothing. Use an odd number of
controllers. Three controllers continue after one failure, and five continue
after two failures. A fourth controller gives no more fault tolerance than three.
To tune etcd, or to connect k0s to an external etcd cluster, use `k0s.patch`. See
[k0s: configuration](https://docs.k0sproject.io/stable/configuration/).

### 3.6 `join`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `token` | string | conditional | — | Inline. Easy in a lab, a risk in production |
| `tokenFrom` | object | conditional | — | Resolved at first boot (§3.16) |

Set exactly one. It is required for `worker`. Corium rejects it for `single`,
because a `single` node bootstraps its own cluster.

> **Warning.** Keep tokens out of instance metadata. A controller token is
> almost a cluster-admin credential. A person with an unexpired controller token
> can join a full control-plane member, with read and write access to etcd. A
> worker token gives less access, but it still lets a machine join the cluster.
> Use short expiry times and a secret store.

Create tokens on a controller that already runs:

```bash
k0s token create --role=worker     --expiry=1h
k0s token create --role=controller --expiry=1h
```

To list and revoke tokens on a controller, use `k0s token list` and
`k0s token invalidate <id>`. See
[k0s: multi-node clusters](https://docs.k0sproject.io/stable/k0s-multi-node/).

You do not have to create a token manually and paste it. Instead, ask a
controller for a complete worker block over the management API:

> **Warning.** Keep the output secret. The block below contains a valid token in
> clear text. `cctl worker-config <controller> > worker.yaml` writes only the
> YAML, and the reminder goes to standard error.

```console
$ cctl worker-config <controller> --name w-1 --label corium.dev/pool=general
corium:
  role: worker
  node:
    name: w-1
    labels:
      corium.dev/pool: general
  join:
    token: <a freshly minted worker token, inline>
```

The command creates a new token on the controller (default expiry `1h`, set
with `--expiry`). Then it prints the `corium:` block for you to paste into a
cloud-config. You add the users, the SSH keys and all other content. The
controller must run the API (§3.13), and you need your `corium:admin` client
certificate.

### 3.7 `node`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | no | derived (§4) | Hostname, and the name it registers under |
| `labels` | map | no | — | Applied to the Node object |
| `taints` | list | no | — | `key`, `value`, `effect` |

`name` must have 63 characters or fewer. It can contain only lowercase letters,
digits and hyphens. It must start and end with a letter or a digit. `effect` must
be `NoSchedule`, `PreferNoSchedule` or `NoExecute`.

Corium sorts labels before it puts them on the command line. Thus the same
input produces the same command.

Labels take effect only when the node registers for the first time. k0s gives
them to the kubelet. The kubelet applies them at registration and ignores them
after that. Thus, if you edit `node.labels` and reboot, nothing changes. The node
is already registered, and Corium does not bootstrap it twice. To change labels
on a running node, use `kubectl label`, or provision the node again. The same
rule applies to `node.taints`.

### 3.8 `addons`

This block lists Helm charts that k0s installs at bootstrap through its Helm
extensions. Corium uses no Helm binary and no operator in the cluster.

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | yes | — | Release name |
| `chart` | string | yes | — | Qualified, `repository/chart` |
| `version` | string | no | — | Always set it |
| `namespace` | string | no | `default` | |
| `repository` | object | conditional | — | `name` and `url` |
| `values` | map | no | — | Passed to Helm without change |

An add-on in the same document must declare each repository that a `chart`
uses. Corium rejects a chart that names an undeclared repository.

If you do not set `version`, the node gets the latest version at boot time. Thus
the result on the node depends on the time when it booted.

Corium rejects add-ons on workers. Only a controller installs them, so add-ons
on other nodes never take effect.

k0s installs charts one time, at bootstrap. Corium does not model the removal of
a chart. If you delete an add-on from the configuration of a node that already
bootstrapped, nothing happens, because the node does not bootstrap again. To
remove a release, delete the k0s Chart resource that it created:

```bash
kubectl delete chart <name> -n kube-system
```

See [k0s: Helm charts](https://docs.k0sproject.io/stable/helm-charts/).

### 3.9 `ha`

This block gives a highly available control plane without an external load
balancer and without PKI in the configuration. The controllers run VRRP between
them, and one controller holds a virtual IP.

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `enabled` | bool | no | `false` | |
| `virtualIP` | CIDR | yes | — | With a prefix length. keepalived needs it to add the address |
| `interface` | string | no | the interface that has the default route | |
| `virtualRouterID` | int | no | assigned by k0s, from 51 | 1–255. Must be unique in the broadcast domain |
| `authPass` | string | conditional | — | Eight characters or fewer |
| `authPassFrom` | object | conditional | — | Alternative to `authPass` (§3.16) |
| `unicastPeers` | list | no | — | The addresses of the other controllers |

`authPass` has a limit because keepalived cuts it to eight characters and gives
no message. With a longer value, two controllers can think that they share a
password, but they do not. Thus Corium rejects a longer value.

`unicastPeers` is required on each network without multicast. Most clouds have
no multicast. On a flat L2 segment, `unicastPeers` causes no problem.

The eight-character limit does not come from Corium. k0s itself rejects a longer
value with `AuthPass must be 8 characters or less`. Corium finds the error during
validation, so the node does not get to the point where it fails to start.

You can put an external load balancer in front of the controllers and not use
the virtual IP. In this case, it must send three ports to each controller: 6443
(Kubernetes API), 8132 (konnectivity) and 9443 (the join API).

`cluster.endpoint` is required when HA is enabled, and it must be the virtual
IP. Without it, clients connect to one controller, and the VIP gives no benefit.

Corium rejects HA for `single` (one node only) and for `worker` (no control
plane to balance). Corium also rejects HA settings when `enabled` is false,
because they have no effect and give no message.

Certificates never appear here. Controllers two and three join with a token,
and k0s sends them the cluster CA over its join API on port 9443. See
[`examples/ha-controller-first.yaml`](examples/ha-controller-first.yaml) and
[k0s: control plane load balancing](https://docs.k0sproject.io/stable/cplb/).

Corium does not model a related feature, node-local load balancing. It runs a
proxy on each worker, so that kubelet and kube-proxy can connect to any healthy
controller without an external load balancer. It solves the problem for workers.
CPLB solves it for clients. You can use it through `k0s.patch`. k0s documents
that it is not compatible with `spec.api.externalAddress`, and Corium sets that
field from `cluster.endpoint`. See
[k0s: node-local load balancing](https://docs.k0sproject.io/stable/nllb/).

### 3.10 `upgrades`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `automatic` | enum | no | `none` | `none`, `download` or `apply` |
| `schedule` | string | no | `daily` | systemd `OnCalendar` expression |

If you set `schedule`, Corium also removes the random delay. The default daily
run has a random delay of up to one hour. Thus the nodes do not all connect to
the registry at the same time. An explicit schedule is a maintenance window. A move
of up to one hour removes the purpose of that window.

`none` does nothing. `download` stages a newer image and does not reboot. Thus
the reboot that you schedule is almost instant. `apply` drains the node, reboots
into the staged image, and uncordons the node when k0s runs again.

Corium rejects a `schedule` with `automatic: none`, because that setting has
no effect and gives no message.

See [upgrades](upgrades.md#unattended-upgrades).

### 3.11 `raid`

This block is a list of software RAID arrays. Corium builds them from the spare
disks of the node at first boot. It does not include the disk that the OS booted
from. [Software RAID](raid.md) gives the reason, and tells how to install onto a
redundant root.

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | yes | — | Becomes `/dev/md/<name>` |
| `level` | int | yes | — | `0`, `1`, `5`, `6` or `10` |
| `devices` | list | yes | — | Whole disks, absolute paths |
| `spares` | list | no | — | Idle members that replace a member that fails |
| `filesystem` | enum | no | `ext4` | `ext4`, `xfs`, or `none` for a raw device |
| `mountPoint` | string | no | — | Absolute path. Also written to `/etc/fstab` |
| `wipe` | bool | no | `false` | Permission to erase devices that hold data |

> **Warning.** `wipe` is off by default, and this is intentional. If a device has
> a filesystem, a partition table, or the metadata of another array, the
> bootstrap stops. The error names what Corium found. You can recover a node that
> refuses to boot. You cannot recover a disk that Corium erased without a
> message.

```yaml
corium:
  role: controller+worker
  raid:
    - name: data
      level: 1
      devices:
        - /dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi1
        - /dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi2
      filesystem: ext4
      mountPoint: /var/lib/corium/data
```

Validation rejects these errors before Corium touches a disk:

- A level with too few devices for it.
- A device that two arrays use.
- Two arrays with the same name.
- A spare on a RAID 0.
- A `mountPoint` on an array with `filesystem: none`.

Use `/dev/disk/by-id/...`, not `/dev/sdb`. The kernel gives names in the order
that it finds the disks. Thus on a first boot, a name can point to a different
disk than the one that you meant.

### 3.12 `wireguard`

*New in 0.3.0.* Version 0.2.0 has neither this field nor the `wireguard-tools`
package.

This block is a list of host WireGuard interfaces. Corium starts them at first
boot, before k0s. Thus a cluster can run over an encrypted overlay between hosts
that share no network, for example across sites or providers. This is a host
function, separate from the CNI. k0s can encrypt pod traffic, but only if the
hosts can already connect to each other. See
[ADR 6](adr/0006-host-wireguard-overlay.md).

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `name` | string | yes | — | Interface name, for example `wg0`. Unique on the node |
| `address` | CIDR or list | yes | — | The overlay address of this node, with prefix length. A single CIDR, or a list for a dual-stack interface (`[10.10.0.1/24, fd00::1/128]`) |
| `listenPort` | int | conditional | — | UDP port. Required on each node that a peer connects to |
| `mtu` | int | no | — | Replaces the interface MTU |
| `nodeAddress` | bool | no | `false` | Register this address with k0s (see below). One interface at most |
| `privateKey` | string | conditional | — | Base64 key. Inline is a risk. Use `privateKeyFrom` |
| `privateKeyFrom` | object | conditional | — | Resolve the key at first boot (§3.16) |
| `peers` | list | no | — | The other ends of the overlay |

Each entry in `peers`:

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `publicKey` | string | yes | — | Base64. Not a secret |
| `endpoint` | string | no | — | `host:port`. Use an IP if you can |
| `allowedIPs` | list | yes | — | CIDRs routed to this peer. Must not overlap the CIDRs of another peer |
| `persistentKeepalive` | int | no | — | Seconds. Set it behind NAT (25 is usual) |
| `presharedKey` | string | no | — | Base64. A secret. Use `presharedKeyFrom` |
| `presharedKeyFrom` | object | no | — | Resolve it at first boot (§3.16) |

```yaml
corium:
  role: controller+worker
  cluster:
    endpoint: 10.10.0.1          # the overlay address, not the physical one
  wireguard:
    - name: wg0
      address: 10.10.0.1/24
      listenPort: 51820
      nodeAddress: true          # k0s registers this address, not the physical NIC
      privateKeyFrom:
        file: /run/corium/wg0.key
      peers:
        - publicKey: "PEER_PUBLIC_KEY_BASE64"
          endpoint: node-b.example:51820
          allowedIPs: [10.10.0.0/24]
          persistentKeepalive: 25
```

`nodeAddress` makes the overlay the transport of the cluster. Without it, the
kubelet registers the address that it finds on the physical NIC. Then other nodes
cannot connect to the node across the overlay for logs, exec, port-forward and
metrics. `ha` prevents the same failure for the virtual IP. Set `nodeAddress` on
the one interface that other nodes must use to connect to this node.
`corium-agent` gives that address to the kubelet as `--node-ip`. Only one
interface can set it. On a dual-stack interface, Corium registers the first
address in the list. Thus put the address that you want as the node IP first.

The interface starts before k0s and starts again after each reboot.
`corium-agent` writes `/etc/wireguard/<name>.conf` with mode `0600`, because the
file contains the private key. Then it enables `wg-quick@<name>` and makes the
k0s service require it. Thus, if the overlay does not start, the node does not
half-join a cluster. The image contains `wireguard-tools`, but the tools do
nothing until you declare an interface. The kernel module is in-tree and loads
when it is necessary.

Validation rejects these errors before Corium starts an interface:

- A bad or duplicate interface name.
- An address with no prefix length.
- A port or MTU out of range.
- A key that is not a 32-byte base64 value.
- More than one `nodeAddress`.
- A peer with no public key, or with a duplicate public key.
- An endpoint that is not `host:port`.
- A missing or malformed `allowedIPs`.
- Two peers with overlapping ranges.

An error message never shows a private key or a preshared key.

The escape hatch is still available for an overlay that is not the cluster
transport, or for a node with no cloud-init datasource. It is a `write_files`
configuration and an enabled `wg-quick@` unit.
[ADR 6](adr/0006-host-wireguard-overlay.md) tells when to use which.

### 3.13 `api`

This block configures the management API of the node, `corium-apid`.
[ADR 4](adr/0004-management-api.md) describes it in full. Since
[ADR 11](adr/0011-api-on-by-default.md), the API is on by default. A node with
no `api:` block serves the enrolment route until it bootstraps. The pairing code
on the console of the node authenticates that route.

> Shipped in 0.2.0: all four management surfaces, CA rotation and the local
> recovery path. Each release publishes `cctl`. See
> [downloads](install/downloads.md#install-cctl). The daemon runs unconfined
> under SELinux. [ADR 4](adr/0004-management-api.md) gives the reason, and lists
> what a confined domain must allow.
>
> This section is the `api:` schema. [cctl](cli.md) tells how to use it: claim a
> node, read its journals, upgrade it, and give it to a different operator.

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `enabled` | bool | no | on | `false` is the only way to have no daemon and no port. `true` with no CA asks for maintenance mode. Maintenance mode also stops the bootstrap until the node is claimed |
| `operatorCA` | string | no | — | PEM certificate of the CA that signs operator client certificates |
| `operatorCAFrom` | object | no | — | Resolve the CA at first boot (§3.16) |
| `insecure` | bool | no | `false` | Remove the pairing code. Maintenance mode only |
| `awaitConfig` | bool | no | `false` | Stop the bootstrap until an operator sends a configuration. Needs an explicit `api:` block next to it. A document with no `role` sets it implicitly (§3.2) |

Set at most one of `operatorCA` and `operatorCAFrom`.

You can provision a full fleet from one cloud-config that has no secrets and no
machine-specific data. To do this, do not write the role:

```yaml
corium:
  api:
    enabled: true
```

A node that boots with this document waits for an operator. The operator must
claim it and also tell it its role. Then the node bootstraps with the document
that the operator sent. The operator uses `cctl enroll --config` for one step,
or `cctl apply` after the claim. The node waits because the document contains
nothing to build. No flag causes the wait (§3.2).

`awaitConfig` gives the same instruction explicitly. It is the only way to give
it in a document that names a role, for example a fleet-wide `role: worker` that
expects the other data per machine. Without `awaitConfig`, a document that names
a role builds that role immediately. In maintenance mode, the node first waits
until an operator claims it.

Corium refuses `awaitConfig` in a document that has no `api:` block. A document
must ask explicitly to stop the bootstrap. Since ADR 11, a running API is not
that request.

The `operatorCA` value is a certificate, not a key. The node never gets the
private key that signs with the certificate. For this reason, the value is safe
in clear text in cloud-init, but a join token is not:

```yaml
corium:
  role: worker
  api:
    operatorCA: |
      -----BEGIN CERTIFICATE-----
      MIIBkTCB+6ADAgECAhRk...
      -----END CERTIFICATE-----
```

If you write `enabled: false` with one of these keys, Corium gives an error. No
precedence rule applies. The configuration says two contradictory things. If
Corium guessed which one you meant, the other has no effect and gives no
message.

#### The four ways a node is claimed

| `api:` | Mode | What happens |
|---|---|---|
| absent | enrollable | The default. The daemon serves, and an operator can claim the node with the pairing code. Nothing waits. A document that names a role builds it on first boot, the same as with the API off |
| `enabled: false` | off | No daemon, no port |
| `operatorCA` | A | The CA is inline, in clear text. Unattended. The metadata contains no secret, because a certificate is public |
| `operatorCAFrom` | B | The same, resolved at first boot from a `SecretSource` (§3.16) |
| `enabled: true`, neither key | C | Maintenance mode. The node stops its bootstrap and waits until an operator claims it |

The default and mode C have one difference, and it is the important one. Mode C
waits. The enrollable mode serves the port and continues the bootstrap. A node
waits only if its document asks for it by name, with `awaitConfig` or with no
`role`. Thus the new default does not change a configuration already in service.
It only opens the port.

The daemon runs only when it has a function. In two states, it has no function,
so it stops and does not serve. The first state is a node that bootstrapped and
names no operator CA. Its enrolment is closed, and the daemon has nobody to
authenticate. The second state is `enabled: false`, which asks for nothing. In
both states, `corium-apid` exits with code 78 and opens no port. Thus each node
is claimable, or it already answers to an operator.

Modes A and B claim the node at boot, so it joins its cluster unattended. Mode C
does not. A node that waits for a claim is in no cluster. The node validates its
configuration and stops before the k0s bootstrap. Then it shows a single-use
pairing code and its certificate fingerprint on the console, the serial port and
the journal.

This order is the important part. It is not a detail. Suppose that the node
bootstraps first. Then it runs workloads and holds cluster credentials. At the
same time, it obeys the first client that connects to an unauthenticated port.
The machine is valuable and unclaimed at the same time. The rule also applies
in the other direction, and this gives `cctl reset` its meaning. A node cannot
go back to maintenance mode while it is a cluster member. Thus a reset removes
the node from the cluster.

Enrolment closes when the node bootstraps, independent of the configuration.
This keeps the rule true now that the port is open by default. A node with no
`api:` block is claimable during the minute of its bootstrap, and never after.
If a node gets `enabled: true` after it is already in service, nobody can claim
it. To take control of a bootstrapped node, use `cctl reset`.

Enrolment sends a CA certificate. It can also send the configuration of the
node, with `cctl enroll --config`, or with `cctl apply` after the claim. The
bootstrap limits this, not a permission. A node that bootstrapped refuses it,
from any operator. Thus nobody can change the role or the cluster of a machine
in service. Decisions 6 and 7 stay true.

Mode C has a cost: it is not zero touch. For three nodes, you go to three
consoles. If you want unattended provisioning with no secret in the metadata, use
`operatorCA`. The certificate is not a secret.

[`cctl`](cli.md) tells how to claim a node and how to manage it after the claim.
Before you give `corium:readonly` to a person, know that it reads journals.
Corium does not remove secrets from journals. Journals contain all data that any
software on the node wrote to the log.

#### `insecure`

`api.insecure: true` removes the pairing code. The first client that connects
to an unclaimed node claims it, with no proof. It applies only to maintenance
mode. In other cases it does nothing, so Corium gives a validation error and
does not ignore it. These cases are: with an operator CA, with the API off, or
alone without `enabled: true`.

[ADR 11](adr/0011-api-on-by-default.md) made the port the default, not
`insecure`. A node with no `api:` block still asks for the pairing code. The code
is on a channel that an attacker on the network cannot read.

Two facts limit the risk. First, an unclaimed node is in no cluster, so the
first client gets an empty machine. Second, enrolment is one-way, so the
window closes when a client uses it. But that client controls the future of the
machine: the machine obeys the CA that the client pins.

Use this setting for a bench, a lab, a provisioning network that you fully
control, or a PXE fleet where you cannot go to each console. The node shows a
clear warning:

```text
  !! api.insecure is set: no pairing code is required, so the
  !! first client to reach this port claims this node for good.
```

The node also records that its claim was unauthenticated, and `cctl status`
reports this from then on. A node holds the same pinned CA for each type of
claim. Without that record, you cannot find which machines of a fleet the
first client claimed. A later CA rotation does not remove the record.

#### Precedence and validation

| Written | Result |
|---|---|
| `enabled: false` with either CA key | Validation error. The configuration says two contradictory things |
| both CA keys | Validation error, the same as for `token` and `tokenFrom` |
| `insecure` outside maintenance mode | Validation error. It has no effect and gives no message |
| `awaitConfig` with no explicit `api:` block | Validation error. A document must ask explicitly to stop the bootstrap |
| no `role` and no explicit `api:` block | Validation error. This is almost always a typing error in `role:`, not a node that must wait |

Validation examines an inline `operatorCA`. It must be one PEM certificate, it
must be a CA, and it must not be expired. If you paste a private key there,
Corium rejects it and names the error. In that case, the key that controls your
fleet is in a document that goes into instance metadata. Treat that key as
compromised.

Corium records the enrolment under `/var/lib/corium/api/`. The pinned CA has
mode `0644`, because a certificate is not a secret. The serving key of the node
has mode `0600`, because it is a secret. The record stays after reboots and
upgrades. A claimed node never goes back to maintenance mode without an operator
action. Otherwise, a power cycle is enough to take control of a machine.

#### Where the daemon stands with SELinux

`corium-apid` runs as `unconfined_service_t`, the same as `corium-agent` and
all other services on the image. It has no confined domain, and the image ships
no policy module. Its systemd unit limits it with `NoNewPrivileges`,
`ProtectHome`, `PrivateTmp`, `RestrictNamespaces`, `MemoryDenyWriteExecute` and
`LockPersonality`. Each of these settings was tested on a real node across a
`bootc switch` and a `k0s reset`. [ADR 4](adr/0004-management-api.md) gives the
reason why the daemon is privileged. It also lists each file and command that
the daemon uses, and the work to write a confined domain.

#### Calling the API without cctl

You can also use `curl`. This is one reason why the API uses JSON over HTTP:

```console
$ curl -k --cert ~/.corium/client.crt --key ~/.corium/client.key \
    https://192.168.1.51:7443/v1/health
{"status":"ok","role":"corium:admin"}
```

The `-k` flag is necessary. The node signs its own certificate, because a
configuration never contains a private key. The fingerprint is the check, and
`cctl` pins it for you.

### 3.14 Defaults

| Field | Default |
|---|---|
| `cluster.name` | `corium` |
| `network.podCIDR` | `10.244.0.0/16` |
| `network.serviceCIDR` | `10.96.0.0/12` |
| `network.cni` | `kuberouter` |
| `storage.type` | `sqlite` for `single`, `etcd` otherwise |
| `addons[].namespace` | `default` |
| `upgrades.automatic` | `none` |
| `upgrades.schedule` | `daily` |
| `backup.schedule` | `daily`, but only when `backup.enabled` is set |
| `backup.path` | `/var/lib/corium/backups`, with the same condition |
| `backup.keep` | `7`, with the same condition |
| `node.name` | Derived from the machine ID (§4) |
| `zfs[].vdevs[].type` | `stripe` |
| `zfs[].mountPoint` | `/<name>` (ZFS's own default) |

The defaults step is idempotent, and it never replaces an explicit value.
Corium applies the ZFS vdev type when it builds the pool. It does not write it
into the document, so a rendered configuration shows it as not set.

The `backup` defaults apply only to a `backup` block that sets a value. If
Corium always fills them in, each node gets a `backup` block that the operator
did not write. Then Corium rejects each worker, because a worker cannot have a
`backup` block.

### 3.15 `k0s.patch` — the escape hatch

This is a strategic merge patch. Corium applies it to the rendered `k0s.yaml`
after all other steps, and does not interpret it. Thus you can set each k0s
setting, also the settings that Corium does not know.

```yaml
corium:
  k0s:
    patch:
      spec:
        api:
          extraArgs:
            audit-log-path: /var/log/kubernetes/audit.log
```

Maps merge key by key. The patch replaces all other types, including lists,
completely. If a patch sets a list, the result is that list. The patch does not
add the list to the existing one.

The patch can replace values that Corium calculated, including values that
Corium needs. This makes it an escape hatch and not a suggestion. Corium checks
only that the result is valid YAML. If a patch breaks the cluster, you are
responsible for it.

The second escape hatch is the document itself. It stays a normal cloud-config,
so `write_files`, `runcmd` and all other modules continue to work. Corium is a
guest in that document, not its owner.

### 3.16 Secret sources

`join.tokenFrom`, `ha.authPassFrom` and `api.operatorCAFrom` use a secret
source. With a secret source, the value does not have to be in instance metadata.
Each process that can connect to the metadata service can read that metadata.

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | string | conditional | — | Must be `https` |
| `file` | string | conditional | — | Absolute path |
| `authFile` | string | no | — | File that holds a bearer token for `url` |
| `waitFor` | duration | no | — | Try again until the secret appears, for this maximum time |

Set exactly one of `url` or `file`. Corium rejects plain HTTP, and you cannot
disable this rule. Each system on the network path can read a token that Corium
gets over HTTP.

### Waiting for a secret

With `waitFor`, all nodes of a cluster can start at the same time. A joining
node can boot before the node that creates its token is ready. The joining node
waits, and joins when the token appears. Without `waitFor`, it fails, and an
operator must start the machines in the correct order manually.

```yaml
join:
  tokenFrom:
    url: https://secrets.example.com/corium/controller-token
    waitFor: 15m
```

Corium waits only when the secret is absent: a missing file, a refused
connection, or a `404`, `408`, `425`, `429`, `502`, `503`, `504`. A rejected or
malformed request fails immediately. If Corium tries a wrong credential again for
15 minutes, it helps nobody and hides the mistake.

The delay between attempts increases up to 32 seconds and then stays at 32
seconds. The maximum wait is one hour, because after one hour, nobody watches
the node.

If you omit `waitFor`, the behavior stays strict: one attempt, then a failure.

A download stops after 30 seconds and reads 256 KiB at most. Errors never show
the response body. The value is a credential, and a message that writes it to
the journal discloses it.

### 3.17 `zfs`

This block is a list of ZFS pools. Corium builds them from the data disks of
the node at first boot, before k0s. It does not include the disk that the OS
booted from. It needs the ZFS image variant, because the base image does not
contain the module. See [ZFS on data disks](zfs.md) for the guide,
[deploy/zfs/README.md](../deploy/zfs/README.md) to build the image, and
[ADR-0007](adr/0007-zfs-data-disks.md) for the reason why it is a variant.

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Required. The pool name. Also the default mount point `/<name>` |
| `vdevs` | list | — | Required. One or more virtual devices. The pool stripes across them |
| `mountPoint` | string | `/<name>` | Mount point of the root dataset. An absolute path, or `none`/`legacy` to keep it unmounted |
| `options` | map | — | Pool properties (`zpool create -o`), for example `ashift: "12"`. You cannot change them after the pool exists |
| `filesystemOptions` | map | — | Properties of the root dataset that all datasets inherit (`-O`), for example `compression: lz4` |
| `datasets` | list | — | Child filesystems, each with its own `mountPoint` and `properties` |
| `wipe` | bool | `false` | Permission to erase devices that hold data |

Each vdev:

| Key | Type | Default | Notes |
|---|---|---|---|
| `type` | enum | `stripe` | `stripe`, `mirror`, `raidz`, `raidz2`, `raidz3` |
| `devices` | list | — | Required. Whole disks, absolute paths under `/dev` |

Each dataset:

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Required. Relative to the pool. Slashes make a hierarchy |
| `mountPoint` | string | inherited | Absolute path, or `none`/`legacy` |
| `properties` | map | — | ZFS properties, for example `recordsize`, `quota`, `compression` |

Minimum devices per vdev: `stripe` 1, `mirror` 2, `raidz` 2, `raidz2` 3,
`raidz3` 4.

```yaml
corium:
  role: controller+worker
  zfs:
    - name: tank
      options:
        ashift: "12"
      filesystemOptions:
        compression: lz4
      vdevs:
        - type: mirror
          devices:
            - /dev/disk/by-id/wwn-0x5000c500a0b1c2d3
            - /dev/disk/by-id/wwn-0x5000c500a0b1c2d4
      datasets:
        - name: data
          mountPoint: /var/lib/corium/data
```

Validation rejects these errors before Corium touches a disk:

- A vdev type with too few devices.
- A device that two vdevs use.
- A device that a pool and a `raid[]` array both use.
- Two pools with the same name.
- A duplicate dataset name.
- A mount point that is not absolute and is not `none` or `legacy`.

`wipe` is off by default, and this is intentional, the same as for `raid[]`. If a
device has a filesystem, a partition table, or the label of a different pool or
array, the bootstrap stops. Corium does not erase the device.

Use `/dev/disk/by-id/...`, not `/dev/sdb`, for the reason that [`raid`](#311-raid)
gives: the kernel gives names in the order that it finds the disks.

### 3.18 `backup`

This block runs `k0s backup` on a schedule on a control-plane node. Corium
creates a systemd timer for it at first boot. Each run writes one archive of the
control plane state. The archive contains the etcd or kine/SQLite datastore, the
PKI, `k0s.yaml`, the manifests and the Helm configuration. Then the run deletes
the oldest archives after the number in `keep`.

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Turns the timer on. Without it, the other keys have no effect |
| `schedule` | string | `daily` | systemd `OnCalendar` expression. See systemd.time(7) |
| `path` | string | `/var/lib/corium/backups` | Absolute path of the directory for the archives |
| `keep` | int | `7` | The number of archives that stay after a run, newest first |

```yaml
corium:
  role: controller+worker
  backup:
    enabled: true
    schedule: "*-*-* 02:30:00"
    path: /mnt/backups
    keep: 14
```

Corium writes the archive with mode `0600` in a directory with mode `0700`. If
the directory does not exist, Corium creates it. The archive contains the private
key of the cluster CA and all Secrets in etcd. Thus protect it the same as a
kubeconfig. For each run, Corium logs only the path and the size.

The archive name is `corium-backup-<UTC timestamp>.tar.gz`. Corium makes this
name itself, so that the cleanup step can find the archives that Corium wrote. A
run deletes only regular files with exactly this name format. It never touches
other files in the directory, including a `k0s backup` that you made manually.

The default `path` is on the disk of the node. A backup there survives an
operator mistake and nothing else. If you need the backups to be useful, set the
path to a mount on a different machine. Corium creates the directory, but not
the mount.

Validation rejects a `backup` block on a `role: worker` node. `k0s backup` reads
a control plane and does not run without one. The timer fails each time, and
you think that the node has backups. Validation also rejects these
errors:

- A relative `path`.
- A negative `keep`.
- A `schedule` that is not a calendar expression (`0 3 * * *` is cron, and
  systemd wants `*-*-* 03:00:00`).
- A block configured with `enabled: false`, because it has no effect and gives
  no message.

You cannot keep all archives. A directory that always grows fills the filesystem
that holds `/var/lib/k0s`. Then the cluster stops because of its own backups.
For a long history, set a large `keep`. The limit is intentional.

The archive does not contain PersistentVolumes, workload data, or an external
datastore. See the [k0s backup
documentation](https://docs.k0sproject.io/stable/backup/) for the exact list.

#### Restore is manual

There is no `restore` field, and Corium will not add one. `k0s restore`
destroys data. An operator runs it on a stopped node, usually a new one, after
the operator selects the correct archive. If a timer does this automatically, a
cluster can lose one day of state.

An operator runs these commands on a machine where k0s is installed but does not
run:

```bash
# On the node being restored, as root.
systemctl stop k0scontroller
k0s restore /mnt/backups/corium-backup-20260925T023000Z.tar.gz
k0s install controller --enable-worker   # the same arguments the node had
systemctl start k0scontroller
```

Two conditions are necessary for a successful restore. First, the external
address of the cluster must be the same as at the time of the backup, because
the certificates contain it. Second, on a multi-controller cluster, restore onto
one new controller and start it. Then join the other controllers with a new
token. Do not restore three machines.

---

### 3.19 `manifests`

This block is plain Kubernetes YAML. Corium writes it to
`/var/lib/k0s/manifests/<stack>/` before k0s starts. This uses the
[Manifest Deployer](https://docs.k0sproject.io/stable/manifests/) of k0s. It is
the second part of the k0s extension mechanism. The first part is the Helm
charts that [`addons`](#38-addons) models.

Each direct subdirectory of `/var/lib/k0s/manifests` is an independent stack (a
set of manifests that k0s manages together). The controller applies each `.yaml`
file in it, and keeps the cluster the same as the files. If you remove a file,
the controller deletes its resources.

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Required. The stack directory name. No slash, no leading dot |
| `files` | list | — | Required. One or more manifest files. See below |

Each file:

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | Required. A plain file name that ends in `.yaml` |
| `content` | string | — | Required. The manifest, written byte for byte. Several documents separated by `---` are permitted |

```yaml
corium:
  role: controller+worker
  manifests:
    - name: metallb-config
      files:
        - name: pools.yaml
          content: |
            apiVersion: metallb.io/v1beta1
            kind: IPAddressPool
            metadata:
              name: default
              namespace: metallb-system
            spec:
              addresses:
                - 192.0.2.10-192.0.2.20
            ---
            apiVersion: metallb.io/v1beta1
            kind: L2Advertisement
            metadata:
              name: default
              namespace: metallb-system
```

Use it for objects that have no chart: a MetalLB `IPAddressPool`, a
cert-manager `ClusterIssuer`, a `StorageClass`, or the RBAC that an in-house
controller needs. Put each object that has a chart in [`addons`](#38-addons).

Corium rejects `.yml`. The deployer reads only the `.yaml` extension. k0s does
not report a file named `.yml`. It ignores the file and gives no message. Thus
Corium refuses the file at validation.

Do not give a stack the name of a k0s stack. k0s runs several of its in-cluster
components as stacks in the same directory. Corium cannot list them, because the
upstream project changes the set. Thus use a name that is clearly yours.

Validation rejects these errors offline, before Corium writes a file:

- A stack or file name that contains a slash, `..`, or a leading dot.
- Two stacks with the same name.
- Two files with the same name in one stack.
- A file name that does not end in `.yaml`.
- Empty content.
- Content that is not valid YAML.

Corium checks only the syntax. It has no schema for a Kubernetes object. If the
API server rejects a document, the deployer reports it in the controller log.

A controller applies manifests, so Corium rejects them on a worker. The rule and
the reason are the same as for `addons`.

Corium writes them before `k0s install`. If a stack arrives after that, the
cluster already reports that it is ready. For example, the node boots and
becomes ready without its address pool. It gets the pool later, possibly after a
workload that waited for a LoadBalancer stopped.

You cannot change `manifests` on a node that already bootstrapped. This is true
for all fields except `addons`, `k0s.patch` and `kubernetes.version`. `cctl apply` refuses the change
and names the field. To change a live stack, use `kubectl`, or reset the node.

---

### 3.20 `kubernetes`

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `version` | string | no | the floor of the image | A k0s release tag, such as `v1.36.4+k0s.1`. Must be inside the window that the booted image supports |
| `mirror` | string | no | the release default | The registry for the extension, without a tag or digest |

The Kubernetes version is separate from the OS image, and you can change each
one without the other. The image contains one version in `/usr/bin/k0s`. This
version is the floor (the version that the image always has). A signed system
extension can replace it with a different version from the window that the
image supports. Thus you can install a Fedora security fix without a new
Kubernetes minor version. You can also install a new Kubernetes minor version
without a Fedora rebase.

Both fields are optional. A node that declares neither field runs the floor of
its image and needs no network for it. This is intentional: a node must always
be able to boot, even when it cannot get a version from the network.

```yaml
corium:
  role: single
  kubernetes:
    version: v1.36.4+k0s.1
```

An image supports a window of three Kubernetes minor versions: the minor of its
floor, and the two adjacent minors. The skew policy of k0s sets this width.
Upgrades go one minor at a time, controllers first, and a worker can be one minor
behind its controllers. Thus a cluster during a rollout can correctly have two
minors. `/usr/lib/corium/k0s.window` lists the versions that a booted image
supports.

Corium refuses a version outside the window. To get such a version, upgrade the
OS. The OS version and the Kubernetes version are independent, but not without
limits.

k0s has stricter skew rules than upstream Kubernetes: one minor at a time,
controllers before workers, and no return to an earlier minor. Corium refuses a
change that breaks these rules, and names the rule, before it stops anything.
Thus a node does not find out during an upgrade that k0s will not start. A
rollback of a Kubernetes version is not free, as an OS rollback is. A rollback is
a second forward operation, and k0s does not support a downgrade of a minor
version.

For an air-gapped site, set `mirror` to a registry that the node can connect to.
A mirror changes where an artifact comes from. It does not remove the need for a
signature. The signature rule is the same rule that protects an OS image.

```yaml
corium:
  role: single
  kubernetes:
    version: v1.36.4+k0s.1
    mirror: registry.internal.example/corium/k0s
```

`mirror` is a repository, not a reference. It has no tag and no digest,
because `version` selects the artifact in it.

After the bootstrap, `cctl apply` re-applies `kubernetes.version` on a running
node, together with `addons` and `k0s.patch`. See
[ADR 8](adr/0008-day-two-reconcile.md).
[ADR 10](adr/0010-kubernetes-version-axis.md) gives the reasons for the full
design.

## 4. How the hostname is settled

Kubernetes identifies a node by its hostname. A duplicate hostname gives no
clear error. The nodes replace the Node object of each other in turn, and all
nodes report that they are healthy.

| # | Source | Used when |
|---|---|---|
| 1 | `node.name` | Set explicitly |
| 2 | The current hostname | A different tool already set a real one |
| 3 | `corium-<machine-id[:8]>` | The hostname is still generic |

Generic means `fedora`, `localhost`, `localhost.localdomain` or empty. An
unconfigured image boots with these names, and they do not identify a node.

The derived name is not random. A random name changes at each reboot. Then
each reboot registers a new node and keeps the old node as a ghost.
systemd makes the machine ID on first boot, and the image contains no machine
ID. Thus the ID is unique for each node and does not change during its life.

If you clone a disk after first boot, the clone gets the same machine ID. Clear
`/etc/machine-id` on the clone, or set `node.name`.

Corium sets the hostname before k0s starts, because k0s registers the node with
the name that it reads at startup.

### Node address

When HA is enabled, Corium sets the kubelet `--node-ip` to the address of the
node itself, not to the virtual IP.

Without this setting, the kubelet can register the VIP, because it uses the
address that it finds on the interface. The VIP belongs to the controller that
currently wins the election. The failure comes later. All functions work until
the first failover. After the failover, the advertised address of the node
belongs to a different machine. Then logs, exec, port-forward and metrics all go
to the wrong node.

---

## 5. A worked example

```yaml
#cloud-config
corium:
  role: controller+worker
  cluster:
    name: prod
    endpoint: 10.0.0.10
    subjectAltNames: [k8s.example.com]
  storage:
    type: etcd
  node:
    name: ctrl-1
    labels: {pool: general}
  addons:
    - name: cert-manager
      chart: jetstack/cert-manager
      version: 1.16.2
      namespace: cert-manager
      repository: {name: jetstack, url: 'https://charts.jetstack.io'}
      values: {crds: {enabled: true}}
  k0s:
    patch:
      spec:
        api:
          extraArgs: {audit-log-path: /var/log/audit.log}
```

Renders to:

```yaml
apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
metadata:
    name: prod
spec:
    api:
        externalAddress: 10.0.0.10
        extraArgs:
            audit-log-path: /var/log/audit.log
        sans:
            - 10.0.0.10
            - k8s.example.com
    extensions:
        helm:
            charts:
                - chartname: jetstack/cert-manager
                  name: cert-manager
                  namespace: cert-manager
                  values: |
                    crds:
                        enabled: true
                  version: 1.16.2
            repositories:
                - name: jetstack
                  url: https://charts.jetstack.io
    network:
        podCIDR: 10.244.0.0/16
        provider: kuberouter
        serviceCIDR: 10.96.0.0/12
    storage:
        type: etcd
    telemetry:
        enabled: false
```

and runs:

```bash
k0s install controller --config /etc/k0s/k0s.yaml \
    --enable-worker --no-taints --labels pool=general
```

Look at these points in the output:

- `endpoint` appears in `sans`, but you did not ask for it.
- The patch merged into `spec.api` and did not change the other keys in it.
- The chart values became the YAML string that k0s expects.
- Telemetry is off by default. Corium sends no data to its developers, and the
  clusters that it builds send none.

To get this output without a machine, run:

```bash
corium-agent validate node.yaml
corium-agent bootstrap --dry-run --config node.yaml
```

`--dry-run` resolves no secrets and renames nothing. It has no effect outside
the process. A connection to a secret store for output that nobody applies is a
surprise. On a shared network, it also tells other people what you plan to do.
