# Configuration reference

What the configuration file contains, where it comes from, and what Corium does
with it.

For a task-oriented introduction, start with the [quick start](quickstart.md).
For what Corium models versus what it passes through to k0s, see
[feature support](features.md).

Anything here describing k0s behaviour links to the
[official k0s documentation](https://docs.k0sproject.io/). Corium configures
upstream k0s without forking or patching it, so where the two disagree, k0s is
right and this page is stale.

---

## 1. Where the configuration comes from

`corium-agent` searches four sources on first boot and uses the first that
answers. The order runs from most specific to most general, so a more targeted
answer always beats a broader one.

| # | Source | Intended for |
|---|---|---|
| 1 | `/etc/corium/config.yaml` | An operator's answer for this one machine |
| 2 | cloud-init's merged document | Clouds and hypervisors |
| 3 | `corium.config=` on the kernel command line | PXE and netboot |
| 4 | `/usr/share/corium/config.yaml` | A default baked into a derived image |

Source 2 reads `/var/lib/cloud/instance/cloud-config.txt`, the document
cloud-init has already merged, so multipart payloads and vendor-data are
resolved before Corium sees them.

Source 3 accepts a path or an `https://` URL:

```
corium.config=/run/media/config.yaml
corium.config=https://boot.example.com/nodes/edge-01.yaml
```

The last occurrence on the command line wins, matching the kernel's own
handling, so a value appended at boot overrides one baked into the bootloader.
Quoted values are honoured, so a path containing a space survives. A fetched
configuration times out after 30 seconds and is capped at 1 MiB.

An **empty file is treated as absent**, not as an empty configuration. A
zero-byte file would otherwise boot a node with no role at all.

A source that fails for **any reason other than being absent stops the search**.
Falling through to a baked-in default when the intended configuration is merely
unreachable is how a node silently joins the wrong cluster.

If no source answers, the node boots as an ordinary machine and says so in the
journal. That is a supported outcome, not an error.

### Two document shapes

The same schema arrives by two routes, told apart by which key is present.

**Embedded**, under `corium:` in a cloud-config, alongside cloud-init's own keys:

```yaml
#cloud-config
corium:
  role: single
users:
  - name: core
```

**Standalone**, the schema alone at the top level — what you write in
`/etc/corium/config.yaml` or serve over PXE, where a cloud-config wrapper would
be ceremony for its own sake:

```yaml
role: single
cluster:
  name: lab
```

A document containing both is read as embedded: a top-level `role:` in a
cloud-config is far more likely to belong to another tool.

Unknown keys are handled differently on each side of the boundary. At the top
level of a cloud-config they belong to cloud-init and are left alone. **Inside
the Corium schema they are rejected**, because there they are typos — and a
silently ignored key means a setting you carefully wrote never took effect.

---

## 2. How it becomes a running node

```
  source chain ──▶ parse ──▶ defaults ──▶ validate ──▶ hostname
                                                          │
                       k0s service ◀── k0s install ◀── render
```

| Step | What happens |
|---|---|
| **Parse** | Locate the schema in the document and decode it strictly |
| **Defaults** | Fill unset fields (§3.12). Idempotent |
| **Validate** | Report **every** problem at once, offline |
| **Hostname** | Settle the node's name before anything reads it (§4) |
| **Secrets** | Resolve `tokenFrom` / `authPassFrom` / `operatorCAFrom` |
| **Claim** | In maintenance mode, wait here until an operator enrols the node (§3.11) |
| **Render** | Produce `/etc/k0s/k0s.yaml`, then apply `k0s.patch` |
| **Install** | `k0s install …`, then start the service |
| **Mark** | Write `/var/lib/corium/bootstrapped` |

Three properties of this pipeline are load-bearing:

**Validation is offline and exhaustive.** It performs no network or filesystem
access, so `corium-agent validate` gives the same answer on your workstation as
on the node. It reports every problem in one pass, so you do not discover your
mistakes one reboot at a time.

**Rendering is deterministic.** The same input produces a byte-identical
`k0s.yaml`; map keys are sorted rather than left to Go's randomised iteration
order. This matters for HA, where every controller must render the same file —
the virtual IP, router ID and VRRP password are a shared agreement, and a
disagreement means two controllers claiming one address.

**Bootstrap is idempotent.** The marker file is written last, and
`corium-bootstrap.service` does not start when it exists. A failure part-way
leaves the node unmarked, so the next boot retries from a known point rather
than resuming into an unknown one. Re-bootstrapping a node that already joined a
cluster destroys data, so this check is deliberate.

### What lands on the node

| Path | Contents |
|---|---|
| `/etc/k0s/k0s.yaml` | Rendered cluster configuration, mode `0600`. Controllers only |
| `/etc/k0s/join-token` | Resolved join token, mode `0600` |
| `/var/lib/corium/bootstrapped` | Marker; its presence means "already done" |
| `/etc/systemd/system/k0scontroller.service` | Written by `k0s install` |

Workers get no `k0s.yaml`: they take their configuration from the control plane
they join.

Secrets are never logged. Tokens and passwords are written with mode `0600` and
referred to indirectly in the journal.

---

## 3. The schema

### 3.1 Root

| Key | Type | Required | Notes |
|---|---|---|---|
| `role` | enum | **yes** | The only required field |
| `cluster` | object | no | Identity and reachability (§3.2) |
| `network` | object | no | Addressing and CNI (§3.3) |
| `storage` | object | no | Datastore (§3.4) |
| `join` | object | conditional | Required for `worker` (§3.5) |
| `node` | object | no | Node attributes (§3.6) |
| `addons` | list | no | Helm charts (§3.7) |
| `ha` | object | no | Control plane load balancing (§3.8) |
| `upgrades` | object | no | Unattended upgrades (§3.9) |
| `raid` | list | no | Software RAID on spare disks (§3.10) |
| `api` | object | no | The management API, off by default (§3.11) |
| `k0s` | object | no | Escape hatch (§3.13) |

### `role`

| Value | Control plane | Workloads | Notes |
|---|---|---|---|
| `single` | yes | yes | Self-contained. **Cannot gain nodes later**: k0s provisions it with SQLite and without the machinery multi-node clusters need |
| `controller` | yes | no | Runs no kubelet, so nothing schedules on it at all |
| `controller+worker` | yes | yes | Expandable. Corium passes `--no-taints`, without which the node would schedule nothing and look broken |
| `worker` | no | yes | Requires `join` |

Controllers carry the labels `node-role.kubernetes.io/control-plane=true` and
`node.k0sproject.io/role=control-plane`, which is how you tell them apart in
`kubectl get nodes`.

Without `--no-taints`, k0s taints a `controller+worker` node
`node-role.kubernetes.io/control-plane:NoSchedule`. Corium always passes the
flag, because a role named "controller+worker" that schedules nothing is a
trap. Add the taint back through `node.taints` if you want it.

`single` is the one irreversible choice in the schema. Everything else can be
changed by reprovisioning a node; a single-node cluster has to be rebuilt to
become anything else. Use `controller+worker` if you might ever add a machine —
it costs nothing today and keeps the door open.

`single` also turns off more than storage: k0s disables konnectivity and
refuses control plane load balancing outright in this mode. Corium rejects
`ha.enabled` with `role: single` for the same reason, before the node boots
rather than after.

See [k0s: configuration](https://docs.k0sproject.io/stable/configuration/).

### 3.2 `cluster`

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | `corium` | Cosmetic, but reaches generated kubeconfig contexts |
| `endpoint` | string | — | The address clients and joining nodes use. Added to the API certificate automatically |
| `subjectAltNames` | list | — | Additional names in the certificate |

`endpoint` becomes `spec.api.externalAddress`. Set it to the load balancer, the
HA virtual IP, or the sole controller's address — not to a specific controller
in an HA cluster, which defeats the point.

### 3.3 `network`

| Key | Type | Default | Notes |
|---|---|---|---|
| `podCIDR` | CIDR | `10.244.0.0/16` | |
| `serviceCIDR` | CIDR | `10.96.0.0/12` | Must not overlap `podCIDR` |
| `cni` | enum | `kuberouter` | `kuberouter`, `calico`, `custom` |

`cni: custom` installs nothing. The node stays `NotReady` and pods stay
`Pending` until you install a network — expected, not broken. See
[`examples/custom-cni.yaml`](examples/custom-cni.yaml).

kube-router is k0s's default and covers networking, network policy and service
proxying in a single component. Tuning any of the three, changing the
kube-proxy mode, or enabling dual-stack is done through `k0s.patch` rather than
the `corium:` schema — see [feature support](features.md#passthrough) and
[k0s: networking](https://docs.k0sproject.io/stable/networking/).

Overlapping CIDRs are rejected: they produce a cluster that comes up and then
misroutes traffic in ways that are miserable to diagnose.

### 3.4 `storage`

| Key | Type | Default | Notes |
|---|---|---|---|
| `type` | enum | role-dependent | `etcd` or `sqlite` |

Defaults to `sqlite` for `single`, `etcd` otherwise. `sqlite` is rejected with
`role: controller` because k0s treats a file-backed datastore as non-joinable:
a second controller does not error, it quietly runs as its own single-controller
cluster with its own state. Two machines that each believe they are the cluster
is a worse failure than a refusal, so Corium refuses.

Multiple controllers on kine are possible, but only against a network datastore
(MySQL, PostgreSQL). That is reachable through `k0s.patch`
(`spec.storage.kine.dataSource`), not through `storage.type`.

Corium's `sqlite` renders as k0s's `kine`, which is the mechanism; `sqlite` is
what you are actually choosing.

etcd runs embedded in the controllers; there is nothing to install. Use an odd
number of them — three tolerates one failure, five tolerates two. A fourth
controller adds no fault tolerance over three. Tuning etcd, or pointing k0s at
an external cluster, is reachable through `k0s.patch`. See
[k0s: configuration](https://docs.k0sproject.io/stable/configuration/).

### 3.5 `join`

| Key | Type | Notes |
|---|---|---|
| `token` | string | Inline. Convenient for labs, a liability in production |
| `tokenFrom` | object | Resolved at first boot (§3.14) |

Set exactly one. Required for `worker`; rejected for `single`, which bootstraps
its own cluster.

Mint tokens on an existing controller:

```bash
k0s token create --role=worker     --expiry=1h
k0s token create --role=controller --expiry=1h
```

A controller token is effectively a cluster-admin credential — whoever holds an
unexpired one can join a full control-plane member, with read and write access
to etcd. A worker token is narrower but still lets a machine join the cluster.
Prefer short expiries and a secret store over embedding either in instance
metadata.

Tokens can be listed and revoked on a controller with `k0s token list` and
`k0s token invalidate <id>`. See
[k0s: multi-node clusters](https://docs.k0sproject.io/stable/k0s-multi-node/).

### 3.6 `node`

| Key | Type | Notes |
|---|---|---|
| `name` | string | Hostname, and the name it registers under. Derived if unset (§4) |
| `labels` | map | Applied to the Node object |
| `taints` | list | `key`, `value`, `effect` |

`name` must be 63 characters or fewer, lowercase letters, digits and hyphens,
starting and ending with a letter or digit. `effect` must be `NoSchedule`,
`PreferNoSchedule` or `NoExecute`.

Labels are sorted before reaching the command line, so identical input produces
an identical command.

**Labels only take effect when the node first registers.** k0s passes them to
the kubelet, which applies them at registration and ignores them afterwards, so
editing `node.labels` and rebooting changes nothing — the node is already
registered and Corium will not bootstrap it twice. Change labels on a running
node with `kubectl label`, or reprovision it. The same applies to `node.taints`.

### 3.7 `addons`

Helm charts installed at bootstrap through k0s's Helm extensions. No Helm binary
and no in-cluster operator are involved.

| Key | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | Release name |
| `chart` | string | yes | Qualified, `repository/chart` |
| `version` | string | no | **Pin it** |
| `namespace` | string | no | Defaults to `default` |
| `repository` | object | conditional | `name` and `url` |
| `values` | map | no | Passed through unmodified |

Every repository referenced by a `chart` must be declared by some add-on in the
same document; a chart naming an undeclared repository is rejected.

Leaving `version` unset resolves to whatever is latest at boot, which makes a
node's outcome depend on *when* it booted.

Add-ons are rejected on workers: only a controller installs them, so declaring
them elsewhere expresses an intent that will never be carried out.

Charts are installed once at bootstrap. Corium does not model removing one:
deleting an add-on from the configuration of an already-bootstrapped node does
nothing, because the node will not bootstrap again. Remove a release by
deleting the k0s Chart resource it created:

```bash
kubectl delete chart <name> -n kube-system
```

See [k0s: Helm charts](https://docs.k0sproject.io/stable/helm-charts/).

### 3.8 `ha`

A highly available control plane without an external load balancer, and without
any PKI in the configuration. The controllers run VRRP between themselves and
one holds a virtual IP.

| Key | Type | Required | Notes |
|---|---|---|---|
| `enabled` | bool | — | |
| `virtualIP` | CIDR | yes | **With a prefix length**: keepalived needs it to add the address |
| `interface` | string | no | Defaults to the interface holding the default route |
| `virtualRouterID` | int | no | 1–255. Omit it and k0s assigns one starting at 51. Must be unique within the broadcast domain |
| `authPass` | string | yes | **Eight characters or fewer** |
| `authPassFrom` | object | — | Alternative to `authPass` (§3.14) |
| `unicastPeers` | list | no | The other controllers' addresses |

`authPass` is capped because **keepalived silently truncates it to eight
characters**. A longer value lets two controllers believe they share a password
they do not, so Corium rejects it rather than allowing that.

`unicastPeers` is required on any network without multicast, which includes most
clouds. It is harmless on a flat L2 segment.

The eight-character cap is not Corium being cautious: k0s itself rejects a
longer value with `AuthPass must be 8 characters or less`. Corium catches it
during validation instead, so the node never gets as far as failing to start.

If you put an external load balancer in front of the controllers instead of
using the virtual IP, it has to carry three ports to every controller: **6443**
(Kubernetes API), **8132** (konnectivity) and **9443** (the join API).

`cluster.endpoint` is required when HA is enabled, and should be the virtual IP:
without it, clients would be pointed at one controller and the VIP would buy
nothing.

HA is rejected for `single` (one node by definition) and for `worker` (no
control plane to balance). Settings given while `enabled` is false are also
rejected, since they would silently do nothing.

**Certificates never appear here.** Controllers two and three join with a token
and k0s ships them the cluster CA over its join API on port 9443. See
[`examples/ha-controller-first.yaml`](examples/ha-controller-first.yaml) and
[k0s: control plane load balancing](https://docs.k0sproject.io/stable/cplb/).

A related feature Corium does not model is **node-local load balancing**, which
runs a proxy on each worker so that kubelet and kube-proxy reach any healthy
controller without an external load balancer. It solves the problem for
workers, where CPLB solves it for clients. It is reachable through `k0s.patch`,
and k0s documents it as incompatible with `spec.api.externalAddress` — which
Corium sets from `cluster.endpoint`. See
[k0s: node-local load balancing](https://docs.k0sproject.io/stable/nllb/).

### 3.9 `upgrades`

| Key | Type | Default | Notes |
|---|---|---|---|
| `automatic` | enum | `none` | `none`, `download` or `apply` |
| `schedule` | string | `daily` | systemd `OnCalendar` expression |

Setting `schedule` also clears the randomised delay. The default daily run
carries up to an hour of jitter so a fleet does not hit the registry together;
an explicit schedule is a maintenance window, and moving it by up to an hour
would defeat the point of writing one.

`none` does nothing. `download` stages a newer image without rebooting, so the
reboot you schedule is near-instant. `apply` drains the node and then reboots
into the staged image, uncordoning once k0s is back.

A `schedule` with `automatic: none` is rejected: it would be a setting that
silently does nothing.

See [upgrades](upgrades.md#unattended-upgrades).

### 3.10 `raid`

A list of software RAID arrays, built from this node's **spare disks** at first
boot. It does not cover the disk the OS booted from — see
[software RAID](raid.md) for why, and for how to install onto a redundant root.

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | — | **Required.** Becomes `/dev/md/<name>` |
| `level` | int | — | **Required.** `0`, `1`, `5`, `6` or `10` |
| `devices` | list | — | **Required.** Whole disks, absolute paths |
| `spares` | list | — | Idle members pulled in when one fails |
| `filesystem` | enum | `ext4` | `ext4`, `xfs`, or `none` for a raw device |
| `mountPoint` | string | — | Absolute path; also written to `/etc/fstab` |
| `wipe` | bool | `false` | Consent to erasing devices that hold data |

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

Rejected at validation, before anything touches a disk: a level with too few
devices for it, a device claimed by two arrays, two arrays sharing a name, a
spare on a RAID 0, and a `mountPoint` on an array with `filesystem: none`.

**`wipe` is off by default and that is the point.** A device carrying a
filesystem, a partition table, or another array's metadata stops the bootstrap
with an error naming what it found. A node that refuses to boot is recoverable;
a disk that has been silently consumed is not.

Prefer `/dev/disk/by-id/...` over `/dev/sdb`. Kernel names are handed out in
discovery order, so on a first boot they can name a different disk than the one
you meant.

### 3.11 `api`

The node's management API, `corium-apid`. Off unless asked for, and covered in
full by [ADR 4](adr/0004-management-api.md).

> **Partly implemented.** The daemon runs, `cctl` claims a node and reports on
> it, and roles are enforced. Of the four management surfaces, node state is
> done (`cctl status`); services and journals, upgrades, and node lifecycle are
> not.

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Setting either key below implies `true`. False masks `corium-apid.service` |
| `operatorCA` | string | — | PEM certificate of the CA that signs operator client certificates |
| `operatorCAFrom` | object | — | Resolve it at first boot instead (§3.14) |

Set at most one of `operatorCA` and `operatorCAFrom`.

The value is a **certificate**, not a key. The node is never given the private
key that signs with it, which is why — unlike a join token — it is safe in
cloud-init in clear:

```yaml
corium:
  role: worker
  api:
    operatorCA: |
      -----BEGIN CERTIFICATE-----
      MIIBkTCB+6ADAgECAhRk...
      -----END CERTIFICATE-----
```

An inline CA is checked at validation time: it must be one PEM certificate, it
must be a CA, and it must not have expired. A private key pasted here is
rejected by name, because it means the key that owns your fleet has just been
written into a document that ends up in instance metadata — treat it as
compromised.

Writing `enabled: false` alongside either key is an error rather than a
precedence rule. The configuration is saying two contradictory things, and
guessing which one you meant would leave the other silently doing nothing.

#### Maintenance mode

`enabled: true` with no CA. The node reads its configuration, validates it, and
then **stops before bootstrapping k0s**, printing a single-use pairing code and
its certificate fingerprint to the console, the serial port and the journal:

```
Corium node is unenrolled and is not in a cluster.

  address       192.168.1.51:7443
  pairing code  K7QM-93XF
  fingerprint   SHA256:tQ2f...9c1a

  cctl enroll 192.168.1.51 --code K7QM-93XF
```

Enrolment pins the CA and releases the bootstrap, which proceeds from the
cloud-init configuration the node has been holding all along. It sends a CA
certificate and nothing else — it is not a way to configure a node.

Getting an operator CA, and a certificate to use it with:

```console
$ cctl pki init
Created an operator CA in /home/you/.corium
...
$ cctl pki issue --role admin
Signed a client certificate for "you" as corium:admin, valid for 2160h0m0s.
```

`pki init` prints the certificate ready to paste into cloud-init, which is all
mode A needs. The private key beside it never leaves your machine.

Then claim the node, using both values from its console:

```console
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...9c1a
Claimed 192.168.1.51:7443.

  node fingerprint  SHA256:tQ2f...9c1a
  operator CA       SHA256:HklllX2CnKCQpdebtG2MO8K+Ft0Hwnfbv4Ly5KdFVDk

The node is restarting to require your client certificate, and its
bootstrap is released: it will now join the cluster its cloud-init
configuration describes.
```

Leaving out `--fingerprint` shows what answered and asks you to confirm it
against the console. It is refused when there is nobody there to ask: a script
that confirms whatever answers has checked nothing while appearing to.

Afterwards the fingerprint is remembered, so later calls need no flags:

```console
$ cctl health 192.168.1.51
192.168.1.51:7443  ok  (authenticated as corium:admin)

$ cctl status 192.168.1.51
192.168.1.51:7443

  hostname     worker-01
  role         controller+worker
  cluster      prod

  os           Fedora Linux 44 (Cloud Edition)
  booted       ghcr.io/corium-os/corium:0.1
  digest       sha256:aaaa1111

  staged       ghcr.io/corium-os/corium:0.2
  digest       sha256:bbbb2222
  (the next reboot moves this node to the staged image)

  k0s          v1.31.2+k0s.0
  service      k0scontroller.service (running)
  greenboot    passed
  uptime       35h40m50s
```

A field it could not determine is left out rather than shown as a dash or a
zero. This is usually read just before doing something irreversible, and a
blank is honest where a placeholder invites a guess. `--json` prints the node's
reply verbatim.

`curl` works too, which is half the reason the API speaks JSON over HTTP:

```console
$ curl -k --cert ~/.corium/client.crt --key ~/.corium/client.key \
    https://192.168.1.51:7443/v1/health
{"status":"ok","role":"corium:admin"}
```

The `-k` is not a shortcut: the node signs its own certificate, because no
private key is ever carried in a configuration. The fingerprint is the check,
and `cctl` pins it for you.

Two consequences worth knowing before choosing this mode:

- **It is not zero touch.** Three nodes means three consoles. If you want
  unattended provisioning with nothing secret in the metadata, use
  `operatorCA` — the certificate is not a secret.
- **A node waiting to be claimed is in no cluster**, and the rule holds in both
  directions: a node cannot return to maintenance mode while it is a cluster
  member, so `cctl reset` takes it out of the cluster on the way.

Enrolment is recorded under `/var/lib/corium/api/` — the pinned CA at `0644`
because a certificate is not a secret, and the node's own serving key at `0600`
because that one is — and it survives reboots and upgrades. A node that has been claimed never falls back to maintenance mode on
its own, or power-cycling a machine would be enough to take it.

### 3.12 Defaults

| Field | Default |
|---|---|
| `cluster.name` | `corium` |
| `network.podCIDR` | `10.244.0.0/16` |
| `network.serviceCIDR` | `10.96.0.0/12` |
| `network.cni` | `kuberouter` |
| `storage.type` | `sqlite` for `single`, `etcd` otherwise |
| `addons[].namespace` | `default` |
| `node.name` | Derived from the machine ID (§4) |

Applying defaults is idempotent and never overwrites an explicit value.

### 3.13 `k0s.patch` — the escape hatch

A strategic merge patch applied to the rendered `k0s.yaml` **after** Corium has
finished, passed through without interpretation. Every k0s setting stays
reachable, including ones Corium has never heard of.

```yaml
corium:
  k0s:
    patch:
      spec:
        api:
          extraArgs:
            audit-log-path: /var/log/kubernetes/audit.log
```

Maps merge key by key; **every other type, including lists, is replaced
wholesale**. A patch that sets a list means that list, not that list appended to
whatever was there.

The patch can override values Corium computed, including ones it considers
load-bearing — that is what makes it an escape hatch rather than a suggestion.
Corium checks only that the result is valid YAML. A patch that breaks the
cluster is yours to own.

The second escape hatch is that the document remains an ordinary cloud-config:
`write_files`, `runcmd` and every other module keep working. Corium is a guest
in that document, not its owner.

### 3.14 Secret sources

Used by `join.tokenFrom`, `ha.authPassFrom` and `api.operatorCAFrom`, so a value
need not sit in instance metadata where anything reaching the metadata service
can read it.

| Key | Type | Notes |
|---|---|---|
| `url` | string | **Must be `https`** |
| `file` | string | Absolute path |
| `authFile` | string | File holding a bearer token for `url` |
| `waitFor` | duration | Retry until the secret appears, at most this long |

Set exactly one of `url` or `file`. Plain HTTP is rejected without an opt-out: a
token fetched over HTTP is a token handed to anyone on the path.

### Waiting for a secret

`waitFor` is what lets a cluster start all at once. A joining node can boot
before the node that mints its token has finished, wait, and join when the
token appears — instead of failing and needing an operator to sequence the
machines by hand.

```yaml
join:
  tokenFrom:
    url: https://secrets.example.com/corium/controller-token
    waitFor: 15m
```

Only **absence** is waited out — a missing file, a connection refused, or a
`404`, `408`, `425`, `429`, `502`, `503`, `504`. A rejected or malformed
request fails immediately: retrying a wrong credential for a quarter of an hour
helps nobody and hides the mistake.

Retries back off to 32 seconds and stop there. The maximum budget is one hour,
because a node still waiting after that is a node nobody is watching.

Omitting `waitFor` keeps the strict behaviour: one attempt, then fail.

Fetches time out after 30 seconds and read at most 256 KiB. Errors never quote
the response body, because the value being handled is a credential and a message
echoing it into the journal has leaked it.

---

## 4. How the hostname is settled

Kubernetes identifies a node by its hostname, and a duplicate does not fail
loudly: nodes take turns overwriting each other's Node object while everything
reports healthy.

| # | Source | Used when |
|---|---|---|
| 1 | `node.name` | Set explicitly |
| 2 | The current hostname | Something already set a real one |
| 3 | `corium-<machine-id[:8]>` | The hostname is still generic |

Generic means `fedora`, `localhost`, `localhost.localdomain` or empty — the
names an unconfigured image boots with, which carry no identity.

The derived name is **not random**. A random name would change on reboot,
registering a new node every time and leaving the old one behind as a ghost.
systemd generates the machine ID on first boot and the image ships none, so it
is unique per node and stable for its lifetime.

If you clone a disk *after* first boot, the machine ID comes with it. Clear
`/etc/machine-id` on the clone or set `node.name`.

The hostname is applied before k0s starts, since k0s registers the node under
whatever it reads at startup.

### Node address

With HA enabled, Corium pins the kubelet's `--node-ip` to the node's own
address, excluding the virtual IP.

Without this the kubelet may register the VIP, because it picks whatever it
finds on the interface — and the VIP belongs to whichever controller currently
wins the election. The failure is delayed: everything works until the first
failover, after which the node's advertised address belongs to a different
machine and logs, exec, port-forward and metrics all go to the wrong node.

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

```
k0s install controller --config /etc/k0s/k0s.yaml \
    --enable-worker --no-taints --labels pool=general
```

Worth noting in the output: `endpoint` appeared in `sans` without being asked
for, the patch merged into `spec.api` without disturbing its siblings, chart
values became the YAML string k0s expects, and telemetry is off by default —
Corium does not phone home, and neither do the clusters it builds.

Reproduce any of this without touching a machine:

```bash
corium-agent validate node.yaml
corium-agent bootstrap --dry-run --config node.yaml
```

`--dry-run` resolves no secrets and renames nothing: it reaches no further than
the process. Contacting a secret store to produce output nobody applies would be
both a surprise and, on a shared network, a leak of intent.
