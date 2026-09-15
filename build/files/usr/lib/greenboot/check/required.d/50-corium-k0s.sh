#!/usr/bin/env bash
#
# Greenboot health check: is Kubernetes actually running on this node?
#
# If this fails on three consecutive boots, greenboot rolls the node back to the
# image it was running before. That makes the check's failure modes as important
# as its success: a check that is too strict rolls back healthy nodes, which is
# worse than not checking at all.
#
# So it only asserts what a broken image would genuinely break -- the k0s
# service is running and answering -- and deliberately does not require the node
# to be Ready in Kubernetes. A node can be legitimately NotReady for reasons
# that have nothing to do with the image: no CNI installed yet, a control plane
# that has not come back, a cluster-wide problem. Rolling back the OS would not
# fix any of those, and would take a healthy machine out of service while
# someone is already busy.
set -uo pipefail

readonly MARKER=/var/lib/corium/bootstrapped
readonly TIMEOUT=300
readonly INTERVAL=5

# A node that was never bootstrapped has no Kubernetes to check. This is the
# normal state for a machine provisioned without a corium block.
if [[ ! -e "${MARKER}" ]]; then
    echo "corium: node is not bootstrapped, nothing to check"
    exit 0
fi

# Whichever unit this node's role installed.
unit=""
for candidate in k0scontroller.service k0sworker.service; do
    if systemctl cat "${candidate}" &>/dev/null; then
        unit="${candidate}"
        break
    fi
done

if [[ -z "${unit}" ]]; then
    echo "corium: node is bootstrapped but no k0s unit exists" >&2
    exit 1
fi

# Give it time. Starting k0s, unpacking its supervised binaries and bringing up
# etcd is not instant, and greenboot runs early.
deadline=$((SECONDS + TIMEOUT))
while ((SECONDS < deadline)); do
    if systemctl is-active --quiet "${unit}" && k0s status &>/dev/null; then
        echo "corium: ${unit} is running and answering"
        exit 0
    fi

    sleep "${INTERVAL}"
done

echo "corium: ${unit} did not come up within ${TIMEOUT}s" >&2
systemctl status "${unit}" --no-pager --lines=20 2>&1 | sed 's/^/  /' >&2
exit 1
