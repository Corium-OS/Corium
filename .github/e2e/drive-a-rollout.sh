#!/usr/bin/env bash
#
# Drive a two-node cluster through Kubernetes rollouts that forget the worker
# (ADR 12): the first is allowed with a warning, the next minor is refused.
#
#   CTRL=10.0.0.10:7443 WORKER=10.0.0.11:7443 \
#   CTRL_CONSOLE=ctrl.log WORKER_CONSOLE=worker.log .github/e2e/drive-a-rollout.sh
#
# Both nodes must be freshly booted and waiting to be claimed.
set -euo pipefail

CTRL="${CTRL:?set CTRL to host:port of the controller API}"
WORKER="${WORKER:?set WORKER to host:port of the worker API}"
CCTL="${CCTL:-cctl}"
WORK="${WORK:-$(mktemp -d)}"
LOCK="${LOCK:-$(dirname "$0")/../../build/k0s.lock}"

export HOME="${WORK}"
CORIUM_DIR="${HOME}/.corium"
mkdir -p "${HOME}"

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# The consoles show the boot, not the journal, so a failure dumps what each node
# can still say about itself over the API.
diagnose() {
	echo "-- nodes as the API server sees them"
	kubectl get nodes -o wide 2>&1 || true

	for node in "${CTRL}" "${WORKER}"; do
		printf '\n\033[1m-- %s\033[0m\n' "${node}"
		"${CCTL}" status "${node}" 2>&1 || true
		"${CCTL}" services "${node}" 2>&1 || true
		echo "-- kubelet starts"
		"${CCTL}" logs "${node}" --unit k0sworker --since 1h --lines 100000 2>&1 |
			grep -i -E 'kubelet version|starting kubelet|staging|stage' | cut -c 1-300 || true
		for unit in corium-bootstrap corium-apid k0scontroller k0sworker; do
			echo "-- ${unit}"
			"${CCTL}" logs "${node}" --unit "${unit}" --since 1h 2>&1 | tail -60 || true
		done
	done
}

fail() {
	printf '\033[31mFAIL: %s\033[0m\n' "$1" >&2
	exit 1
}

# On any failure, including a command that stopped the script through set -e.
on_exit() {
	local status=$?
	[ "${status}" -eq 0 ] || diagnose
}
trap on_exit EXIT

fingerprint() {
	openssl s_client -connect "$1" </dev/null 2>/dev/null |
		openssl x509 -outform DER 2>/dev/null |
		openssl dgst -sha256 -binary |
		base64 | tr -d '=' | sed 's/^/SHA256:/'
}

pairing_code() {
	grep -oE '[0-9A-Z]{4}-[0-9A-Z]{4}' "$1" | tail -1 || true
}

running_version() {
	"${CCTL}" status "$1" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+' | head -1
}

wait_for() {
	local what="$1" tries="$2"
	shift 2
	for attempt in $(seq 1 "${tries}"); do
		if "$@" >/dev/null 2>&1; then
			return 0
		fi
		[ "${attempt}" -lt "${tries}" ] || fail "${what}"
		sleep 5
	done
}

operator_ca() {
	printf '  api:\n    operatorCA: |\n'
	sed 's/^/      /' "${CORIUM_DIR}/operator-ca.pem"
}

step "three minors from the window"
# The latest release of each minor. Three are needed: a start, a minor the
# worker is forgotten at, and the one that would leave it two behind.
mapfile -t VERSIONS < <(awk '!/^#/ && NF == 3 { print $1 }' "${LOCK}" | sort -uV |
	awk -F. '{ latest[$1"."$2] = $0 } END { for (m in latest) print latest[m] }' | sort -V)
[ "${#VERSIONS[@]}" -ge 3 ] || fail "the window in ${LOCK} holds fewer than three minors"
LOW="${VERSIONS[0]}" MID="${VERSIONS[1]}" TOP="${VERSIONS[2]}"
echo "start on ${LOW}, then ${MID}, then ${TOP}"

step "an operator CA"
"${CCTL}" pki init
"${CCTL}" pki issue --role admin

step "the controller, on ${LOW}"
{
	printf 'corium:\n  role: controller\n  cluster:\n    name: e2e-rollout\n'
	printf '  node:\n    name: ctrl\n  kubernetes:\n    version: %s\n' "${LOW}"
	operator_ca
} > "${WORK}/ctrl.yaml"

CODE="$(pairing_code "${CTRL_CONSOLE}")"
[ -n "${CODE}" ] || fail "no pairing code on the controller's console"
"${CCTL}" enroll "${CTRL}" --code "${CODE}" \
	--fingerprint "$(fingerprint "${CTRL}")" --config "${WORK}/ctrl.yaml"

wait_for "the controller never answered an authenticated call" 60 "${CCTL}" health "${CTRL}"
wait_for "the controller never produced a kubeconfig" 120 "${CCTL}" kubeconfig "${CTRL}"
"${CCTL}" kubeconfig "${CTRL}" --server "${CTRL%:*}:6443" > "${WORK}/kubeconfig"
export KUBECONFIG="${WORK}/kubeconfig"

step "the worker, joined on ${LOW}"
# The kubeconfig is a file k0s writes early; minting a token needs k0s itself
# up, which can take a little longer.
for attempt in $(seq 1 60); do
	if "${CCTL}" worker-config "${CTRL}" --name worker > "${WORK}/worker.yaml" 2>"${WORK}/worker-config.err"; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the controller never minted a worker token: $(cat "${WORK}/worker-config.err")"
	sleep 5
done
# worker-config carries no cluster name, and a worker without one reports the
# default, which the rollout preflight refuses as another cluster.
{
	printf '  cluster:\n    name: e2e-rollout\n'
	printf '  kubernetes:\n    version: %s\n' "${LOW}"
	operator_ca
} >> "${WORK}/worker.yaml"

CODE="$(pairing_code "${WORKER_CONSOLE}")"
[ -n "${CODE}" ] || fail "no pairing code on the worker's console"
"${CCTL}" enroll "${WORKER}" --code "${CODE}" \
	--fingerprint "$(fingerprint "${WORKER}")" --config "${WORK}/worker.yaml"

wait_for "the worker never answered an authenticated call" 60 "${CCTL}" health "${WORKER}"

for attempt in $(seq 1 120); do
	if kubectl get node worker --no-headers 2>/dev/null | grep -qE '\sReady\s'; then
		break
	fi
	[ "${attempt}" -lt 120 ] || fail "the worker never became Ready. $(kubectl get nodes 2>&1 | tail -3)"
	sleep 5
done
kubectl get nodes -o wide

step "a rollout that forgets the worker is allowed, and says so"
OUT="$("${CCTL}" k8s upgrade --version "${MID}" --controllers "${CTRL}" 2>&1)" ||
	fail "moving the controller alone to ${MID} was refused; the worker one minor behind is legal. ${OUT}"
echo "${OUT}"
grep -q "warning: worker" <<<"${OUT}" ||
	fail "the rollout did not warn about the worker it was not given"
[ "$(running_version "${CTRL}")" = "${MID}" ] || fail "the controller is not on ${MID}"

step "the next minor is refused, naming the worker"
if OUT="$("${CCTL}" k8s upgrade --version "${TOP}" --controllers "${CTRL}" 2>&1)"; then
	fail "the controller moved to ${TOP} with the worker still on ${LOW}"
fi
echo "${OUT}"
grep -q "worker (v" <<<"${OUT}" || fail "the refusal does not name the worker"
grep -q "skew rules" <<<"${OUT}" || fail "the refusal does not name the rule"
[ "$(running_version "${CTRL}")" = "${MID}" ] || fail "the controller moved despite the refusal"

step "with the worker caught up, the next minor is accepted"
"${CCTL}" k8s upgrade --version "${MID}" --controllers "${CTRL}" --workers "${WORKER}" ||
	fail "the worker could not catch up to ${MID}"
[ "$(running_version "${WORKER}")" = "${MID}" ] || fail "the worker is not on ${MID}"

# The controller reads the worker's version from its Node object, which the
# restarted kubelet updates a few seconds after the node reports it moved.
MID_MINOR="$(cut -d. -f1,2 <<<"${MID}")"
for attempt in $(seq 1 60); do
	if kubectl get node worker -o jsonpath='{.status.nodeInfo.kubeletVersion}' 2>/dev/null |
		grep -q "^${MID_MINOR}\."; then
		break
	fi
	[ "${attempt}" -lt 60 ] || fail "the worker's Node never reported ${MID_MINOR}"
	sleep 5
done

OUT="$("${CCTL}" k8s upgrade --version "${TOP}" --controllers "${CTRL}" --workers "${WORKER}" --dry-run 2>&1)" ||
	fail "a complete rollout to ${TOP} was refused. ${OUT}"
echo "${OUT}"
if grep -q "warning:" <<<"${OUT}"; then
	fail "warned about a worker the rollout was given"
fi

printf '\n\033[32mThe rollout noticed the worker it was not given.\033[0m\n'
