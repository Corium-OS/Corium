# 1. Base the OS on fedora-bootc rather than Fedora CoreOS

Status: accepted

## Context

Corium needs an immutable base in the CoreOS/OSTree lineage, and exposes
cloud-init as its configuration surface.

Fedora CoreOS was the obvious starting point: it is the mature member of the
lineage, with excellent install tooling (`coreos-installer`, live ISO, PXE).
Three things were verified before ruling on it.

- Image-provided Ignition fragments in `/usr/lib/ignition/base.d/` work, and
  with the right precedence. Ignition merges the user's config *over* the
  image's, so baked-in plumbing applies wherever the operator is silent and
  never overrides them. Confirmed in `internal/exec/engine.go` in
  `coreos/ignition`, not from documentation.
- FCOS boots with no Ignition config at all. Maintainers declined to make its
  absence fatal: *"Today, we happily boot and sit there if the user doesn't
  provide Ignition"* (`fedora-coreos-tracker#279`). A cloud-init-only node is
  therefore possible.
- Zincati, FCOS's auto-update agent, fails on OCI-native deployments and must
  be masked.

So the design was buildable. The question was whether it should be built.

## Decision

Base on `quay.io/fedora/fedora-bootc`.

## Consequences

FCOS is Ignition-native. Running cloud-init as the primary surface there means
masking Zincati, neutralising Afterburn's SSH key injection — it overlaps
cloud-init's metadata role, and running both gives two agents racing to
provision the same user — and arbitrating between NetworkManager and
cloud-init's network stage. That is a meaningful amount of work whose endpoint
is the place `fedora-bootc` already occupies.

The ecosystem has drawn this line already. `ucore`, the most developed FCOS
derivative, stays on Ignition. `k0snode-bootc`, which runs k0s on bootc with
cloud-init, starts from a generic bootc base rather than FCOS. No project ships
FCOS with cloud-init as its primary surface.

Finally, FCOS is itself converging on bootc (`fedora-coreos-tracker#2214`,
targeting Fedora 46). Starting from `fedora-bootc` is starting where FCOS is
going.

What is given up: FCOS's install tooling and its battle-tested server base.
`bootc-image-builder` covers the first, producing an installable artefact
directly from the image rather than FCOS's two-step of installing a stock image
and rebasing onto ours.
