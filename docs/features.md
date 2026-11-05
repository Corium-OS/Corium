# Feature support

This page lists what Corium models, what it passes through, and what it does
not do on purpose.

Read this page before you commit to Corium, to find out if Corium will get in
your way. The short answer: Corium models the settings that most clusters
need, and **everything else in k0s stays reachable** through a passthrough
patch. Corium does not prevent you from configuring any k0s feature. It only
gives a shorter name to some of them and not to others.

Each description of k0s behavior on this page links to the
[official k0s documentation](https://docs.k0sproject.io/). Corium does not fork
or patch k0s. If the two disagree, k0s is right and this page is out of date.

---

## Three levels of support

| Level | Meaning |
|---|---|
| **Modelled** | A first-class field in the `corium:` schema. Corium validates it, gives it a default, and documents it in the [configuration reference](reference.md) |
| **Passthrough** | Not modelled, but reachable verbatim through `k0s.patch`. Corium does not validate it or change it |
| **Out of scope** | Not supported, on purpose. Each one has a reason below |

Passthrough is not a lower tier for forgotten settings. See
[why a setting ends up there](#passthrough).

---

## Modelled

### Cluster shape

| Feature | Field | Notes |
|---|---|---|
| Single-node cluster | `role: single` | Implies SQLite storage. You cannot add nodes later |
| Control plane only | `role: controller` | Runs no workloads |
| Control plane plus workloads | `role: controller+worker` | Corium passes `--no-taints`, so the node runs workloads |
| Worker | `role: worker` | Requires a join token |
| Cluster name | `cluster.name` | Appears in the generated kubeconfig contexts |
| API endpoint | `cluster.endpoint` | Becomes `spec.api.externalAddress`. Corium also adds it to the certificate |
| Extra certificate names | `cluster.subjectAltNames` | Appended to `spec.api.sans` |

### Networking

| Feature | Field | Notes |
|---|---|---|
| Pod address range | `network.podCIDR` | Default `10.244.0.0/16`, matching k0s |
| Service address range | `network.serviceCIDR` | Default `10.96.0.0/12`, matching k0s |
| kube-router | `network.cni: kuberouter` | The k0s default: networking, policy, and service proxy in one component |
| Calico | `network.cni: calico` | |
| Bring your own CNI | `network.cni: custom` | k0s deploys nothing. The node stays `NotReady` until you install a CNI. [Worked example with Cilium](cilium.md) |

### Storage

| Feature | Field | Notes |
|---|---|---|
| Embedded etcd | `storage.type: etcd` | Default for every multi-node role |
| SQLite | `storage.type: sqlite` | Rendered as the k0s `kine`. For a single controller only. Corium rejects it with `role: controller` |

### High availability

| Feature | Field | Notes |
|---|---|---|
| Virtual IP for the control plane | `ha.enabled`, `ha.virtualIP` | k0s [control plane load balancing](https://docs.k0sproject.io/stable/cplb/) with keepalived. No external load balancer needed |
| VRRP group | `ha.virtualRouterID` | Must be unique in the broadcast domain |
| VRRP password | `ha.authPass`, `ha.authPassFrom` | Eight characters at most, because keepalived cuts longer passwords without a warning |
| Interface | `ha.interface` | Defaults to the interface that holds the default route |
| Non-multicast networks | `ha.unicastPeers` | Required on most clouds |

### Nodes

| Feature | Field | Notes |
|---|---|---|
| Node name | `node.name` | Derived from the machine ID when unset |
| Labels | `node.labels` | Corium sorts them before they reach the command line, so identical input gives an identical command |
| Taints | `node.taints` | `key`, `value`, `effect` |
| Node address | `wireguard[].nodeAddress` | Set automatically with HA (without the virtual IP), or pinned to a WireGuard overlay interface |

### Joining

| Feature | Field | Notes |
|---|---|---|
| Inline token | `join.token` | Or minted and rendered for you by `cctl worker-config` |
| Token from a URL or file | `join.tokenFrom` | HTTPS only |
| Waiting for a token to appear | `join.tokenFrom.waitFor` | Every node can start at the same time instead of one after another |
| Bearer auth for that URL | `join.tokenFrom.authFile` | |

### Add-ons

| Feature | Field | Notes |
|---|---|---|
| Helm charts at bootstrap | `addons[]` | Rendered into [`spec.extensions.helm`](https://docs.k0sproject.io/stable/helm-charts/). No Helm binary and no in-cluster operator |
| Chart repositories | `addons[].repository` | |
| Chart values | `addons[].values` | Converted to the YAML string k0s expects |

### Bundled manifests

| Feature | Field | Notes |
|---|---|---|
| Plain Kubernetes YAML at bootstrap | `manifests[]` | Written to `/var/lib/k0s/manifests/<stack>/` before k0s starts. Use it for objects that have no chart. See [Manifest Deployer](https://docs.k0sproject.io/stable/manifests/) |
| Several files, several documents | `manifests[].files` | One file can hold documents separated by `---`, as kubectl accepts them |
| Checked offline | `manifests[].files[].content` | `corium-agent validate` parses it as YAML. Corium has no schema for a Kubernetes object and does not claim to have one |
| `.yml` refused | n/a | The deployer reads only the `.yaml` extension and skips a `.yml` file without a warning. Corium rejects `.yml` instead |
| Reconciling and pruning | n/a | Done by k0s, not by Corium. k0s watches each stack and prunes the resources of a file that you remove |

### Upgrades

| Feature | Field | Notes |
|---|---|---|
| Unattended staging | `upgrades.automatic: download` | Stages a new image, and never reboots on its own |
| Unattended reboot | `upgrades.automatic: apply` | Drains the node, reboots, and uncordons. If a PDB refuses the drain, the upgrade is cancelled |
| Check schedule | `upgrades.schedule` | systemd `OnCalendar`, default daily |
| Version ladder | n/a | `0.5.0`, `0.5` and `latest` per release. No major rung below 1.0, because a 0.x minor can break things. See [upgrades](upgrades.md#choosing-what-to-track) |
| Automatic rollback on failure | n/a | greenboot health check. A node that cannot run k0s returns to its previous image |

### Backups

| Feature | Field | Notes |
|---|---|---|
| Scheduled control plane snapshot | `backup.enabled` | A systemd timer that runs [`k0s backup`](https://docs.k0sproject.io/stable/backup/): etcd or kine, the PKI, `k0s.yaml`, manifests, and Helm configuration. For controllers only. Corium rejects it on a worker |
| Schedule | `backup.schedule` | systemd `OnCalendar`, default daily |
| Target directory | `backup.path` | Default `/var/lib/corium/backups`. Corium writes archives with mode `0600` in a `0700` directory |
| Retention | `backup.keep` | Default 7. Deletes only the archives that Corium named, never a glob |
| Restore | n/a | Manual. An operator decides to run `k0s restore` on a stopped node, not a timer. See [reference §3.18](reference.md#318-backup) |
| Workload data | n/a | Out of scope. A `k0s backup` holds the control plane, not PersistentVolumes |

### Kubernetes version

| Feature | Field | Notes |
|---|---|---|
| Pin a Kubernetes version | `kubernetes.version` | Independent of the OS image. A signed system extension overlays `/usr/bin/k0s`. If empty, the node runs the version that the image ships. See [ADR 10](adr/0010-kubernetes-version-axis.md) |
| Pull from an internal registry | `kubernetes.mirror` | For a site that cannot reach the default registry. A mirror changes where an artefact comes from. The artefact must still be signed |
| Supported window | n/a | Three Kubernetes minors per image, listed in `/usr/lib/corium/k0s.window`. To get a different minor, upgrade the OS |
| Skew enforcement | n/a | One minor at a time, and no move back to an older minor. These are the k0s rules. Corium refuses a change that breaks them, names the rule, and stops nothing |
| Rolling a version back | n/a | Not free, as an OS rollback is. k0s does not support a downgrade of a minor, so a revert is a second forward move |

### Disks

| Feature | Field | Notes |
|---|---|---|
| Software RAID on spare disks | `raid[]` | Levels 0, 1, 5, 6, 10. Created before k0s starts |
| Hot spares | `raid[].spares` | Added automatically when a member fails |
| Filesystem | `raid[].filesystem` | `ext4` (default), `xfs`, or `none` for a raw device |
| Mount and persist | `raid[].mountPoint` | Written to `/etc/fstab` by UUID |
| Refuses to destroy data | `raid[].wipe` | Off by default. A device that holds data stops the bootstrap |
| Root filesystem on RAID | n/a | Works, but only at install time. It needs a hand-written Kickstart, a second ESP, and an fstab edit. See [software RAID](raid.md) |
| ZFS pools on data disks | `zfs[]` | Mirror or raidz, created before k0s. Needs the ZFS image variant. See [ZFS on data disks](zfs.md) |
| ZFS datasets | `zfs[].datasets` | Per-dataset mount points and properties (compression, recordsize, quota) |
| ZFS refuses to destroy data | `zfs[].wipe` | Off by default. A device in a pool cannot also be in a `raid[]` array |
| Root filesystem on ZFS | n/a | Out of scope. It needs the module in the initramfs, and bootc has no declarative way to put it there. See [ADR 7](adr/0007-zfs-data-disks.md) |

### Overlay networking

| Feature | Field | Notes |
|---|---|---|
| Host WireGuard interface | `wireguard[]` | Started before k0s, so that a cluster can run over an encrypted overlay between hosts. See [ADR 6](adr/0006-host-wireguard-overlay.md) |
| Overlay as the node address | `wireguard[].nodeAddress` | The kubelet registers this address, not the physical NIC. This field makes the overlay the transport for the cluster |
| Peers | `wireguard[].peers` | Public key, endpoint, allowed IPs, keepalive, and an optional preshared key |
| Private key as a secret | `wireguard[].privateKeyFrom` | Resolved at first boot, written with mode `0600`, and never logged, as for a join token |
| k0s waits for the overlay | n/a | The k0s service requires the interface. If the overlay fails, the node does not half-join |

#### Not the same as a CNI's encryption

This is a host concern. It is different from the pod-traffic encryption that a
CNI gives. The overlay lets hosts on different sites or providers reach each
other at all. `wireguard-tools` is in the image but does nothing until you
declare an interface.

### Management API

Off unless you ask for it. The decision is [ADR 4](adr/0004-management-api.md).
The commands are in [cctl](cli.md).

#### Schema fields

| Feature | Field | Notes |
|---|---|---|
| The API itself | `api.enabled` | On by default. `enabled: false` is the only way to run no daemon and bind no port. A bootstrapped node that names no operator CA also binds no port |
| Operator CA, inline | `api.operatorCA` | The certificate of the CA that signs operator client certificates. It is public, so it is safe in clear text in cloud-init |
| Operator CA, resolved | `api.operatorCAFrom` | The same `SecretSource` as `join.tokenFrom`, `waitFor` included |
| Maintenance mode | `api.enabled: true`, no CA | The node holds its bootstrap and prints a pairing code on the console until an operator claims it |
| Open enrolment | `api.insecure` | Removes the pairing code. The first client that reaches the node claims it |

`api.insecure` is for test benches and controlled provisioning networks. The
node shows this on its console, and records that its claim was not
authenticated.

A node in maintenance mode is not a cluster member. It validates its
configuration and waits. It does not join first and then wait for a claim.
Enrolment carries no configuration. It sends a CA certificate and nothing
else. The role and the join token of the node still come from cloud-init.

#### How the daemon works

| Property | Notes |
|---|---|
| Transport | JSON over HTTP and mutual TLS on `7443`. It uses only the standard library, so no dependency ships to a node for it |
| Roles | Carried in the organization field of the client certificate: `corium:readonly`, `corium:operator`, `corium:admin`. Every route names the lowest role that can call it |
| Image validation | A node refuses an image that its own signing policy accepts without a signature. This is not a label check, because anyone can forge a label |
| SELinux | The daemon runs unconfined, as the agent does. `/var/lib/corium/api` has its own type, so that a confined domain has an anchor. The parent keeps `var_lib_t` because greenboot and systemd read it |

#### What it can be asked to do

These are commands, not schema fields. The syntax, flags, and output are in
[cctl](cli.md).

| Command | What it does |
|---|---|
| `cctl status` | Role, cluster, booted and staged image digests, kernel, k0s version and service, greenboot result, and uptime. Read-only |
| `cctl services` | Lists the units that the API knows about. Units come from a fixed list. The caller cannot pass a unit name through |
| `cctl logs` | Journals, for one unit or for all of them, with `--since`, `--follow` and `--unit kernel`. Limits: 10000 records, and one hour of following |
| `cctl restart --unit k0sworker` | At `corium:operator`. You can read units that run once, for example the bootstrap, but you cannot restart them |
| `cctl upgrade <nodes...> --image` | One node at a time. It stops at the first node that does not come back on the digest that it received |
| `cctl rollback <node>` | Marks the previous image as the next to boot, and does not reboot |
| `cctl cordon` / `cctl drain` | At `corium:operator`, **on controllers only**. If a pod disruption budget refuses the drain, the command does not force it, and the node stays cordoned |
| `cctl reboot` / `cctl shutdown` | At `corium:admin`. The node answers before it goes down |
| `cctl kubeconfig <node>` | At `corium:admin`. Points at the virtual IP of the cluster, if there is one. `--server` overrides it |
| `cctl config <node>` | At `corium:admin`. Prints the `corium:` document that the node recorded when it applied it. `cctl apply` compares against the same baseline. It does not print the file in `/etc`, which somebody can edit later. The command tells you when the two disagree |
| `cctl worker-config <controller>` | At `corium:admin`, **on controllers only**. Creates a new worker token and prints the `corium:` block to paste into a cloud-config. `--name`, `--label` and `--expiry` fill in the block |
| `cctl reset --confirm <node name>` | In this order: leaves the cluster, erases the bootstrap, forgets its owner, and reboots unclaimed. If a staged image is waiting, the reboot uses it |
| `cctl ca rotate <nodes...> --to <dir>` | At `corium:admin`. A node refuses a CA unless the caller shows a certificate signed by it. Thus a rotation cannot lock you out |
| `cctl pki init`, `cctl pki issue` | Create the operator CA and sign client certificates. The CA key stays on your machine |
| `corium-agent api set-ca --file` | If you lose the operator CA key, run this as root on the node. Local only. The node stays a member of its cluster |

**Why `cctl drain` is controllers only.** A worker holds kubelet credentials,
and these cannot evict pods. To drain a worker, use `kubectl drain` with the
kubeconfig that `cctl kubeconfig` gets.

### Where configuration comes from

| Feature | Notes |
|---|---|
| cloud-init | NoCloud, ConfigDrive, OpenStack, EC2, Azure, GCE, Hetzner, VMware, OVF |
| A file on the machine | `/etc/corium/config.yaml` |
| The kernel command line | `corium.config=<url or path>`, for PXE |
| An image default | `/usr/share/corium/config.yaml` |

---

## Passthrough

You set everything else that k0s exposes through `k0s.patch`. Corium applies
the patch verbatim to the rendered `k0s.yaml` after it is done. It merges maps
key by key and replaces lists in full.

A setting goes here when few clusters need it, when it is specific to one
cluster, or when upstream changes it faster than a schema can follow. A
modelled field is a promise to support that spelling forever. That promise is
expensive, so Corium makes it only for a small number of settings.

This was tested. The example below renders with every field intact, next to
the values that Corium computed:

```yaml
corium:
  role: controller+worker
  k0s:
    patch:
      spec:
        network:
          kubeProxy:
            mode: ipvs            # or nftables, userspace, or disabled
          nodeLocalLoadBalancing:
            enabled: true         # Envoy on each worker, no external LB
            type: EnvoyProxy
          dualStack:
            enabled: true
            IPv6podCIDR: fd00::/108
        workerProfiles:
          - name: default
            values:
              maxPods: 150
        images:
          repository: registry.example.com/mirror
        featureGates:
          - name: SomeGate
            enabled: true
        konnectivity:
          agentPort: 8132
```

Commonly reached this way:

| Area | k0s configuration | Reference |
|---|---|---|
| kube-proxy mode, or disabling it | `spec.network.kubeProxy` | [networking](https://docs.k0sproject.io/stable/networking/) |
| Node-local load balancing | `spec.network.nodeLocalLoadBalancing` | [NLLB](https://docs.k0sproject.io/stable/nllb/) |
| IPv6 and dual-stack | `spec.network.dualStack` | [dual-stack](https://docs.k0sproject.io/stable/dual-stack/) |
| kube-router tuning | `spec.network.kuberouter` | [networking](https://docs.k0sproject.io/stable/networking/) |
| Calico tuning | `spec.network.calico` | [networking](https://docs.k0sproject.io/stable/networking/) |
| Cluster domain | `spec.network.clusterDomain` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| API server arguments and ports | `spec.api.extraArgs`, `port`, `k0sApiPort` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| Scheduler and controller-manager arguments | `spec.scheduler`, `spec.controllerManager` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| etcd tuning and external etcd | `spec.storage.etcd` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| Kubelet profiles | `spec.workerProfiles` | [worker profiles](https://docs.k0sproject.io/stable/worker-node-config/) |
| Registry mirroring | `spec.images` | [airgap](https://docs.k0sproject.io/stable/airgap-install/) |
| Feature gates | `spec.featureGates` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| Konnectivity ports | `spec.konnectivity` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| Certificate lifetimes | `spec.api.ca`, `spec.storage.etcd.ca` | [configuration](https://docs.k0sproject.io/stable/configuration/) |
| Multiple controllers on kine | `spec.storage.kine.dataSource` pointing at MySQL or PostgreSQL | [configuration](https://docs.k0sproject.io/stable/configuration/) |

### What a patch can and cannot do

**Avoid `spec.extensions.storage`.** From k0s 1.31, it is deprecated and does
nothing. It still parses, so a configuration that uses it looks accepted but
has no effect. Install storage as an add-on instead.

**A patch can override anything Corium computed**, including values that
Corium needs to work. This makes the patch an escape hatch and not only a
suggestion. Corium checks only that the result is valid YAML.

**Your document stays an ordinary cloud-config.** `write_files`, `runcmd`,
`users` and every other module continue to work. Corium is a guest in that
document, not its owner.

---

## Static configuration, not dynamic

k0s can run in two modes. By default, it reads `k0s.yaml` from disk on every
controller. With `--enable-dynamic-config`, it stores a `ClusterConfig`
resource in the cluster instead, and controllers reconcile against that
resource.

**Corium writes a static configuration file.** It does not pass
`--enable-dynamic-config`. This has two results:

- To change the configuration, you reprovision the node. This agrees with
  the rest of the design: you rebuild a node from an image, you do not adjust
  it in place.
- k0s documents a conflict when some controllers use dynamic configuration and
  others do not. If you enable it, enable it on all controllers.

If you use dynamic configuration, three parts of the configuration always stay
in a local file on each controller: `spec.api`, `spec.storage`, and
`spec.network.controlPlaneLoadBalancing`. Thus they must be identical on all
controllers. Corium already makes sure of this, because every controller in an
HA cluster renders the same file.

See [k0s: dynamic
configuration](https://docs.k0sproject.io/stable/dynamic-configuration/).

---

## Out of scope

These are decisions, not gaps. Each one is easy to add, but Corium must not
add any of them.

| Not supported | Why |
|---|---|
| **k0s Autopilot** | Autopilot upgrades Kubernetes by replacing the k0s binary on disk. The Corium binary is in the read-only `/usr`, because Kubernetes ships with the OS. Two upgrade mechanisms that can each change the version independently are worse than one |
| **Fleet management** | Corium provisions a node and stops. It does not track, group, or reconcile machines. That is the job of a control plane. See the note below |
| **A configuration API for machines in service** | Every cloud, hypervisor, and PXE setup already supports cloud-init. That is its value, and Corium does not replace it with a protocol of its own. `cctl apply` writes `/etc/corium/config.yaml`. This file is first in the source chain, and it exists for the machines that cloud-init never reaches. Thus `cctl apply` fills a slot that the design already reserved. It does not add a new authority. The part that the heading names stays out of scope: a node whose role, cluster, name, network, or disks can change under a running Kubernetes. A bootstrapped node re-applies only the safe subset, and refuses each of those fields by name. Nothing watches a document between one apply and the next. `cctl config` reads the record of a node but cannot change it. See [ADR 4](adr/0004-management-api.md) and [ADR 8](adr/0008-day-two-reconcile.md) |
| **Arbitrary command execution over the API** | There is no `exec` endpoint and no endpoint that accepts a command line. Such an endpoint makes the API an SSH with a worse client. Then no argument for a small API surface applies |
| **Forking or patching k0s** | Corium configures upstream k0s. With a fork, Corium owns the Kubernetes bugs, and that work has no value for the project |
| **Removing add-ons** | The k0s Helm extensions install charts. Corium does not model the removal of a chart. If you remove a chart from the configuration, the release continues to run. To remove it, run `kubectl delete chart <name> -n kube-system`, as [k0s: Helm charts](https://docs.k0sproject.io/stable/helm-charts/) describes. For the same reason, `cctl apply` refuses a day-two document that removes a chart. It does not report a removal that did not happen |
| **Multiple Kubernetes distributions** | Only k0s. Support for k3s or RKE2 too needs an abstraction that fits none of them correctly |

**On fleet management.** The management API does not change that answer. It
is one daemon per node that answers for that node. It has no registry, no
inventory, and no desired state. A rolling upgrade is a `cctl` loop over the
addresses that you supply.

**On removing add-ons.** If you delete a chart from the configuration, the
release stays in place. Thus `cctl apply` refuses a day-two document that
removes a chart. It does not report a removal that did not happen.

### Not supported yet

These features are absent because nobody needed them enough, not because they
are wrong.

| Missing | Notes |
|---|---|
| **Air-gapped bundles** | k0s supports [air-gap installs](https://docs.k0sproject.io/stable/airgap-install/) with an image bundle in `<data-dir>/images/`. You can already bake a bundle into the Corium image with a `COPY`. But there is no `corium:` field for it, and it is not tested |
| **Worker profiles as a modelled field** | Reachable through the patch |


---

## Defaults

Where k0s has a default, Corium uses the same default. Thus a Corium node and a
k0s node configured by hand agree, unless there is a reason to differ.

| Setting | Corium | k0s |
|---|---|---|
| `podCIDR` | `10.244.0.0/16` | same |
| `serviceCIDR` | `10.96.0.0/12` | same |
| CNI provider | `kuberouter` | same |
| Storage, multi-node | `etcd` | same |
| Storage, `role: single` | `sqlite` (kine) | implied by `--single` |
| Telemetry | off | off |
| Cluster name | `corium` | `k0s` |
| Add-on namespace | `default` | same |

The only intentional difference is the cluster name. It affects only the
generated kubeconfig contexts.

Some values that k0s sets keep their k0s defaults, because Corium does not
change them. These are the API port `6443`, the join API port `9443`, the
konnectivity ports `8132`/`8133`, the cluster domain `cluster.local`, and the
certificate lifetimes. To see the full set for your version, run
`k0s config create` on a node.
