# Corium and the alternatives

This page is for readers who must choose an immutable Linux to run Kubernetes
on. The table below compares the projects on five axes:

- How the project implements immutability.
- How you configure a machine.
- Which Kubernetes comes with it.
- If it has a shell.
- The license.

The sections after the table tell you, project by project, when to pick that
project instead. If one of these projects fits you better, use it. That is a
better result than a choice of Corium that you regret.

The facts were checked in September 2026. This area changes quickly. If a fact
was recent or not verifiable at the time of writing, the page says so.

---

## At a glance

| | Immutability | Configured with | Kubernetes | Shell | License |
|---|---|---|---|---|---|
| **Corium** | bootc / OSTree, image is an OCI artifact | cloud-init | k0s, baked in | yes | MIT |
| **[Talos Linux](https://www.talos.dev/)** | Read-only squashfs root, A/B | Typed YAML over a gRPC API | Its own upstream build | **no** | MPL-2.0 (Omni: BUSL-1.1) |
| **[Kairos](https://kairos.io/)** | elemental-toolkit A/B + recovery | cloud-init | k3s, k0s, kubeadm, RKE2 | yes | Apache-2.0 |
| **[Flatcar](https://www.flatcar.org/)** | A/B, read-only `/usr` + dm-verity | Ignition | none, bring your own | yes | Apache-2.0 |
| **[Fedora / CentOS / RHEL bootc](https://bootc.dev/)** | bootc / OSTree | Ignition or cloud-init | none, bring your own | yes | Various open |
| **[Bottlerocket](https://github.com/bottlerocket-os/bottlerocket)** | dm-verity read-only root, A/B | TOML via a local API | EKS and ECS variants | via admin container | Apache-2.0 / MIT |
| **[Ubuntu Core](https://ubuntu.com/core)** | Snap-based, transactional | Snap config, cloud-init | not a flagship pairing | limited | Various open |
| **[SUSE SL Micro](https://microos.opensuse.org/)** | Btrfs snapshots, transactional-update | Elemental / Edge Image Builder | RKE2 in the SUSE stack | yes | Various open |
| **[k0s](https://k0sproject.io/) on a normal distro** | none | whatever you already use | k0s | yes | Apache-2.0 |

"Configured with" is about how a machine is *provisioned*. For Corium, this is
cloud-init and nothing else. Corium also ships a day-two management API and a
client, `cctl`. You use them to read a node, run its upgrades, and take it out
of service. But this API is an addition, and it is off unless you turn it on.
The Talos API is the only way into a Talos node. The two APIs answer different
questions. Talos removed the shell and had to put an API in its place. Corium
kept the shell and added an API, because SSH is a poor fit for the few tasks
that an operator does on an immutable node.

---

## Why you might pick each one instead

**Talos Linux** is the strongest option in this area, and the most complete.
It removes the shell fully. It has no SSH and no interactive login, only a gRPC
API. This is a real security decision. Since September 2026, Talos has a
commercial tier with FIPS 140-3, SBOMs, and CVE SLAs, so you can buy support.
Pick Talos if you want the smallest attack surface available and you accept
operations without a shell.

There are two caveats. New users have the most difficulty with the no-shell
model. It is a permanent decision, not a setting that you can relax later.
Also, Omni is BUSL-licensed. Omni is the fleet manager that makes Talos easy to
use at scale. You can self-host it for free only outside production. The API of
Corium is a smaller version of the same idea: it gives the operations, and you
keep the shell. This is a weaker security position, but it is easier to adopt.

For the rest:

| Project | Pick it when | The catch |
|---|---|---|
| **Kairos** | You want neutral governance. Kairos is a CNCF Sandbox project, and Corium is not. Or you want to be free to change the Kubernetes distribution later. Kairos supports k3s, k0s, kubeadm, and RKE2, not only one, and it keeps SSH | It is **not** built on bootc. It builds its own A/B and recovery scheme with elemental-toolkit on top of any base distribution. But recent work (kairos-init, the Hadron base) moves it toward OCI-based builds |
| **Flatcar Container Linux** | You want the longest production record of A/B updates with dm-verity, from a CNCF-donated project | Azure retired Flatcar for AKS in 2026. Azure removed the node images in September, gave no in-place migration, and replaced Flatcar with Azure Linux. This says nothing about the engineering of Flatcar. But it is a real data point about dependence on the commitment of a hyperscaler |
| **Fedora CoreOS, CentOS bootc, RHEL image mode** | You want the foundation of Corium, and you plan to build the Kubernetes layer yourself. bootc comes from here, Red Hat is behind it, and enterprise support is available for RHEL | They ship **no Kubernetes at all**. Corium fills exactly that gap. If you prefer to own that layer, use them |
| **Bottlerocket** | You are committed to EKS or ECS. It gives the deepest AWS integration, dm-verity integrity checks, and a clean settings API | Outside AWS, the architecture allows it, but the support and the users are on AWS. Also, AWS is making its bare-metal footprint smaller, not larger |
| **Ubuntu Core** | You run regulated IoT or edge fleets and want a security maintenance window of up to 15 years, which no other project matches | Kubernetes is the weak point. Users usually run Canonical Kubernetes and MicroK8s on Ubuntu Server, not on Core. Core + Kubernetes is a path that you build yourself, not a path that you follow |
| **SUSE SL Micro** (what Elemental images became) | You already use the Rancher and Harvester ecosystem. Edge Image Builder is good at air-gapped edge images | Outside that ecosystem, there is less reason to use it |

Of all these projects, Kairos is the closest to Corium. The difference is the
immutability primitive. The main argument of Corium is that the OS must be an
ordinary container image, built and shipped with the tools that you already use.
If that argument does not convince you, Kairos is the more mature choice.

**k3OS** is archived, and its README points to Elemental. This page mentions it
because it still appears in comparisons.

---

## The real baseline: k0s on an ordinary distribution

Most clusters run Kubernetes on a normal Ubuntu, Debian, or RHEL. Corium must
show that it is better than this option.

With a normal distribution, you get full control, your existing tools, the
package manager, SSH, configuration management, and the operational knowledge
that your team already has. There is no vendor image pipeline, no new
abstraction, and nothing to learn. For many teams, that is the correct answer,
and they do not need to read further.

You must build these things yourself:

- Atomic updates and rollback.
- Drift prevention between machines.
- An image build and release pipeline.
- Node provisioning.

Corium is those four things, and nothing more. If you do not need them, you do
not need Corium.

---

## Earlier bootc + k0s attempts

Two small projects combine bootc with k0s:
[RobertoBochet/k0snode-bootc](https://github.com/RobertoBochet/k0snode-bootc)
and [lollo03/bootc-homelab-k0s](https://github.com/lollo03/bootc-homelab-k0s).
Both are personal repositories, not maintained products. Neither one appears to
add a cloud-init configuration layer. But the idea did not start with Corium,
and these projects had it first.

Apart from these two, no adopted project combines bootc, k0s, and cloud-init.
That gap is narrow. The gap alone is not a reason for Corium to exist. It only
shows that Corium does not duplicate another project.

---

## When not to use Corium

This is the useful part of a comparison.

**You need commercial support or a compliance story today.** Corium is a young
project and has no support contract. Talos Enterprise and RHEL image mode have
one.

**You need neutral governance.** Corium is one project by one author. Kairos is
a CNCF Sandbox project, and Flatcar is CNCF-donated.

**You want to change Kubernetes distribution later.** Corium is k0s and only
k0s. Kairos supports four.

**You want the smallest possible attack surface.** Talos removes the shell.
Corium keeps a normal Fedora userland with SSH, which is more surface than
Talos by construction.

**You are entirely on AWS or in the Rancher ecosystem.** Bottlerocket and
SL Micro have better integration than a general-purpose image.

**You do not want immutability.** Immutability has real costs:

- You cannot install packages in place.
- A configuration change means that you provision the machine again.
- Every change goes through a build.

If those costs give you nothing, an ordinary distribution is simpler and a
better fit.

---

## What Corium is betting on

Corium bets on teams that already work with OCI registries and GitOps. For these
teams, an operating system that *is* a container image is worth more than a
custom mechanism, even a good one. This bet is about ease of use. It does not
claim that Corium is technically better. Talos is more hardened, Kairos has
better governance, and RHEL image mode has more support behind it.
