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

export CORIUM_DIR="${WORK}/operator"

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
if "${CCTL}" --dir "${CORIUM_DIR}" status "${ADDRESS}" \
	--fingerprint "${FINGERPRINT}" >/dev/null 2>&1; then
	fail "an unenrolled node answered status; it must refuse until claimed"
fi
echo "unenrolled node refuses status, as it must"

step "an operator CA, and a certificate under it"
"${CCTL}" --dir "${CORIUM_DIR}" pki init
"${CCTL}" --dir "${CORIUM_DIR}" pki issue --role admin

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

"${CCTL}" --dir "${CORIUM_DIR}" enroll "${ADDRESS}" \
	--code "${CODE}" --fingerprint "${FINGERPRINT}" --config "${WORK}/node.yaml"

step "wait for the node to bootstrap and come back authenticated"
for attempt in $(seq 1 60); do
	if "${CCTL}" --dir "${CORIUM_DIR}" health "${ADDRESS}" >/dev/null 2>&1; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the node never answered an authenticated call"
	sleep 5
done
"${CCTL}" --dir "${CORIUM_DIR}" health "${ADDRESS}"

step "status reports what the configuration asked for"
STATUS="$("${CCTL}" --dir "${CORIUM_DIR}" status "${ADDRESS}")"
echo "${STATUS}"
grep -q "role *single" <<<"${STATUS}" || fail "status does not report role single"
grep -q "cluster *e2e" <<<"${STATUS}" ||
	fail "status does not name the cluster the applied configuration asked for, so the node bootstrapped with the document it booted with rather than the one it was sent"

step "a bootstrapped node refuses a new configuration"
if "${CCTL}" --dir "${CORIUM_DIR}" apply "${ADDRESS}" --file "${WORK}/node.yaml" >/dev/null 2>&1; then
	fail "apply succeeded on a bootstrapped node; it must refuse with 409"
fi
echo "refused, as it must"

step "services and journals"
"${CCTL}" --dir "${CORIUM_DIR}" services "${ADDRESS}"
"${CCTL}" --dir "${CORIUM_DIR}" logs "${ADDRESS}" --unit corium-apid --since 10m | tail -5

step "kubernetes is up, and the kubeconfig works"
for attempt in $(seq 1 60); do
	if "${CCTL}" --dir "${CORIUM_DIR}" kubeconfig "${ADDRESS}" > "${WORK}/kubeconfig" 2>/dev/null; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the node never produced a kubeconfig"
	sleep 5
done
grep -q "client-certificate-data" "${WORK}/kubeconfig" ||
	fail "the kubeconfig carries no client certificate"
echo "kubeconfig fetched, $(wc -c < "${WORK}/kubeconfig") bytes"

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

"${CCTL}" --dir "${CORIUM_DIR}" access ssh add "${ADDRESS}" \
	--user core --key-file "${WORK}/key.pub"
"${CCTL}" --dir "${CORIUM_DIR}" access ssh list "${ADDRESS}"

ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o BatchMode=yes \
	-o UserKnownHostsFile=/dev/null -p "${SSH_PORT:-2222}" \
	-i "${WORK}/key" "core@${SSH_HOST:-127.0.0.1}" 'echo logged in' ||
	fail "ssh was refused after the API granted the key. The mode or the SELinux label on /var/lib/corium/ssh is wrong, and sshd reports neither"

KEY_FINGERPRINT="$("${CCTL}" --dir "${CORIUM_DIR}" access ssh list "${ADDRESS}" |
	grep -oE 'SHA256:[A-Za-z0-9+/]+' | head -1)"
"${CCTL}" --dir "${CORIUM_DIR}" access ssh revoke "${ADDRESS}" \
	--key-fingerprint "${KEY_FINGERPRINT}"

if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o BatchMode=yes \
	-o UserKnownHostsFile=/dev/null -p "${SSH_PORT:-2222}" \
	-i "${WORK}/key" "core@${SSH_HOST:-127.0.0.1}" true 2>/dev/null; then
	fail "ssh still worked after the key was revoked"
fi
echo "revoked, and the door is shut"

printf '\n\033[32mEverything this script knows how to check, checked.\033[0m\n'
