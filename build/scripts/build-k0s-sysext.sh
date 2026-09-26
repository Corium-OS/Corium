#!/usr/bin/env bash
#
# Build a k0s system extension.
#
# The extension carries one file that matters -- /usr/bin/k0s -- and overlays it
# onto the path the OS image already fills with its floor version. Nothing about
# the node's units changes: k0s is invoked by absolute path, and an overlay
# leaves that path where it was. See docs/adr/0010-kubernetes-version-axis.md.
#
# Runs inside a Fedora container (mise run k0s-sysext), because it needs
# mkfs.erofs and a reliable sha256sum. It does not need to run on the target
# architecture: a filesystem image is not architecture-specific, only the binary
# inside it is.
set -euo pipefail

readonly LOCK_FILE="${LOCK_FILE:-build/k0s.lock}"
readonly OUT_DIR="${OUT_DIR:-output/sysext}"

# The extension's name, and therefore the filename it must carry once installed:
# systemd matches /var/lib/extensions/<name>.raw against
# /usr/lib/extension-release.d/extension-release.<name> inside the image. The
# name is fixed rather than versioned because it names the slot, not its
# contents -- a node has one k0s extension, and changing version means changing
# what is in that slot. The version travels in the image's metadata instead.
readonly EXT_NAME="corium-k0s"

# The ABI contract between a Corium image and a k0s extension.
#
# Matching on SYSEXT_LEVEL rather than VERSION_ID is the whole reason this
# constant exists. An extension pinned to VERSION_ID binds itself to a Fedora
# major, and since bootc never touches /var, an extension installed before an OS
# rebase survives it and then silently stops matching -- leaving a node quietly
# back on its floor version. Corium owns this number instead, and bumping it is
# as reviewable an event as changing the checksums below it.
readonly SYSEXT_LEVEL="${SYSEXT_LEVEL:-1}"

usage() {
	echo "usage: $0 <k0s-version> <arch>" >&2
	echo "  arch: amd64 | arm64" >&2
	exit 2
}

[[ $# -eq 2 ]] || usage

version="$1"
arch="$2"

case "${arch}" in
	amd64) sysext_arch=x86-64 ;;
	arm64) sysext_arch=arm64 ;;
	*)
		echo "build-k0s-sysext: unsupported architecture: ${arch}" >&2
		exit 1
		;;
esac

# --- The pinned checksum ---------------------------------------------------
#
# Same trust anchor as the floor: the lock file in git, not a checksum fetched
# from beside the artefact. A version absent from the window is refused here
# rather than built and published as though it were supported.
expected="$(awk -v v="${version}" -v a="${arch}" \
	'$1 == v && $2 == a { print $3 }' "${LOCK_FILE}")"

if [[ -z "${expected}" ]]; then
	echo "build-k0s-sysext: ${version} (${arch}) is not pinned in ${LOCK_FILE}" >&2
	echo "  add it with: mise run k0s-window ${version}" >&2
	exit 1
fi

# The staging tree has to live on a filesystem that accepts security.selinux
# xattrs, and that rules out both of the obvious places. A container's own
# rootfs is mounted with a fixed SELinux `context=`, under which writing the
# xattr returns EOPNOTSUPP; a bind mount from a macOS host arrives over virtiofs,
# which does not carry them either. A named podman volume is backed by an
# ordinary Linux filesystem and does. WORK_ROOT points at one; see the
# k0s-sysext task. The finished image is an ordinary file and can be written
# anywhere -- the labels that matter are inside it.
readonly WORK_ROOT="${WORK_ROOT:-/tmp}"
work="$(mktemp -d -p "${WORK_ROOT}")"
trap 'rm -rf "${work}"' EXIT

root="${work}/root"
mkdir -p "${root}/usr/bin" "${root}/usr/lib/extension-release.d"

# --- Fetch and verify ------------------------------------------------------
encoded="${version//+/%2B}"
url="https://github.com/k0sproject/k0s/releases/download/${encoded}/k0s-${version}-${arch}"

echo "build-k0s-sysext: fetching k0s ${version} (${arch})"
curl --fail --silent --show-error --location --retry 3 --retry-delay 2 \
	--output "${root}/usr/bin/k0s" "${url}"

actual="$(sha256sum "${root}/usr/bin/k0s" | cut -d' ' -f1)"
if [[ "${actual}" != "${expected}" ]]; then
	echo "build-k0s-sysext: checksum mismatch for ${version} (${arch})" >&2
	echo "  expected: ${expected}" >&2
	echo "  actual:   ${actual}" >&2
	exit 1
fi

chmod 0755 "${root}/usr/bin/k0s"

# --- The extension release file --------------------------------------------
#
# ID must match the host's, and SYSEXT_LEVEL must match the host's declaration
# of the same. CORIUM_K0S_VERSION is ours: it is how a node answers "which k0s
# is actually merged right now" without executing the binary, which matters when
# the reason you are asking is that it did not start.
cat > "${root}/usr/lib/extension-release.d/extension-release.${EXT_NAME}" <<EOF
ID=fedora
SYSEXT_LEVEL=${SYSEXT_LEVEL}
SYSEXT_SCOPE=system
ARCHITECTURE=${sysext_arch}
CORIUM_K0S_VERSION=${version}
EOF

# --- SELinux labels --------------------------------------------------------
#
# This is not a finishing touch. An extension is merged as the *upper* layer of
# an overlay on /usr, and a merged directory takes its label from the upper
# layer -- so an unlabelled `usr/bin` in here does not merely arrive unlabelled,
# it replaces the label on the node's /usr/bin with whatever this tree carries.
#
# Built in a container, that is `container_file_t` with the build's MCS
# categories, and the result is a node where sshd can no longer traverse
# /usr/bin: SSH accepts the connection and then resets it, which is a
# spectacularly unhelpful way to find out. Verified the hard way on a test node
# (2026-09-26), whose /usr/bin came back as
# container_file_t:s0:c115,c985 while every file beneath it kept its own label.
#
# setfiles applies the target policy's own file_contexts to the tree, so
# usr/bin lands as bin_t, usr/lib as lib_t, and the merged result is
# indistinguishable from the image's own.
# -F forces the reset. Without it setfiles treats the staging tree's inherited
# container_file_t as a deliberate administrative customisation and leaves every
# path alone, reporting success while changing nothing -- "not reset as
# customized by admin", which is easy to read as a warning about someone else's
# system rather than as the build silently failing.
echo "build-k0s-sysext: labelling the tree"
setfiles -F -m -r "${root}" \
	/etc/selinux/targeted/contexts/files/file_contexts "${root}"

# Fail loudly rather than shipping an extension that will take a node's /usr
# down with it. The directories are checked, not the binary, because they are
# the ones that overwrite a label rather than add one.
for dir in usr/bin usr/lib; do
	# tr strips the trailing NUL getfattr emits, which the shell warns about.
	label="$(getfattr --only-values -n security.selinux \
		"${root}/${dir}" 2>/dev/null | tr -d '\0' || true)"
	case "${label}" in
		system_u:object_r:bin_t:s0*|system_u:object_r:lib_t:s0*) ;;
		*)
			echo "build-k0s-sysext: ${dir} carries an unexpected SELinux label" >&2
			echo "  got: ${label:-<none>}" >&2
			echo "  merging this would replace the label on a node's /${dir}" >&2
			exit 1
			;;
	esac
done

# --- Timestamps ------------------------------------------------------------
#
# Deterministic, and deliberately distinct per version.
#
# k0s skips re-staging its embedded payload into /var/lib/k0s/bin when the
# already-staged file's mtime equals the k0s executable's and its size matches
# (pkg/assets/stage.go). Normalising every extension to the same epoch -- the
# reflex for a reproducible build -- would manufacture exactly that collision,
# and the symptom is a node running a stale kubelet against a new control plane.
#
# Deriving the timestamp from the version keeps the build reproducible while
# making two versions disagree. The agent clears /var/lib/k0s/bin on a version
# change as well; this is the belt to that pair of braces, and it costs nothing.
epoch=$(( 1700000000 + 0x$(printf '%s' "${version}" | sha256sum | cut -c1-6) ))
find "${root}" -exec touch -h -d "@${epoch}" {} +

# --- Pack ------------------------------------------------------------------
mkdir -p "${OUT_DIR}"
image="${OUT_DIR}/${EXT_NAME}-${version}-${arch}.raw"

# -T pins the filesystem-level timestamps; the per-file mtimes set above are
# what k0s reads, and erofs preserves those.
mkfs.erofs -T "${epoch}" --quiet "${image}" "${root}"

echo "build-k0s-sysext: built ${image}"
echo "  version:      ${version}"
echo "  architecture: ${sysext_arch}"
echo "  sysext level: ${SYSEXT_LEVEL}"
echo "  size:         $(du -h "${image}" | cut -f1)"
