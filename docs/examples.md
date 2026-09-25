# Configuration examples

Annotated cloud-config documents covering the common shapes a node takes. Every
one is a complete, working document: copy it, change the addresses and keys, and
boot.

They live in [`docs/examples/`](examples/) in the repository, and each carries
comments explaining not only what a field does but why it is there. The links
below open the files on GitHub.

Check any of them before booting anything:

```bash
corium-agent validate node.yaml
```

Validation is offline and reports every problem at once.

---

## Start here

| Example | What it builds |
|---|---|
| [`single-node.yaml`](examples/single-node.yaml) | The smallest useful node: a complete single-node cluster in a handful of lines |
| [`worker.yaml`](examples/worker.yaml) | A worker joining an existing cluster with a token |
| [`controller.yaml`](examples/controller.yaml) | A control-plane node |

## Highly available control plane

| Example | What it builds |
|---|---|
| [`ha-controller-first.yaml`](examples/ha-controller-first.yaml) | The first of three controllers, which mints the token the others need |
| [`ha-controller-join.yaml`](examples/ha-controller-join.yaml) | The second and third controllers, which wait for that token |

See [HA cluster](install/ha-cluster.md) for the procedure these belong to.

## Networking and storage

| Example | What it builds |
|---|---|
| [`custom-cni.yaml`](examples/custom-cni.yaml) | `cni: custom`, with the CNI installed as an add-on. See [Cilium](cilium.md) |
| [`wireguard-overlay.yaml`](examples/wireguard-overlay.yaml) | A node whose cluster traffic runs over an encrypted host overlay. See [WireGuard overlay](wireguard-overlay.md) |
| [`stretched-controller-calico-wireguard.yaml`](examples/stretched-controller-calico-wireguard.yaml) | A controller in a cluster split across two sites |
| [`stretched-worker.yaml`](examples/stretched-worker.yaml) | A worker on the far side of that split |
| [`raid.yaml`](examples/raid.yaml) | A node that mirrors its two spare disks and puts Kubernetes state on the array. See [Software RAID](raid.md) |

## Extensions

| Example | What it builds |
|---|---|
| [`manifests.yaml`](examples/manifests.yaml) | MetalLB installed as a chart and configured with plain Kubernetes YAML in the same document, through `manifests[]` |

## Managed nodes

| Example | What it builds |
|---|---|
| [`managed-node.yaml`](examples/managed-node.yaml) | A worker reachable with `cctl`, showing each way an operator CA can arrive |
| [`awaiting-config.yaml`](examples/awaiting-config.yaml) | A node that boots and waits to be told what it is — one identical document for a whole fleet, carrying no secrets |

See [cctl](cli.md) for the commands that drive these.

## Beyond the schema

| Example | What it builds |
|---|---|
| [`escape-hatch.yaml`](examples/escape-hatch.yaml) | `k0s.patch`, `write_files` and `runcmd` alongside the `corium:` block, for everything the schema does not model |
| [`standalone.yaml`](examples/standalone.yaml) | The schema on its own, with no cloud-config wrapper — for `/etc/corium/config.yaml`, a kernel command line, or an image default |

---

## Next

- [Configuration reference](reference.md) — every field, and what Corium does with it
- [Feature support](features.md) — what is modelled, what passes through to k0s
- [Quick start](quickstart.md) — from a published image to a working node
