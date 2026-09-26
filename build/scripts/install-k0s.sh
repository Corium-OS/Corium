#!/usr/bin/env bash
#
# Install the floor k0s binary into the read-only system tree.
#
# "Floor" is the load-bearing word. This binary is what a node runs when it has
# been told nothing, when it cannot reach a mirror, or when its system extension
# has stopped matching -- which, since /var survives an OS rebase, is a state a
# node can reach without anyone doing anything. A signed extension may overlay
# /usr/bin/k0s with another version; the path, and everything that invokes it,
# is unchanged either way. See docs/adr/0010-kubernetes-version-axis.md.
#
# k0s extracts its own supervised binaries into /var/lib/k0s/bin at first start,
# which is writable persistent state and not our concern here.
set -euo pipefail

readonly LOCK_FILE="/run/corium-build/k0s.lock"
readonly DEST="/usr/bin/k0s"

# The floor version is a single assignment in the lock file; the checksums are
# the table below it, keyed by version and architecture.
floor="$(awk -F= '/^K0S_FLOOR=/ { print $2 }' "${LOCK_FILE}")"
if [[ -z "${floor}" ]]; then
	echo "install-k0s: no K0S_FLOOR pinned in ${LOCK_FILE}" >&2
	exit 1
fi

arch="$(uname -m)"
case "${arch}" in
	x86_64)  k0s_arch=amd64 ;;
	aarch64) k0s_arch=arm64 ;;
	*)
		echo "install-k0s: unsupported architecture: ${arch}" >&2
		exit 1
		;;
esac

expected="$(awk -v v="${floor}" -v a="${k0s_arch}" \
	'$1 == v && $2 == a { print $3 }' "${LOCK_FILE}")"
if [[ -z "${expected}" ]]; then
	echo "install-k0s: no checksum pinned for ${floor} (${k0s_arch}) in ${LOCK_FILE}" >&2
	exit 1
fi

url="https://github.com/k0sproject/k0s/releases/download/${floor//+/%2B}/k0s-${floor}-${k0s_arch}"

echo "install-k0s: fetching k0s ${floor} (${k0s_arch})"
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
echo "install-k0s: installed ${DEST} (${floor}, floor version)"
