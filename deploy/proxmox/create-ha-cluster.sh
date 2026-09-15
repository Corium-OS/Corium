#!/usr/bin/env bash
#
# Bootstrap a three-controller HA Corium cluster on Proxmox.
#
# Run on the Proxmox node. Creates three VMs, brings up the first so it can form
# the cluster, then joins the other two with a token it mints from the first.
#
# A k0s cluster has no pre-shared secret: the first controller generates the CA,
# and only then can it issue a join token.
#
# The joining controllers no longer have to wait for that in a script, though.
# They are configured with join.tokenFrom.waitFor, so they boot immediately,
# find no token, and wait for one to appear. All three machines start together;
# this script only has to deliver the token once it exists.
set -euo pipefail

DISK_IMAGE="${DISK_IMAGE:?set DISK_IMAGE (path to the qcow2)}"
SSH_KEY="${SSH_KEY:?set SSH_KEY (an ssh public key line)}"

CLUSTER_NAME="${CLUSTER_NAME:-corium}"
VIP="${VIP:?set VIP, e.g. 192.168.0.200}"
PREFIX="${PREFIX:-24}"
GATEWAY="${GATEWAY:?set GATEWAY, e.g. 192.168.0.1}"
NAMESERVER="${NAMESERVER:-$GATEWAY}"

# Three node addresses and their VM ids, index-aligned.
NODE_IPS=(${NODE_IPS:?set NODE_IPS, e.g. "192.168.0.201 192.168.0.202 192.168.0.203"})
VMIDS=(${VMIDS:?set VMIDS, e.g. "142 143 144"})

VRRP_ROUTER_ID="${VRRP_ROUTER_ID:-51}"

# Keepalived uses only the first eight characters, so this is generated at
# exactly that length; Corium rejects anything longer rather than letting
# controllers disagree about a password they all believe they set.
#
# Generated rather than defaulted: a shipped default password is one everybody
# keeps, and VRRP authentication exists to stop a stray host on the segment from
# claiming the virtual IP. It is generated once here and used by all three
# controllers, because they must agree on it.
VRRP_PASS="${VRRP_PASS:-$(LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | head -c 8)}"

if [[ ${#VRRP_PASS} -gt 8 ]]; then
	echo "VRRP_PASS is ${#VRRP_PASS} characters; keepalived uses only 8" >&2
	exit 1
fi

MEMORY="${MEMORY:-4096}"
CORES="${CORES:-2}"
DISK_SIZE="${DISK_SIZE:-32G}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# write_config <index> <output path>
write_config() {
	local index="$1" out="$2"

	cat > "${out}" <<EOF
#cloud-config
# Corium HA controller $((index + 1)) of ${#VMIDS[@]}.

corium:
  role: controller+worker

  cluster:
    name: ${CLUSTER_NAME}
    endpoint: ${VIP}

  ha:
    enabled: true
    virtualIP: ${VIP}/${PREFIX}
    virtualRouterID: ${VRRP_ROUTER_ID}
    authPass: ${VRRP_PASS}

  storage:
    type: etcd
EOF

	if [[ "${index}" -gt 0 ]]; then
		# A path rather than the token itself: the node waits for this file to
		# appear instead of needing it at boot.
		cat >> "${out}" <<EOF

  join:
    tokenFrom:
      file: /etc/corium/join-token
      waitFor: 20m
EOF
	fi

	cat >> "${out}" <<EOF

users:
  - name: core
    groups: [wheel]
    sudo: 'ALL=(ALL) NOPASSWD:ALL'
    shell: /bin/bash
    ssh_authorized_keys:
      - ${SSH_KEY}

ssh_pwauth: false
EOF
}

create_vm() {
	local index="$1"
	local vmid="${VMIDS[$index]}" ip="${NODE_IPS[$index]}"
	local config="/tmp/corium-ha-${vmid}.yaml"

	write_config "${index}" "${config}"

	if qm status "${vmid}" &>/dev/null; then
		qm stop "${vmid}" 2>/dev/null || true
		sleep 2
		qm destroy "${vmid}" --purge >/dev/null
	fi

	VMID="${vmid}" VM_NAME="${CLUSTER_NAME}-ctrl-$((index + 1))" \
		DISK_IMAGE="${DISK_IMAGE}" CLOUD_CONFIG="${config}" \
		IP_CONFIG="ip=${ip}/${PREFIX},gw=${GATEWAY}" NAMESERVER="${NAMESERVER}" \
		MEMORY="${MEMORY}" CORES="${CORES}" DISK_SIZE="${DISK_SIZE}" \
		bash "${here}/create-vm.sh" >/dev/null

	qm start "${vmid}" >/dev/null
	echo "started ${CLUSTER_NAME}-ctrl-$((index + 1)) (vm ${vmid}) at ${ip}"
}

wait_for_ping() {
	local ip="$1" deadline=$((SECONDS + 600))

	while ((SECONDS < deadline)); do
		ping -c1 -W1 "${ip}" >/dev/null 2>&1 && return 0
		sleep 5
	done

	echo "timed out waiting for ${ip}" >&2
	return 1
}

echo "==> starting all three controllers at once"
for index in 0 1 2; do
	create_vm "${index}"
done

for ip in "${NODE_IPS[@]}"; do
	wait_for_ping "${ip}"
done

echo "==> waiting for the first controller to serve the API"
echo "    (the other two are already up, waiting for their token)"
ssh -o StrictHostKeyChecking=no -o BatchMode=yes "core@${NODE_IPS[0]}" \
	'for i in $(seq 1 120); do sudo k0s kubectl get --raw /readyz >/dev/null 2>&1 && exit 0; sleep 5; done; exit 1'

echo "==> minting a controller join token"
# Short-lived on purpose: an unexpired controller token is a cluster-admin
# credential, since whoever holds it can join a full control-plane member.
token="$(ssh -o StrictHostKeyChecking=no -o BatchMode=yes "core@${NODE_IPS[0]}" \
	'sudo k0s token create --role=controller --expiry=1h' | tail -1 | tr -d '\n')"

echo "==> delivering it; the waiting controllers pick it up on their own"
for index in 1 2; do
	ssh -o StrictHostKeyChecking=no -o BatchMode=yes "core@${NODE_IPS[$index]}" \
		"sudo install -d -m 0700 /etc/corium && \
		 printf '%s' '${token}' | sudo tee /etc/corium/join-token >/dev/null && \
		 sudo chmod 0600 /etc/corium/join-token"
	echo "delivered to ${NODE_IPS[$index]}"
done

echo
echo "All three controllers are up. Check the cluster with:"
echo "  ssh core@${NODE_IPS[0]} sudo k0s kubectl get nodes"
echo "  ssh core@${NODE_IPS[0]} sudo k0s etcd member-list"
echo
echo "The API is reachable at https://${VIP}:6443 regardless of which"
echo "controller currently holds the virtual IP."
