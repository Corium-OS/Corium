# Corium and the alternatives

For anyone choosing an immutable Linux to run Kubernetes on. The table below
compares the projects on five axes — how immutability is implemented, how a
machine is configured, which Kubernetes comes with it, whether there is a
shell, and the licence — and the sections after it say, project by project,
when to pick that one instead. If one of these fits you better, use it: that is
a better outcome than adopting Corium and regretting it.

Facts were checked in September 2026. This space moves; where something was
recent or unverifiable at the time of writing, it says so rather than
pretending to certainty.

---

## At a glance

| | Immutability | Configured with | Kubernetes | Shell | Licence |
|---|---|---|---|---|---|
| **Corium** | bootc / OSTree, image is an OCI artefact | cloud-init | k0s, baked in | yes | MIT |
| **[Talos Linux](https://www.talos.dev/)** | Read-only squashfs root, A/B | Typed YAML over a gRPC API | Its own upstream build | **no** | MPL-2.0 (Omni: BUSL-1.1) |
| **[Kairos](https://kairos.io/)** | elemental-toolkit A/B + recovery | cloud-init | k3s, k0s, kubeadm, RKE2 | yes | Apache-2.0 |
| **[Flatcar](https://www.flatcar.org/)** | A/B, read-only `/usr` + dm-verity | Ignition | none — bring your own | yes | Apache-2.0 |
| **[Fedora / CentOS / RHEL bootc](https://bootc.dev/)** | bootc / OSTree | Ignition or cloud-init | none — bring your own | yes | Various open |
| **[Bottlerocket](https://github.com/bottlerocket-os/bottlerocket)** | dm-verity read-only root, A/B | TOML via a local API | EKS and ECS variants | via admin container | Apache-2.0 / MIT |
| **[Ubuntu Core](https://ubuntu.com/core)** | Snap-based, transactional | Snap config, cloud-init | not a flagship pairing | limited | Various open |
| **[SUSE SL Micro](https://microos.opensuse.org/)** | Btrfs snapshots, transactional-update | Elemental / Edge Image Builder | RKE2 in the SUSE stack | yes | Various open |
| **[k0s](https://k0sproject.io/) on a normal distro** | none | whatever you already use | k0s | yes | Apache-2.0 |

"Configured with" is about how a machine is *provisioned*, which for Corium is
cloud-init and nothing else. Corium also ships a day-two management API and a
client, `cctl`, for reading a node, driving its upgrades and taking it out of
service — but it is additive and off unless asked for, where Talos's API is the
only way in. The two answer different questions. Talos removed the shell and
had to put an API in its place. Corium kept the shell and added an API because
SSH is a poor fit for the handful of things an operator actually does to an
immutable node.

---

## Why you might pick each one instead

**Talos Linux** is the strongest option in this space, and the most complete.
It removes the shell entirely — no SSH, no interactive login, a gRPC API and
nothing else — which is a genuine security position, not a gimmick. Since
September 2026 there is a commercial tier with FIPS 140-3, SBOMs and CVE SLAs,
so you can buy support. Pick Talos if you want the smallest attack surface
available and are willing to give up shell-based operations.

Two honest caveats. The no-shell model is the most common source of friction
for newcomers, and it is a commitment rather than a setting you can relax
later. And Omni, the fleet manager that makes Talos pleasant at scale, is
BUSL-licensed: free self-hosting is non-production only. Corium's own API is
the shallower version of the same idea — the operations, without giving up the
shell — which is a weaker security position and an easier one to adopt.

For the rest:

| Project | Pick it when | The catch |
|---|---|---|
| **Kairos** | You want neutral governance — it is a CNCF Sandbox project, which Corium is not — or the freedom to change Kubernetes distribution later: it supports k3s, k0s, kubeadm and RKE2 rather than betting on one, and it keeps SSH | It is **not** built on bootc. It builds its own A/B and recovery scheme with elemental-toolkit on top of an arbitrary base distribution, though recent work (kairos-init, the Hadron base) moves it toward OCI-based builds |
| **Flatcar Container Linux** | You want the longest production record of A/B updates with dm-verity, from a CNCF-donated project | Azure retired Flatcar for AKS in 2026: node images removed in September, no in-place migration, replaced by Azure Linux. Not a judgement on Flatcar's engineering, but a real data point about depending on a hyperscaler's commitment |
| **Fedora CoreOS, CentOS bootc, RHEL image mode** | You want Corium's own foundation — this is where bootc comes from, with Red Hat behind it and enterprise support available for RHEL — and intend to build the Kubernetes layer yourself | They ship **no Kubernetes at all**. That is exactly the gap Corium fills, and if you would rather own that layer, you should |
| **Bottlerocket** | You are committed to EKS or ECS: the deepest AWS integration, dm-verity integrity checking, a clean settings API | Outside AWS it is architecturally possible but not where the support and usage are, and AWS has been reducing its bare-metal footprint rather than expanding it |
| **Ubuntu Core** | You run regulated IoT or edge fleets and want a security maintenance window nothing else matches, up to 15 years | Kubernetes is the weak point. Canonical's Kubernetes and MicroK8s are usually run on Ubuntu Server rather than Core, so treat Core + Kubernetes as a path you would be building, not following |
| **SUSE SL Micro** (what Elemental images became) | You are already in the Rancher and Harvester ecosystem; Edge Image Builder is genuinely good at air-gapped edge images | Outside that ecosystem there is less reason to reach for it |

Kairos is the closest neighbour of the lot, and the difference is the
immutability primitive. Corium's whole argument is that the OS should be an
ordinary container image, built and shipped with the tools you already use. If
that argument does not move you, Kairos is the more mature choice.

**k3OS** is archived and its README points to Elemental. Mentioned because it
still appears in comparisons.

---

## The real baseline: k0s on an ordinary distribution

Most clusters run Kubernetes on a normal Ubuntu, Debian or RHEL, and this is
the option Corium actually has to justify itself against.

You get complete control, your existing tooling, package manager, SSH,
configuration management, and the operational knowledge your team already has.
No vendor image pipeline, no new abstraction, nothing to learn. For many teams
that is the right answer and no further reading is required.

What you build yourself: atomic updates and rollback, drift prevention between
machines, an image build and release pipeline, and node provisioning. Corium is
those four things, and nothing more. If you do not need them, you do not need
Corium.

---

## Earlier bootc + k0s attempts

Two small projects combine bootc with k0s:
[RobertoBochet/k0snode-bootc](https://github.com/RobertoBochet/k0snode-bootc)
and [lollo03/bootc-homelab-k0s](https://github.com/lollo03/bootc-homelab-k0s).
Both are personal repositories rather than maintained products, and neither
appears to build a cloud-init configuration layer on top, but the idea is not
original to Corium and they got there first.

Beyond those, no adopted project combines bootc, k0s and cloud-init. That gap
is narrow, and its existence is not itself a reason for Corium to exist — only
a reason it is not redundant.

---

## When not to use Corium

The useful part of a comparison.

**You need commercial support or a compliance story today.** Corium is early
and has no support contract. Talos Enterprise or RHEL image mode do.

**You need neutral governance.** Corium is one project by one author. Kairos is
CNCF Sandbox; Flatcar is CNCF-donated.

**You want to change Kubernetes distribution later.** Corium is k0s and only
k0s. Kairos supports four.

**You want the smallest possible attack surface.** Talos removes the shell.
Corium keeps a normal Fedora userland with SSH, which is more surface than
Talos by construction.

**You are entirely on AWS or in the Rancher ecosystem.** Bottlerocket and
SL Micro are better integrated than a general-purpose image will be.

**You do not want immutability.** It has real costs: no in-place package
installs, configuration changes mean reprovisioning, and every change goes
through a build. If those costs buy you nothing, an ordinary distribution is
simpler and you will be happier.

---

## What Corium is betting on

That for teams already living in OCI registries and GitOps, an operating system
that *is* a container image is worth more than a bespoke mechanism, however
good that mechanism is. That is a bet about ergonomics rather than a claim of
technical superiority: Talos is more hardened, Kairos better governed, RHEL
image mode has more behind it.
