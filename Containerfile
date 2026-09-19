# Corium — an immutable Kubernetes node, built as an OCI image.
#
# The operating system is this file's output: a bootc image pushed to a registry,
# versioned by digest, installed with bootc-image-builder, and upgraded by
# booting a newer digest.
#
# Build:   mise run image
# Inspect: podman run --rm -it <image> bash

ARG BASE_IMAGE=quay.io/fedora/fedora-bootc
ARG BASE_TAG=44

# Release builds should pin BASE_TAG to a digest rather than a moving tag, so
# that rebuilding an old release reproduces the OS it originally shipped.

# ---------------------------------------------------------------------------
# Stage 1 — build the Corium agent.
#
# Compiled in a throwaway stage so the Go toolchain never reaches the OS image.
# ---------------------------------------------------------------------------
FROM docker.io/library/golang:1.27 AS agent-builder

WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum actually change.
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
	go build -trimpath \
	-ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
	-o /out/corium-agent ./cmd/corium-agent

RUN CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
	go build -trimpath \
	-ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
	-o /out/corium-apid ./cmd/corium-apid

# ---------------------------------------------------------------------------
# Stage 2 — the operating system.
# ---------------------------------------------------------------------------
FROM ${BASE_IMAGE}:${BASE_TAG}

ARG VERSION=dev

LABEL org.opencontainers.image.title="Corium" \
      org.opencontainers.image.description="Immutable Kubernetes node based on bootc and k0s" \
      org.opencontainers.image.source="https://github.com/Corium-OS/Corium" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# --- Packages --------------------------------------------------------------
#
# cloud-init is the configuration surface. Everything else is the minimum a
# Kubernetes node needs: iptables/nftables for kube-proxy and the CNI, iproute
# for the network setup k0s performs, conntrack for service tracking.
#
# wireguard-tools ships present but inert, the same way sshd does: nothing enables
# it until a node declares a corium.wireguard interface, at which point the agent
# writes the config and starts wg-quick. It cannot be added at boot -- /usr is
# read-only and cloud-init's packages: is disabled -- so it is an image concern.
# The kernel module is in-tree and loads on demand. See
# docs/adr/0006-host-wireguard-overlay.md.
#
# Deliberately absent: any container engine. k0s ships and supervises its own
# containerd under /var/lib/k0s/bin. A second engine on the host would fight it
# for cgroups and CNI state.
RUN dnf install -y --setopt=install_weak_deps=False \
		cloud-init \
		iptables-nft \
		nftables \
		iproute \
		iproute-tc \
		conntrack-tools \
		socat \
		ethtool \
		wireguard-tools \
		qemu-guest-agent \
		greenboot \
	&& dnf clean all \
	&& rm -rf /var/cache/* /var/lib/dnf /var/log/dnf* /var/log/hawkey.log

# --- k0s -------------------------------------------------------------------
#
# The lock file is mounted rather than copied: the build-time trust anchor has
# no business persisting in the shipped image.
COPY build/scripts/install-k0s.sh /tmp/install-k0s.sh
RUN --mount=type=bind,source=build/k0s.lock,target=/run/corium-build/k0s.lock \
	/tmp/install-k0s.sh && rm -f /tmp/install-k0s.sh

# --- Writable /opt ---------------------------------------------------------
#
# Kubernetes expects /opt/cni/bin to be writable: k0s, like every other
# distribution, ships the CNI plugin binaries into it at runtime. On an OSTree
# system /opt is part of the read-only image, so the CNI DaemonSet fails to
# start and the node never leaves NotReady:
#
#   MountVolume.SetUp failed for volume "cni-bin":
#     mkdir /opt/cni: read-only file system
#
# Pointing /opt at /var/opt makes it machine state, which is what /opt has
# always meant. The link is relative, as OSTree requires for links out of the
# root into /var.
RUN rm -rf /opt && ln -s var/opt /opt

# --- Corium agent ----------------------------------------------------------
COPY --from=agent-builder /out/corium-agent /usr/bin/corium-agent
COPY --from=agent-builder /out/corium-apid /usr/bin/corium-apid

# --- System overlay --------------------------------------------------------
#
# systemd units, sysctls, cloud-init defaults and static assets. Everything here
# lands under /usr, which is read-only at runtime and replaced wholesale on
# upgrade.
COPY build/files/usr /usr
COPY build/files/etc /etc

# --- Service wiring --------------------------------------------------------
#
# Enabled at build time so the preset is baked into the image rather than
# written to /etc on a running system.
# cloud-init's own units are deliberately not enabled here. The package preset
# already enables them, and their names are not stable across releases:
# cloud-init 26.1 on Fedora 44 has no cloud-init.service at all, having split it
# into cloud-init-main.service and cloud-init-network.service. Enabling them by
# name buys nothing and breaks on upgrade.
RUN systemctl enable corium-bootstrap.service \
	&& systemctl enable corium-apid.service \
	&& systemctl enable qemu-guest-agent.service \
	&& systemctl enable greenboot-healthcheck.service \
	&& systemctl enable corium-uncordon.service \
	&& systemctl enable corium-console-banner.timer

# The image decides when it updates; it does not update itself behind the
# operator's back. Upgrades are an explicit, orchestrated, drain-aware act.
RUN systemctl mask bootc-fetch-apply-updates.timer

# State directories are declared in /usr/lib/tmpfiles.d/corium.conf rather than
# created here; see that file for why.

# /run and /tmp are runtime-only: whatever the build left there is never read,
# because both are fresh tmpfs mounts on a booted system. Shipping their
# contents just makes the image bigger and the lint noisier.
#
# Named explicitly rather than globbed: podman bind-mounts its own paths under
# /run during the build (resolv.conf among them), and a wildcard trips over
# them. If a future package leaves something else behind, the lint will say so.
RUN rm -rf /run/cloud-init /run/corium-build /run/dnf /tmp/* /var/tmp/*

# Validate the result against bootc's expectations for a bootable image.
#
# --fatal-warnings because the warnings here are not stylistic: they catch
# content written to /var that will not survive an upgrade, and files left in
# runtime-only directories. The image is clean today, and this keeps it that
# way rather than letting warnings accumulate until nobody reads them.
RUN bootc container lint --fatal-warnings
