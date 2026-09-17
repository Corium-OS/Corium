#!/usr/bin/env bash
#
# Create a Corium VM on Proxmox that installs itself from the Anaconda ISO
# produced by `mise run artefact-anaconda-iso`.
#
# The ISO is unattended: Anaconda deploys the bootc image embedded in it with no
# prompts. Run this on the Proxmox node itself.
set -euo pipefail

VMID="${VMID:?set VMID}"
VM_NAME="${VM_NAME:-corium-${VMID}}"
ISO="${ISO:?set ISO (Proxmox volume id, e.g. local:iso/corium-install.iso)}"
CLOUD_CONFIG="${CLOUD_CONFIG:?set CLOUD_CONFIG (path to a cloud-config file)}"

STORAGE="${STORAGE:-local-lvm}"
SNIPPET_STORAGE="${SNIPPET_STORAGE:-local}"
BRIDGE="${BRIDGE:-vmbr0}"
CORES="${CORES:-4}"
MEMORY="${MEMORY:-8192}"
DISK_SIZE="${DISK_SIZE:-32}"

IP_CONFIG="${IP_CONFIG:-ip=dhcp}"
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
snippet_dir="/var/lib/vz/snippets"
mkdir -p "${snippet_dir}"
install -m 0644 "${CLOUD_CONFIG}" "${snippet_dir}/${snippet_name}"

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

qm set "${VMID}" --scsi0 "${STORAGE}:${DISK_SIZE}"
qm set "${VMID}" --ide0 "${ISO},media=cdrom"

# cloud-init still applies after the install: Anaconda lays down the OS, and
# cloud-init configures the node on its first real boot.
qm set "${VMID}" --ide2 "${STORAGE}:cloudinit"
qm set "${VMID}" --cicustom "user=${SNIPPET_STORAGE}:snippets/${snippet_name}"
qm set "${VMID}" --ipconfig0 "${IP_CONFIG}"

[[ -n "${NAMESERVER}" ]] && qm set "${VMID}" --nameserver "${NAMESERVER}"
[[ -n "${SEARCHDOMAIN}" ]] && qm set "${VMID}" --searchdomain "${SEARCHDOMAIN}"

# Disk FIRST, ISO second. This ordering is the whole trick.
#
# An empty disk has no UEFI boot entry, so the firmware falls through to the ISO
# and Anaconda installs. Once installed, the disk has an entry and wins, and the
# machine boots what was just installed.
#
# Put the ISO first and you get an install loop: the node reinstalls itself on
# every reboot, forever, looking exactly like a machine that will not come up.
qm set "${VMID}" --boot "order=scsi0;ide0"

echo
echo "VM ${VMID} (${VM_NAME}) created. Start it with:"
echo "  qm start ${VMID}"
echo
echo "The install is unattended and takes several minutes. Do not stop the VM"
echo "while it runs: Anaconda wipes the disk early, so an interrupted install"
echo "leaves nothing bootable behind."
