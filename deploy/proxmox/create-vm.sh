#!/usr/bin/env bash
#
# Create a Corium VM on a Proxmox host from a qcow2 produced by
# bootc-image-builder.
#
# Run this on the Proxmox node itself. It is idempotent only in the sense that
# it refuses to touch an existing VM: destroy it yourself first.
set -euo pipefail

VMID="${VMID:?set VMID}"
VM_NAME="${VM_NAME:-corium-${VMID}}"
DISK_IMAGE="${DISK_IMAGE:?set DISK_IMAGE (path to the qcow2)}"
CLOUD_CONFIG="${CLOUD_CONFIG:?set CLOUD_CONFIG (path to a cloud-config file)}"

STORAGE="${STORAGE:-local-lvm}"
SNIPPET_STORAGE="${SNIPPET_STORAGE:-local}"
BRIDGE="${BRIDGE:-vmbr0}"
CORES="${CORES:-4}"
MEMORY="${MEMORY:-8192}"
DISK_SIZE="${DISK_SIZE:-32G}"

# Networking. Leave IP_CONFIG as dhcp, or set it to the Proxmox ipconfig syntax:
#   IP_CONFIG="ip=192.168.0.190/24,gw=192.168.0.1"
IP_CONFIG="${IP_CONFIG:-ip=dhcp}"

# Proxmox's ipconfig0 carries an address and a gateway, and nothing else. A
# statically addressed node therefore comes up with no resolver at all unless
# one is set here, and the failure is confusing: the node is pingable, SSH
# works, and Kubernetes hangs pulling images with "lookup quay.io: Try again".
# DHCP supplies a resolver on its own, so this only matters for static setups.
NAMESERVER="${NAMESERVER:-}"
SEARCHDOMAIN="${SEARCHDOMAIN:-}"

if [[ "${IP_CONFIG}" != *"ip=dhcp"* && -z "${NAMESERVER}" ]]; then
	echo "warning: static IP_CONFIG with no NAMESERVER; the node will have no DNS" >&2
fi

if qm status "${VMID}" &>/dev/null; then
	echo "VM ${VMID} already exists; destroy it first" >&2
	exit 1
fi

snippet_name="corium-${VMID}.yaml"
snippet_dir="$(pvesm path "${SNIPPET_STORAGE}:snippets/${snippet_name}" 2>/dev/null || true)"
snippet_dir="${snippet_dir%/*}"
snippet_dir="${snippet_dir:-/var/lib/vz/snippets}"

mkdir -p "${snippet_dir}"
install -m 0644 "${CLOUD_CONFIG}" "${snippet_dir}/${snippet_name}"
echo "installed cloud-config at ${snippet_dir}/${snippet_name}"

# UEFI, because that is what the Fedora bootc images are built for. The VM gets
# no pre-enrolled Secure Boot keys: enrolling them without also signing every
# kernel module the node loads produces a machine that boots until it does not.
qm create "${VMID}" \
	--name "${VM_NAME}" \
	--cores "${CORES}" \
	--memory "${MEMORY}" \
	--cpu host \
	--machine q35 \
	--bios ovmf \
	--efidisk0 "${STORAGE}:1,efitype=4m,pre-enrolled-keys=0" \
	--scsihw virtio-scsi-single \
	--net0 "virtio,bridge=${BRIDGE}" \
	--serial0 socket \
	--agent enabled=1 \
	--ostype l26

echo "importing ${DISK_IMAGE} into ${STORAGE}"
qm disk import "${VMID}" "${DISK_IMAGE}" "${STORAGE}" --format qcow2

# The imported disk lands unused; attach it and make it the boot device.
imported="$(qm config "${VMID}" | awk -F': ' '/^unused[0-9]+:/ {print $2; exit}')"
if [[ -z "${imported}" ]]; then
	echo "could not find the imported disk in the VM config" >&2
	exit 1
fi

qm set "${VMID}" --scsi0 "${imported}"
qm set "${VMID}" --boot order=scsi0
qm disk resize "${VMID}" scsi0 "${DISK_SIZE}"

# Proxmox builds a NoCloud seed drive from the snippet. cicustom replaces the
# generated user-data wholesale, which is what lets the corium block through --
# the generated one would only carry users and SSH keys.
qm set "${VMID}" --ide2 "${STORAGE}:cloudinit"
qm set "${VMID}" --cicustom "user=${SNIPPET_STORAGE}:snippets/${snippet_name}"
qm set "${VMID}" --ipconfig0 "${IP_CONFIG}"

if [[ -n "${NAMESERVER}" ]]; then
	qm set "${VMID}" --nameserver "${NAMESERVER}"
fi

if [[ -n "${SEARCHDOMAIN}" ]]; then
	qm set "${VMID}" --searchdomain "${SEARCHDOMAIN}"
fi

echo
echo "VM ${VMID} (${VM_NAME}) created. Start it with:"
echo "  qm start ${VMID}"
echo "Watch the console with:"
echo "  qm terminal ${VMID}"
