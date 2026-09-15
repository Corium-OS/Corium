# Corium — an immutable Kubernetes node, built as an OCI image.
#
# The operating system is this file's output: a bootc image pushed to a registry,
# versioned by digest, installed with bootc-image-builder, and upgraded by
# booting a newer digest.
#
# Build:   make image
# Inspect: podman run --rm -it <image> bash

ARG BASE_IMAGE=quay.io/fedora/fedora-bootc
ARG BASE_TAG=43

# ---------------------------------------------------------------------------
# Stage 1 — build the Corium agent.
#
# Compiled in a throwaway stage so the Go toolchain never reaches the OS image.
# ---------------------------------------------------------------------------
FROM docker.io/library/golang:1.23 AS agent-builder

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

# ---------------------------------------------------------------------------
# Stage 2 — the operating system.
# ---------------------------------------------------------------------------
FROM ${BASE_IMAGE}:${BASE_TAG}

ARG VERSION=dev

LABEL org.opencontainers.image.title="Corium" \
      org.opencontainers.image.description="Immutable Kubernetes node based on bootc and k0s" \
      org.opencontainers.image.source="https://github.com/qjoly/corium" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# --- Packages --------------------------------------------------------------
#
# cloud-init is the configuration surface. Everything else is the minimum a
# Kubernetes node needs: iptables/nftables for kube-proxy and the CNI, iproute
# for the network setup k0s performs, conntrack for service tracking.
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
	&& dnf clean all \
	&& rm -rf /var/cache/dnf /var/lib/dnf/history.sqlite*

# --- k0s -------------------------------------------------------------------
#
# The lock file is mounted rather than copied: the build-time trust anchor has
# no business persisting in the shipped image.
COPY build/scripts/install-k0s.sh /tmp/install-k0s.sh
RUN --mount=type=bind,source=build/k0s.lock,target=/run/corium-build/k0s.lock \
	/tmp/install-k0s.sh && rm -f /tmp/install-k0s.sh

# --- Corium agent ----------------------------------------------------------
COPY --from=agent-builder /out/corium-agent /usr/bin/corium-agent

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
RUN systemctl enable corium-bootstrap.service \
	&& systemctl enable cloud-init.service \
	&& systemctl enable cloud-init-local.service \
	&& systemctl enable cloud-config.service \
	&& systemctl enable cloud-final.service

# The image decides when it updates; it does not update itself behind the
# operator's back. Upgrades are an explicit, orchestrated, drain-aware act.
RUN systemctl mask bootc-fetch-apply-updates.timer

# --- State -----------------------------------------------------------------
#
# /var is persistent machine state and is only seeded at install time. These
# directories exist so first boot does not have to guess at ownership and mode.
RUN mkdir -p /var/lib/k0s /var/lib/corium \
	&& chmod 0700 /var/lib/k0s /var/lib/corium

# Validate the result against bootc's expectations for a bootable image.
RUN bootc container lint
