#!/usr/bin/env bash
#
# Install the pinned k0s binary into the read-only system tree.
#
# k0s is baked into /usr and is never modified at runtime: Kubernetes upgrades
# ship as a new OS image. k0s extracts its own supervised binaries into
# /var/lib/k0s/bin at first start, which is writable persistent state and not
# our concern here.
set -euo pipefail

readonly LOCK_FILE="/run/corium-build/k0s.lock"
readonly DEST="/usr/bin/k0s"

# shellcheck disable=SC1090
source "${LOCK_FILE}"

arch="$(uname -m)"
case "${arch}" in
	x86_64)  k0s_arch=amd64 ;;
	aarch64) k0s_arch=arm64 ;;
	*)
		echo "install-k0s: unsupported architecture: ${arch}" >&2
		exit 1
		;;
esac

# Indirect expansion: K0S_SHA256_amd64 / K0S_SHA256_arm64.
checksum_var="K0S_SHA256_${k0s_arch}"
expected="${!checksum_var:-}"
if [[ -z "${expected}" ]]; then
	echo "install-k0s: no checksum pinned for ${k0s_arch} in ${LOCK_FILE}" >&2
	exit 1
fi

url="https://github.com/k0sproject/k0s/releases/download/${K0S_VERSION//+/%2B}/k0s-${K0S_VERSION}-${k0s_arch}"

echo "install-k0s: fetching k0s ${K0S_VERSION} (${k0s_arch})"
curl --fail --silent --show-error --location --retry 3 --retry-delay 2 \
	--output "${DEST}" "${url}"

actual="$(sha256sum "${DEST}" | cut -d' ' -f1)"
if [[ "${actual}" != "${expected}" ]]; then
	echo "install-k0s: checksum mismatch for ${k0s_arch}" >&2
	echo "  expected: ${expected}" >&2
	echo "  actual:   ${actual}" >&2
	rm -f "${DEST}"
	exit 1
fi

chmod 0755 "${DEST}"
echo "install-k0s: installed ${DEST} (${K0S_VERSION})"
