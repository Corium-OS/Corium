#!/usr/bin/env bash
#
# Drive a running Corium node with cctl, and fail on the first thing that lies.
#
# This is the half of the end-to-end test that has nothing to do with CI: given
# an address and a freshly booted node in maintenance mode, it exercises what an
# operator actually does. It is a script rather than workflow steps so it can be
# run by hand against a VM on a desk, which is where it was first written.
#
#   ADDRESS=192.168.1.51:7443 .github/e2e/drive-a-node.sh
#
# Every check says what it expected. A test suite whose failure output is
# "exit 1" costs more time than it saves.
set -euo pipefail

ADDRESS="${ADDRESS:?set ADDRESS to host:port of the management API}"
CCTL="${CCTL:-cctl}"
WORK="${WORK:-$(mktemp -d)}"

# cctl keeps the operator CA and the client certificate in ~/.corium, and takes
# --dir per command rather than globally. Moving HOME is both shorter and
# stricter: nothing this script does can reach the real one, and a stray
# fingerprint cannot be remembered outside the run.
export HOME="${WORK}"
CORIUM_DIR="${HOME}/.corium"
mkdir -p "${HOME}"

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$1" >&2; exit 1; }

# cctl pins a node by the fingerprint of the certificate it serves, not by
# hostname, which is what lets this work through a port forward: the address is
# 127.0.0.1 while the identity checked is still that of the node.
fingerprint() {
	openssl s_client -connect "${ADDRESS}" </dev/null 2>/dev/null |
		openssl x509 -outform DER 2>/dev/null |
		openssl dgst -sha256 -binary |
		base64 | tr -d '=' | sed 's/^/SHA256:/'
}

step "the node is serving, and unclaimed"
FINGERPRINT="$(fingerprint)"
[ -n "${FINGERPRINT#SHA256:}" ] || fail "no certificate on ${ADDRESS}"
echo "fingerprint ${FINGERPRINT}"

# An unenrolled node must refuse everything but enrolment. This is the property
# the whole trust model rests on, so it is checked before anything else.
if "${CCTL}" status "${ADDRESS}" \
	--fingerprint "${FINGERPRINT}" >/dev/null 2>&1; then
	fail "an unenrolled node answered status; it must refuse until claimed"
fi
echo "unenrolled node refuses status, as it must"

step "an operator CA, and a certificate under it"
"${CCTL}" pki init
"${CCTL}" pki issue --role admin

step "claim the node and tell it what to be, in one call"
cat > "${WORK}/node.yaml" <<YAML
corium:
  role: single
  cluster:
    name: e2e
  api:
    operatorCA: |
$(sed 's/^/      /' "${CORIUM_DIR}/operator-ca.pem")
YAML

CODE="$(grep -oE '[0-9A-Z]{4}-[0-9A-Z]{4}' "${CONSOLE:-/dev/null}" | tail -1 || true)"
if [ -z "${CODE}" ]; then
	fail "no pairing code in the console log at ${CONSOLE:-unset}. A node waiting to be claimed prints one, and without it this test cannot prove that enrolment is authenticated at all"
fi
echo "pairing code from the console: ${CODE}"

"${CCTL}" enroll "${ADDRESS}" \
	--code "${CODE}" --fingerprint "${FINGERPRINT}" --config "${WORK}/node.yaml"

step "wait for the node to bootstrap and come back authenticated"
for attempt in $(seq 1 60); do
	if "${CCTL}" health "${ADDRESS}" >/dev/null 2>&1; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the node never answered an authenticated call"
	sleep 5
done
"${CCTL}" health "${ADDRESS}"

step "status reports what the configuration asked for"
STATUS="$("${CCTL}" status "${ADDRESS}")"
echo "${STATUS}"
grep -q "role *single" <<<"${STATUS}" || fail "status does not report role single"
grep -q "cluster *e2e" <<<"${STATUS}" ||
	fail "status does not name the cluster the applied configuration asked for, so the node bootstrapped with the document it booted with rather than the one it was sent"

step "a bootstrapped node accepts the document it is already running"
# Re-applying on a bootstrapped node used to be a flat refusal, and this script
# checked for one. ADR 8 changed that: the safe subset is re-applied in place,
# and a document identical to what the node runs is reported as unchanged
# rather than refused. The rule is enforced by the node, not by cctl, so this
# is the node being asked.
APPLIED="$("${CCTL}" apply "${ADDRESS}" --file "${WORK}/node.yaml")" ||
	fail "apply of the document the node is already running was refused; since ADR 8 it must be accepted, and reported as unchanged"
echo "${APPLIED}"
grep -q "No change on" <<<"${APPLIED}" ||
	fail "apply of the running document was accepted but not reported as unchanged, so the node either rewrote something or misread its own baseline"

step "a bootstrapped node refuses a change to what it is"
# The other half of ADR 8. An identity field -- here the cluster name -- cannot
# change on a node in service; the answer is a 409 that names the field and
# points at cctl reset, so an operator sees which part of their document is the
# problem rather than that there was one.
sed 's/^    name: e2e$/    name: somewhere-else/' "${WORK}/node.yaml" \
	> "${WORK}/other-cluster.yaml"
grep -q "somewhere-else" "${WORK}/other-cluster.yaml" ||
	fail "this script could not rewrite the cluster name in its own document; the heredoc above changed shape"
if REFUSAL="$("${CCTL}" apply "${ADDRESS}" --file "${WORK}/other-cluster.yaml" 2>&1)"; then
	fail "apply changed the cluster of a bootstrapped node; it must refuse with 409 and point at cctl reset"
fi
echo "${REFUSAL}"
grep -q "409" <<<"${REFUSAL}" ||
	fail "the refusal was not a 409; a node that will not make a change must say so as a conflict, not as an error in the request"
grep -q "cluster" <<<"${REFUSAL}" ||
	fail "the refusal does not name the field it refused (cluster)"
echo "refused, naming the field, as it must"

step "services and journals"
"${CCTL}" services "${ADDRESS}"
"${CCTL}" logs "${ADDRESS}" --unit corium-apid --since 10m | tail -5

step "kubernetes is up, and the kubeconfig works"
for attempt in $(seq 1 60); do
	if "${CCTL}" kubeconfig "${ADDRESS}" > "${WORK}/kubeconfig" 2>/dev/null; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the node never produced a kubeconfig"
	sleep 5
done
grep -q "client-certificate-data" "${WORK}/kubeconfig" ||
	fail "the kubeconfig carries no client certificate"
echo "kubeconfig fetched, $(wc -c < "${WORK}/kubeconfig") bytes"

step "kubernetes reports the node Ready"
# The cheapest end-to-end signal there is, and the one the release checklist
# names first. Everything above proves the API answers; this proves the thing
# the machine exists to be actually came up. A service in `running` says systemd
# started a process, not that a cluster works.
#
# Refetched pointed at wherever the operator can reach the control plane, which
# is a forwarded port when this runs in CI and the node's own address when it
# runs against a VM on a desk.
"${CCTL}" kubeconfig "${ADDRESS}" --server "${K8S_SERVER:-127.0.0.1:6443}" \
	> "${WORK}/kubeconfig"
export KUBECONFIG="${WORK}/kubeconfig"

for attempt in $(seq 1 60); do
	if kubectl get nodes --no-headers 2>/dev/null | grep -qE '\sReady\s'; then
		break
	fi
	[ "${attempt}" -lt 60 ] ||
		fail "no node reached Ready in five minutes. $(kubectl get nodes 2>&1 | tail -3)"
	sleep 5
done

kubectl get nodes
kubectl get --raw /readyz

step "the Kubernetes version is an axis of its own"
# ADR 10. Everything above proves the node runs the Kubernetes its image
# shipped; this proves the version is separable from the image -- the node
# pulls a signed extension, swaps the binary underneath k0s, and comes back on
# it, without a new image and without a reboot.
#
# The versions are read from build/k0s.lock rather than written here, so a
# routine bump of the floor does not turn into a failing test that looks like a
# regression. The target is another release on the same minor as the floor,
# which is a legal move under k0s's skew rules.
LOCK="${LOCK:-$(dirname "$0")/../../build/k0s.lock}"

if [ ! -r "${LOCK}" ]; then
	fail "cannot read ${LOCK}; this step needs to know which versions the image supports"
fi

FLOOR="$(awk -F= '/^K0S_FLOOR=/ { print $2 }' "${LOCK}")"
[ -n "${FLOOR}" ] || fail "no K0S_FLOOR in ${LOCK}"

# Same major.minor as the floor, different release. Sorted so the choice is
# deterministic rather than whatever order the file happens to be in.
MINOR="$(cut -d. -f1,2 <<<"${FLOOR}")"
TARGET="$(awk -v m="${MINOR}." -v f="${FLOOR}" \
	'!/^#/ && NF == 3 && $1 != f && index($1, m) == 1 { print $1 }' "${LOCK}" |
	sort -u | tail -1)"

if [ -z "${TARGET}" ]; then
	# Not a failure: a window can legitimately hold one release per minor, and
	# a test that invented a version to move to would be testing nothing.
	echo "no second release on ${MINOR} in the window, so there is nothing safe to move to; skipping"
else
	echo "floor is ${FLOOR}, moving to ${TARGET}"

	RUNNING="$("${CCTL}" status "${ADDRESS}" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+' | head -1)"
	[ "${RUNNING}" = "${FLOOR}" ] ||
		fail "the node reports ${RUNNING:-nothing} but its image's floor is ${FLOOR}; this step's premise is wrong before it starts"

	# --- the refusals, which cost nothing and happen before anything moves ---

	# Built by insertion rather than with `sed a\`, whose continuation syntax
	# differs between GNU and BSD -- and this script is meant to be runnable
	# from a desk as well as from CI.
	with_version() {
		{
			head -1 "${WORK}/node.yaml"
			printf '  kubernetes:\n    version: %s\n' "$1"
			tail -n +2 "${WORK}/node.yaml"
		} > "$2"
	}

	# A version no image supports. Refused against the window, not attempted and
	# discovered as a 404.
	with_version v1.99.0+k0s.0 "${WORK}/outside-window.yaml"
	if OUT="$("${CCTL}" apply "${ADDRESS}" --file "${WORK}/outside-window.yaml" 2>&1)"; then
		fail "the node accepted a Kubernetes version outside the window its image supports"
	fi
	grep -qi "window" <<<"${OUT}" ||
		fail "a version outside the window was refused without saying so: ${OUT}"
	echo "refused a version outside the window"

	# --- the change itself ---

	with_version "${TARGET}" "${WORK}/new-version.yaml"
	grep -q "${TARGET}" "${WORK}/new-version.yaml" ||
		fail "this script could not write the version into its own document"

	# Minutes, not seconds: a quarter of a gigabyte to pull, then a drain.
	CHANGED="$("${CCTL}" apply "${ADDRESS}" --file "${WORK}/new-version.yaml")" ||
		fail "the node refused to move from ${FLOOR} to ${TARGET}, which is a legal change within one minor"
	echo "${CHANGED}"

	grep -q "kubernetes" <<<"${CHANGED}" ||
		fail "the apply did not report re-applying kubernetes"
	grep -q "${TARGET}" <<<"${CHANGED}" ||
		fail "the apply reported success without naming ${TARGET} as what is running; the node must not claim a version it is not on"

	# Asked of the node rather than believed from the apply, because the two
	# disagreeing is exactly the failure this whole design guards against: an
	# extension that does not merge leaves the node on its floor with
	# everything else looking fine.
	AFTER="$("${CCTL}" status "${ADDRESS}" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+' | head -1)"
	[ "${AFTER}" = "${TARGET}" ] ||
		fail "the node reports ${AFTER:-nothing} after being moved to ${TARGET}"
	echo "the node is running ${AFTER}"

	# And the cluster survived it. A version swap that leaves a node NotReady
	# has done the thing without achieving it.
	for attempt in $(seq 1 60); do
		if kubectl get nodes --no-headers 2>/dev/null | grep -qE '\sReady\s'; then
			break
		fi
		[ "${attempt}" -lt 60 ] ||
			fail "the node did not return to Ready after the version change. $(kubectl get nodes 2>&1 | tail -3)"
		sleep 5
	done
	kubectl get nodes
fi

step "ssh access, granted and taken away over the API"
ssh-keygen -t ed25519 -f "${WORK}/key" -N '' -C 'e2e' -q

# The node has no account but the one cloud-init made, and it was given no key,
# so this must fail before the API grants one. Without this the next check
# proves nothing.
if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o BatchMode=yes \
	-o UserKnownHostsFile=/dev/null -p "${SSH_PORT:-2222}" \
	-i "${WORK}/key" "core@${SSH_HOST:-127.0.0.1}" true 2>/dev/null; then
	fail "ssh worked before any key was granted"
fi
echo "ssh refused before the key is granted, as it must be"

"${CCTL}" access ssh add "${ADDRESS}" \
	--user core --key-file "${WORK}/key.pub"
"${CCTL}" access ssh list "${ADDRESS}"

ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o BatchMode=yes \
	-o UserKnownHostsFile=/dev/null -p "${SSH_PORT:-2222}" \
	-i "${WORK}/key" "core@${SSH_HOST:-127.0.0.1}" 'echo logged in' ||
	fail "ssh was refused after the API granted the key. The mode or the SELinux label on /var/lib/corium/ssh is wrong, and sshd reports neither"

KEY_FINGERPRINT="$("${CCTL}" access ssh list "${ADDRESS}" |
	grep -oE 'SHA256:[A-Za-z0-9+/]+' | head -1)"
"${CCTL}" access ssh revoke "${ADDRESS}" \
	--key-fingerprint "${KEY_FINGERPRINT}"

if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o BatchMode=yes \
	-o UserKnownHostsFile=/dev/null -p "${SSH_PORT:-2222}" \
	-i "${WORK}/key" "core@${SSH_HOST:-127.0.0.1}" true 2>/dev/null; then
	fail "ssh still worked after the key was revoked"
fi
echo "revoked, and the door is shut"

printf '\n\033[32mEverything this script knows how to check, checked.\033[0m\n'
