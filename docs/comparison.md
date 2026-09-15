# Corium and the alternatives

An honest comparison. If one of these fits you better, use it — that is a
better outcome than adopting Corium and regretting it.

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

---

## Why you might pick each one instead

**Talos Linux** is the strongest option in this space, and the most complete.
It removes the shell entirely — no SSH, no interactive login, a gRPC API and
nothing else — which is a genuine security position, not a gimmick. Since
September 2026 there is a commercial tier with FIPS 140-3, SBOMs and CVE SLAs,
so you can buy support. Pick Talos if you want the smallest attack surface
available and are willing to give up shell-based operations.

Two honest caveats. The no-shell model is the most common source of friction
for newcomers, and its upgrade tooling has produced real incidents — issues
about upgrades failing without a clear cause, and Secure Boot images bricking
nodes into physical recovery. Also note that Omni, the fleet manager that makes
Talos pleasant at scale, is BUSL-licensed: free self-hosting is non-production
only.

**Kairos** is the closest neighbour and a CNCF Sandbox project, which means
neutral governance that Corium does not have. It supports k3s, k0s, kubeadm and
RKE2 rather than betting on one, and it keeps SSH. Pick Kairos if you want a
foundation-governed project, or the freedom to change Kubernetes distribution
later.

The difference is the immutability primitive. Kairos builds its own A/B and
recovery scheme with elemental-toolkit on top of an arbitrary base distribution.
It is **not** built on bootc, though recent work (kairos-init, the Hadron base)
moves it toward OCI-based builds. Corium's whole argument is that the OS should
be an ordinary container image, built and shipped with the tools you already
use — if that argument does not move you, Kairos is the more mature choice.

**Flatcar Container Linux** has the longest production record of A/B updates
with dm-verity, and is CNCF-donated. One fact belongs in any honest comparison:
Azure retired Flatcar for AKS in 2026, with node images removed in September
and no in-place migration, replaced by Azure Linux. That is not a judgement on
Flatcar's engineering, but it is a real data point about depending on a
hyperscaler's commitment.

**Fedora CoreOS, CentOS bootc, RHEL image mode** share Corium's foundation —
this is where bootc comes from, with Red Hat behind it and enterprise support
available for RHEL. They ship **no Kubernetes at all**. Pick them if you want
the base and intend to build the Kubernetes layer yourself; that is exactly the
gap Corium fills, and if you would rather own that layer, you should.

**Bottlerocket** has the deepest AWS integration, dm-verity integrity checking
and a clean settings API. Pick it if you are committed to EKS or ECS. Outside
AWS it is architecturally possible but not where the support and usage are, and
AWS has been reducing its bare-metal footprint rather than expanding it.

**Ubuntu Core** offers a security maintenance window nothing else matches — up
to 15 years — and is built for regulated IoT and edge fleets. Its Kubernetes
story is the weak point: Canonical's Kubernetes and MicroK8s are usually run on
Ubuntu Server rather than Core, so treat Core + Kubernetes as a path you would
be building, not following.

**SUSE SL Micro** (what Elemental images became) is the right answer inside the
Rancher and Harvester ecosystem, and Edge Image Builder is genuinely good at
air-gapped edge images. Pick it if you are already in that ecosystem.

**k3OS** is archived. Its README points to Elemental. Mentioned because it
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

## Prior art worth crediting

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
that *is* a container image — built with a `Containerfile`, pushed to a
registry, signed, scanned, promoted by moving a tag, rolled back by digest — is
worth more than a bespoke mechanism, however good that mechanism is.

That is a bet about ergonomics, not a claim of technical superiority. Talos is
more hardened. Kairos is more flexible and better governed. RHEL image mode has
more behind it. Corium's argument is that its parts are ones you already know.

If that argument does not land, one of the projects above is a better fit, and
this page has done its job.
