# Installing Cilium

This page replaces a node's CNI with Cilium: what goes in its cloud-config, how
to check the install, and what changes if you drop kube-proxy as well. Corium
ships kube-router, because k0s does — one component covering networking,
network policy and service proxying — and swapping it out takes two decisions.

> **It applies to a node you are about to boot, not to a cluster already
> running.** `cni: custom` is rendered into `/etc/k0s/k0s.yaml` at bootstrap,
> so changing it after the fact means editing that file and restarting k0s.

**Single node:** start at [Configure the node](#configure-the-node).
**Multi-node:** read [Multi-node clusters](#multi-node-clusters) first — the
CNI is a cluster-wide decision, and declaring the chart on the controller only
is a prerequisite rather than a footnote.

Both decisions fit in the cloud-config the node already boots with. k0s must be
told to install nothing, so that two CNIs do not fight over the same node; and
something has to install Cilium. `cni: custom` handles the first, an entry
under `addons:` handles the second, and the node converges on its own without a
second provisioning step.

## Configure the node

```yaml
#cloud-config
corium:
  role: single

  network:
    podCIDR: 10.244.0.0/16
    cni: custom

  addons:
    - name: cilium
      chart: cilium/cilium
      version: 1.20.2
      namespace: kube-system
      repository:
        name: cilium
        url: https://helm.cilium.io/
      values:
        ipam:
          mode: cluster-pool
          operator:
            clusterPoolIPv4PodCIDRList:
              - 10.244.0.0/16
```

That is a complete node. [`examples/custom-cni.yaml`](examples/custom-cni.yaml)
is the same document with an SSH key, ready to boot.

Check it before booting anything:

```bash
corium-agent validate node.yaml
node.yaml: valid (role single, cluster corium)
```

## What `cni: custom` and `addons:` do

`cni: custom` renders `spec.network.provider: custom` into `/etc/k0s/k0s.yaml`,
and k0s then deploys no CNI at all. The node registers, comes up `NotReady`,
and stays there until Cilium is running. That intermediate state is expected
rather than a failed boot — see [troubleshooting](#troubleshooting) for how to
tell the two apart.

`addons:` renders `spec.extensions.helm`, which k0s turns into `Chart` resources
its controller reconciles itself, with no Helm binary and no in-cluster
operator. The install runs from the controller rather than from a pod, so it
works while the cluster still has no pod network — which is what makes a CNI
installable this way. Cilium's agent and operator both run on the host network,
so they do not need the network they are about to provide.

`corium-agent bootstrap --dry-run` shows what the two produce, without touching
the machine — trimmed here to the parts this page is about:

```yaml
# /etc/k0s/k0s.yaml
spec:
    extensions:
        helm:
            charts:
                - chartname: cilium/cilium
                  name: cilium
                  namespace: kube-system
                  values: |
                    ipam:
                        mode: cluster-pool
                        operator:
                            clusterPoolIPv4PodCIDRList:
                                - 10.244.0.0/16
                  version: 1.20.2
            repositories:
                - name: cilium
                  url: https://helm.cilium.io/
    network:
        podCIDR: 10.244.0.0/16
        provider: custom
        serviceCIDR: 10.96.0.0/12
```

The chart values are passed through untouched. Corium does not reconcile them
against the rest of the configuration, so `clusterPoolIPv4PodCIDRList` agreeing
with `network.podCIDR` is your job: disagree, and pods get addresses the cluster
does not route.

## Nothing to configure for the immutable filesystem

Two things that usually need work on a read-only OS are already handled:

- **`/opt/cni/bin` is writable.** The image points `/opt` at `/var/opt`, because
  the CNI DaemonSet installs its binary there and `mkdir /opt/cni:
  read-only file system` is otherwise the first thing you see. The chart's
  defaults — `/opt/cni/bin` and `/etc/cni/net.d` — are already where k0s looks,
  so `cni.binPath` and `cni.confPath` do not need setting.
- **The inotify limits are raised.** `cilium connectivity test` fails with *too
  many open files* on a stock host; `/usr/lib/sysctl.d/` ships the higher values
  in the image.

## Verify the install

```bash
k0s kubectl get charts -A                                 # did the chart install
k0s kubectl -n kube-system get pods -l k8s-app=cilium     # is the agent running
k0s kubectl get nodes                                     # Ready, once it is
```

A couple of minutes after boot, all three should hold:

- `get charts -A` lists `k0s-addon-chart-cilium` in `kube-system`. If it is
  missing, k0s never saw the add-on — check the `addons:` block.
- The Cilium agent pod is `Running` and `1/1` ready, one per node. `Pending`
  means nothing has scheduled it; `CrashLoopBackOff` means it started and
  failed, so read its logs.
- The node is `Ready`, within a minute or so of the agent reporting healthy.
  Until then `NotReady` is expected — nothing has installed a network yet.

`cilium status --wait`, if you install the
[Cilium CLI](https://github.com/cilium/cilium-cli/releases), says more.

## Without kube-proxy

Cilium can take over service handling entirely. That means two changes, and
both belong in the configuration the node first boots with — removing kube-proxy
from a cluster already running it is a different and less pleasant exercise.

```yaml
corium:
  role: controller+worker

  cluster:
    endpoint: 10.0.0.10

  network:
    cni: custom

  # kube-proxy has no corium: key. k0s.patch is applied verbatim to the
  # rendered k0s.yaml, so every k0s setting stays reachable.
  k0s:
    patch:
      spec:
        network:
          kubeProxy:
            disabled: true

  addons:
    - name: cilium
      chart: cilium/cilium
      version: 1.20.2
      namespace: kube-system
      repository:
        name: cilium
        url: https://helm.cilium.io/
      values:
        kubeProxyReplacement: true
        k8sServiceHost: 10.0.0.10
        k8sServicePort: 6443
```

`k8sServiceHost` is not optional here. Cilium has to reach the API server to
load its own configuration, and the in-cluster `kubernetes` Service it would
normally use is a ClusterIP that nothing is translating yet — kube-proxy is
gone, and Cilium is what replaces it. Give it an address that works without a
cluster network: a controller's own address, or the HA virtual IP.

## Multi-node clusters

The CNI is a cluster-wide decision, made by the control plane:

- **Declare the chart on the controller only.** A worker that declares `addons:`
  is rejected at validation — `addons: only a controller installs add-ons, but
  role is "worker"` — rather than silently doing nothing.
- **`network:` belongs on the controller too.** The k0s cluster configuration is
  written on controllers only, so a worker's `cni:` value is never rendered.
- **Workers need nothing.** A worker joining later is `NotReady` until the
  DaemonSet lands on it, then `Ready`. There is no per-node step.

## Troubleshooting

**The node is `NotReady` and there are no Cilium pods.** The chart never
installed. `k0s kubectl get charts -A` shows what k0s thinks it did, and
`journalctl -u k0scontroller | grep -i helm` says why it did not. A version that
does not exist is the usual cause; a node with no route to `helm.cilium.io` is
the second, since the repository is fetched at install time.

**Cilium crash-loops with `Unable to contact k8s api-server`.** With
`kubeProxyReplacement: true`, this is a missing or wrong `k8sServiceHost` — the
agent is trying to reach the API server through a Service IP nothing is
translating.

**Pods get addresses the cluster does not route.** `clusterPoolIPv4PodCIDRList`
and `network.podCIDR` disagree. Nothing checks this for you.

**Everything is fine, but you expected kube-router.** `cni: custom` is sticky:
it is rendered into `/etc/k0s/k0s.yaml` at bootstrap, and changing it after the
fact means editing that file and restarting k0s, not rebooting.

## Other CNIs

Nothing above is specific to Cilium except the values. `cni: custom` plus an
add-on is how any chart-installed CNI goes on, and the same two failure modes —
a chart that never installs, a pod CIDR that disagrees — are the ones to look
for. Calico is the exception: it has a value of its own, `cni: calico`, and k0s
installs it for you.

Every field used here is described in the
[configuration reference](reference.md#34-network), and what else `k0s.patch`
reaches is in [feature support](features.md#passthrough).
