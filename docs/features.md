# Feature support

What Corium models, what it passes through, and what it deliberately does not do.

This page exists so you can tell, before committing to Corium, whether it will
get in your way. The short answer: Corium models the settings most clusters
need, and **everything else in k0s stays reachable** through a passthrough
patch. There is no feature you can configure in k0s that Corium prevents you
from configuring — only features it does not give a shorter name to.

Anything describing k0s behaviour here links to the
[official k0s documentation](https://docs.k0sproject.io/). Corium does not fork
or patch k0s; when the two disagree, k0s is right and this page is stale.

---

## Three levels of support

| Level | Meaning |
|---|---|
| **Modelled** | A first-class field in the `corium:` schema. Validated, with defaults, and documented in the [configuration reference](reference.md) |
| **Passthrough** | Not modelled, but reachable verbatim through `k0s.patch`. Corium neither validates nor interferes |
| **Out of scope** | Deliberately not supported. Each has a reason below |

Passthrough is not a lesser tier for things that were forgotten. It is where
settings belong when they are rarely needed, cluster-specific, or change faster
upstream than a schema should. A modelled field is a promise to support that
spelling forever; that promise is expensive, so it is made sparingly.

---

## Modelled

### Cluster shape

| Feature | Field | Notes |
|---|---|---|
| Single-node cluster | `role: single` | Implies SQLite storage; cannot gain nodes later |
| Control plane only | `role: controller` | Runs no workloads |
| Control plane plus workloads | `role: controller+worker` | Corium passes `--no-taints`, so the node actually schedules |
| Worker | `role: worker` | Requires a join token |
| Cluster name | `cluster.name` | Reaches generated kubeconfig contexts |
| API endpoint | `cluster.endpoint` | Becomes `spec.api.externalAddress`, and is added to the certificate |
| Extra certificate names | `cluster.subjectAltNames` | Appended to `spec.api.sans` |

### Networking

| Feature | Field | Notes |
|---|---|---|
| Pod address range | `network.podCIDR` | Default `10.244.0.0/16`, matching k0s |
| Service address range | `network.serviceCIDR` | Default `10.96.0.0/12`, matching k0s |
| kube-router | `network.cni: kuberouter` | k0s's default: networking, policy and service proxy in one component |
| Calico | `network.cni: calico` | |
| Bring your own CNI | `network.cni: custom` | k0s deploys nothing; the node stays `NotReady` until you install one |

### Storage

| Feature | Field | Notes |
|---|---|---|
| Embedded etcd | `storage.type: etcd` | Default for every multi-node role |
| SQLite | `storage.type: sqlite` | Rendered as k0s's `kine`. Single controller only, and Corium rejects it with `role: controller` |

### High availability

| Feature | Field | Notes |
|---|---|---|
| Virtual IP for the control plane | `ha.enabled`, `ha.virtualIP` | k0s [control plane load balancing](https://docs.k0sproject.io/stable/cplb/) with keepalived. No external load balancer needed |
| VRRP group | `ha.virtualRouterID` | Must be unique in the broadcast domain |
| VRRP password | `ha.authPass`, `ha.authPassFrom` | Capped at eight characters, because keepalived silently truncates beyond that |
| Interface | `ha.interface` | Defaults to the one holding the default route |
| Non-multicast networks | `ha.unicastPeers` | Required on most clouds |

### Nodes

| Feature | Field | Notes |
|---|---|---|
| Node name | `node.name` | Derived from the machine ID when unset |
| Labels | `node.labels` | Sorted before reaching the command line, so identical input gives an identical command |
| Taints | `node.taints` | `key`, `value`, `effect` |
| Node address | — | Set automatically with HA, excluding the virtual IP |

### Joining

| Feature | Field | Notes |
|---|---|---|
| Inline token | `join.token` | |
| Token from a URL or file | `join.tokenFrom` | HTTPS only |
| Waiting for a token to appear | `join.tokenFrom.waitFor` | Lets every node start at once instead of in sequence |
| Bearer auth for that URL | `join.tokenFrom.authFile` | |

### Add-ons

| Feature | Field | Notes |
|---|---|---|
| Helm charts at bootstrap | `addons[]` | Rendered into [`spec.extensions.helm`](https://docs.k0sproject.io/stable/helm-charts/). No Helm binary, no in-cluster operator |
| Chart repositories | `addons[].repository` | |
| Chart values | `addons[].values` | Converted to the YAML string k0s expects |

### Upgrades

| Feature | Field | Notes |
|---|---|---|
| Unattended staging | `upgrades.automatic: download` | Stages a new image, never reboots on its own |
| Unattended reboot | `upgrades.automatic: apply` | Reboots without draining; for labs and single nodes |
| Check schedule | `upgrades.schedule` | systemd `OnCalendar`, default daily |
| Version ladder | — | `1.4.2`, `1.4`, `1`, `latest` published per release |

### Where configuration comes from

| Feature | Notes |
|---|---|
| cloud-init | NoCloud, ConfigDrive, OpenStack, EC2, Azure, GCE, Hetzner, VMware, OVF |
| A file on the machine | `/etc/corium/config.yaml` |
| The kernel command line | `corium.config=<url or path>`, for PXE |
| An image default | `/usr/share/corium/config.yaml` |

---

## Passthrough

Everything else k0s exposes is set through `k0s.patch`, applied verbatim to the
rendered `k0s.yaml` after Corium is done. Maps merge key by key; lists are
replaced wholesale.

This is verified, not asserted — the example below renders with every field
intact alongside the values Corium computed:

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

One passthrough field is worth avoiding: `spec.extensions.storage` is a
deprecated no-op from k0s 1.31 onwards. It still parses, so a configuration
using it looks accepted and does nothing. Install storage as an add-on instead.

A patch can also override anything Corium computed, including values it treats
as load-bearing. That is what makes it an escape hatch rather than a
suggestion. Corium checks only that the result is valid YAML.

The second escape hatch is that your document stays an ordinary cloud-config:
`write_files`, `runcmd`, `users` and every other module keep working. Corium is
a guest in that document, not its owner.

---

## Static configuration, not dynamic

k0s can run in two modes. By default it reads `k0s.yaml` from disk on every
controller. With `--enable-dynamic-config` it instead stores a `ClusterConfig`
resource in the cluster, and controllers reconcile against that.

**Corium writes a static configuration file.** It does not pass
`--enable-dynamic-config`, which means:

- Changing the configuration means reprovisioning the node, in keeping with the
  rest of the design: a node is rebuilt from an image, not adjusted in place.
- Mixing controllers that use dynamic config with ones that do not is a
  documented conflict in k0s. If you enable it, enable it everywhere.

If you want dynamic configuration, three parts of the configuration stay
file-local and per-controller regardless — `spec.api`, `spec.storage`, and
`spec.network.controlPlaneLoadBalancing` — so they must still be identical
across controllers. That is precisely what Corium's rendering already
guarantees, since every controller in an HA cluster renders the same file.

See [k0s: dynamic configuration](https://docs.k0sproject.io/stable/dynamic-configuration/).

---

## Out of scope

These are decisions, not gaps. Each would be straightforward to add; none
should be.

| Not supported | Why |
|---|---|
| **k0s Autopilot** | Autopilot upgrades Kubernetes by replacing the k0s binary on disk. Corium's binary lives in the read-only `/usr`, because Kubernetes ships with the OS. Two upgrade mechanisms, each able to move the version independently, is a worse position than one |
| **Fleet management** | Corium provisions a node and stops. It does not track, group or reconcile machines. That is a control plane's job, and there are good ones |
| **A bespoke configuration API** | The value of cloud-init is that every cloud, hypervisor and PXE setup already speaks it |
| **Forking or patching k0s** | Corium configures upstream k0s. A fork would mean owning Kubernetes bugs, which is not a business worth being in |
| **Removing add-ons** | k0s's Helm extensions install charts; Corium does not model uninstalling one. Deleting a chart from the configuration leaves the release in place — remove it with `kubectl delete chart <name> -n kube-system`, per [k0s: Helm charts](https://docs.k0sproject.io/stable/helm-charts/) |
| **Multiple Kubernetes distributions** | Only k0s. Supporting k3s or RKE2 as well would mean an abstraction that fits none of them properly |

### Not supported yet

Honest about the difference: these are absent because nobody has needed them
enough, not because they are wrong.

| Missing | Notes |
|---|---|
| **Health-gated rollback** | A node that boots a broken image stays broken. Attempted with greenboot and reverted: it rolled nodes back on every reboot, health check passing or not |
| **Air-gapped bundles** | k0s supports [air-gap installs](https://docs.k0sproject.io/stable/airgap-install/) by dropping an image bundle in `<data-dir>/images/`. Corium can already bake one into the image with a `COPY`, but there is no `corium:` field for it and it is untested |
| **Worker profiles as a modelled field** | Reachable through the patch today |
| **Uninstalling or resetting a node** | `k0s reset` exists; Corium does not wrap it. On an image-based OS, reprovisioning is usually the better answer |

---

## Defaults

Corium's defaults match k0s's wherever k0s has one, so that a Corium node and a
hand-configured k0s node agree unless there is a reason to differ.

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

The one deliberate difference is the cluster name, which only affects generated
kubeconfig contexts.

Values k0s sets that Corium does not touch — API port `6443`, join API port
`9443`, konnectivity ports `8132`/`8133`, cluster domain `cluster.local`,
certificate lifetimes — keep their k0s defaults. Run `k0s config create` on a
node to see the full set as your version defines it.
