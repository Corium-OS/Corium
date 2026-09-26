#!/usr/bin/env bash
#
# Greenboot health check: is the k0s system extension actually merged?
#
# This exists for one failure mode, and it is the quietest one in the design.
# An extension declares what it fits, and a node that no longer fits it does not
# fail: systemd simply does not merge it, and the node comes back running the
# floor version its image ships. Kubernetes is up, the node is Ready, and it is
# running a version nobody asked for. Nothing says so.
#
# The way a node reaches that state without anyone doing anything is an OS
# upgrade. /var survives a rebase by design, so the extension installed before it
# is still sitting in the slot afterwards, matched against an os-release that has
# moved underneath it. See docs/adr/0010-kubernetes-version-axis.md.
#
# That is also why failing here is the right thing rather than merely a loud one:
# if greenboot rolls the node back, it rolls it back to the image the extension
# *did* fit, which is a real repair and not a reboot in the hope of something
# changing.
set -uo pipefail

readonly SLOT=/var/lib/extensions/corium-k0s.raw
readonly RELEASE=/usr/lib/extension-release.d/extension-release.corium-k0s

# No extension in the slot means this node runs its image's floor. That is an
# ordinary state -- it is what every node does until it is told otherwise -- and
# there is nothing here to be wrong about.
if [[ ! -e "${SLOT}" ]]; then
    echo "corium: no k0s extension installed, running the image's floor version"
    exit 0
fi

# An extension is installed and merged: the metadata is only readable through
# the overlay, so its presence *is* the proof that the merge happened.
if [[ -r "${RELEASE}" ]]; then
    version="$(sed -n 's/^CORIUM_K0S_VERSION=//p' "${RELEASE}")"
    echo "corium: k0s extension merged (${version:-version not declared})"
    exit 0
fi

# Installed but not merged. Deliberately not tolerated: unlike a NotReady node,
# which can be NotReady for reasons the image cannot fix, this one is running
# something other than what it was configured to run, and the previous image is
# where it worked.
echo "corium: a k0s extension is installed but was not merged into /usr" >&2
echo "  the node is running the floor version instead of the one it was configured with" >&2
echo >&2

# The likely cause is worth printing rather than making somebody derive it: an
# extension is matched on ID and SYSEXT_LEVEL, and an OS rebase is what moves
# those out from under an extension that is already installed.
echo "  installed: $(readlink -f "${SLOT}" 2>/dev/null || echo "${SLOT}")" >&2
echo "  this image: $(sed -n 's/^ID=//p' /etc/os-release) \
sysext level $(sed -n 's/^SYSEXT_LEVEL=//p' /etc/os-release)" >&2

systemd-sysext status --no-pager 2>&1 | sed 's/^/  /' >&2

exit 1
