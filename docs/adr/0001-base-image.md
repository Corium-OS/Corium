# 1. Base the OS on fedora-bootc rather than Fedora CoreOS

Status: accepted

## Context

Corium needs an immutable base in the CoreOS/OSTree lineage. Its configuration
surface is cloud-init.

Fedora CoreOS was the obvious starting point. It is the mature member of the
lineage, and its install tooling is excellent (`coreos-installer`, live ISO,
PXE). We verified three things before we made a decision on it.

- Ignition fragments that the image provides in `/usr/lib/ignition/base.d/`
  work, and with the correct precedence. Ignition merges the user's
  configuration *over* the image's configuration. The plumbing in the image
  thus applies where the operator says nothing, and it never overrides the
  operator. We confirmed this in `internal/exec/engine.go` in
  `coreos/ignition`, not in the documentation.
- FCOS boots with no Ignition configuration at all. The maintainers refused
  to make a missing configuration fatal: *"Today, we happily boot and sit
  there if the user doesn't provide Ignition"* (`fedora-coreos-tracker#279`).
  Thus a node that uses only cloud-init is possible.
- Zincati, the auto-update agent of FCOS, fails on OCI-native deployments.
  You must mask it.

So the design was possible to build. The question was whether to build it.

## Decision

Base on `quay.io/fedora/fedora-bootc`.

## Consequences

FCOS is Ignition-native. To use cloud-init as the primary surface on FCOS, we
must do three things:

- Mask Zincati.
- Neutralize the SSH key injection of Afterburn. Afterburn overlaps the
  metadata role of cloud-init. If both run, two agents race to provision the
  same user.
- Arbitrate between NetworkManager and the network stage of cloud-init.

That is a large amount of work, and its result is the place where
`fedora-bootc` already is.

The ecosystem already made this choice. `ucore`, the most developed FCOS
derivative, stays on Ignition. `k0snode-bootc` runs k0s on bootc with
cloud-init, and it starts from a generic bootc base, not from FCOS. No project
ships FCOS with cloud-init as its primary surface.

Also, FCOS itself is moving to bootc (`fedora-coreos-tracker#2214`, targeting
Fedora 46). When we start from `fedora-bootc`, we start where FCOS is going.

What we give up: the install tooling of FCOS and its server base, which has
much testing in production. `bootc-image-builder` replaces the first. It makes
an installable artifact directly from the image. FCOS needs two steps: you
install a stock image, then you rebase onto ours.
