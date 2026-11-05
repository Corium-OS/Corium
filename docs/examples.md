# Configuration examples

These are annotated cloud-config documents for the common types of node. Each
one is a complete, working document. Copy it, change the addresses and keys,
and boot.

The files are in [`docs/examples/`](examples/) in the repository. Each file has
comments that explain what a field does and why it is there. The links below
open the files on GitHub.

Before you boot anything, validate the document:

```bash
corium-agent validate node.yaml
```

Validation works offline and reports all problems at the same time.

---

## Start here

| Example | What it builds |
|---|---|
| [`single-node.yaml`](examples/single-node.yaml) | The smallest useful node: a complete single-node cluster in a few lines |
| [`worker.yaml`](examples/worker.yaml) | A worker that joins an existing cluster with a token |
| [`controller.yaml`](examples/controller.yaml) | A control-plane node |

## Highly available control plane

| Example | What it builds |
|---|---|
| [`ha-controller-first.yaml`](examples/ha-controller-first.yaml) | The first of three controllers. It creates the token that the other controllers need |
| [`ha-controller-join.yaml`](examples/ha-controller-join.yaml) | The second and third controllers. They wait for that token |

See [HA cluster](install/ha-cluster.md) for the procedure that uses these files.

## Networking and storage

| Example | What it builds |
|---|---|
| [`custom-cni.yaml`](examples/custom-cni.yaml) | `cni: custom`, with the CNI installed as an add-on. See [Cilium](cilium.md) |
| [`wireguard-overlay.yaml`](examples/wireguard-overlay.yaml) | A node whose cluster traffic runs over an encrypted host overlay. See [WireGuard overlay](wireguard-overlay.md) |
| [`stretched-controller-calico-wireguard.yaml`](examples/stretched-controller-calico-wireguard.yaml) | A controller in a cluster split across two sites |
| [`stretched-worker.yaml`](examples/stretched-worker.yaml) | A worker on the other site of that cluster |
| [`raid.yaml`](examples/raid.yaml) | A node that mirrors its two spare disks and puts the Kubernetes state on the array. See [Software RAID](raid.md) |
| [`kubernetes-version.yaml`](examples/kubernetes-version.yaml) | A node pinned to a Kubernetes version that is not the floor of its image, pulled from an internal mirror. See [ADR 10](adr/0010-kubernetes-version-axis.md) |

## Day two

| Example | What it builds |
|---|---|
| [`backup.yaml`](examples/backup.yaml) | A controller that makes a snapshot of its own control plane every night on an NFS mount, and keeps two weeks of snapshots. See [reference §3.18](reference.md#318-backup) |

## Extensions

| Example | What it builds |
|---|---|
| [`manifests.yaml`](examples/manifests.yaml) | MetalLB, installed as a chart and configured with plain Kubernetes YAML in the same document, through `manifests[]` |

## Managed nodes

| Example | What it builds |
|---|---|
| [`managed-node.yaml`](examples/managed-node.yaml) | A worker that you can reach with `cctl`. It shows each way that an operator CA can arrive |
| [`awaiting-config.yaml`](examples/awaiting-config.yaml) | A node that boots and waits for someone to tell it what it is. One identical document for a whole fleet, with no secrets |

See [cctl](cli.md) for the commands that control these nodes.

## Beyond the schema

| Example | What it builds |
|---|---|
| [`escape-hatch.yaml`](examples/escape-hatch.yaml) | `k0s.patch`, `write_files` and `runcmd` next to the `corium:` block, for everything that the schema does not model |
| [`standalone.yaml`](examples/standalone.yaml) | The schema alone, with no cloud-config wrapper. Use it for `/etc/corium/config.yaml`, a kernel command line, or an image default |

---

## Next

- [Configuration reference](reference.md): every field, and what Corium does with it
- [Feature support](features.md): what Corium models, and what passes through to k0s
- [Quick start](quickstart.md): from a published image to a working node
