// Package config defines the Corium configuration surface: the `corium:` block
// embedded in a cloud-config document, its defaults, and its validation rules.
//
// The schema is deliberately small. It covers the configurations that most
// operators need most of the time; anything beyond that is reached through the
// escape hatches (K0s.Patch, and raw cloud-init modules) rather than by growing
// another key here. A configuration key added to this file is permanent, ships
// to every node, and must be supported forever — add them reluctantly.
package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Role describes what a node does in the cluster.
type Role string

const (
	// RoleSingle is a self-contained, single-node cluster. It cannot be
	// expanded later: k0s provisions it with SQLite storage and without the
	// machinery multi-node clusters need.
	RoleSingle Role = "single"

	// RoleController runs the control plane only. It schedules no workloads.
	RoleController Role = "controller"

	// RoleControllerWorker runs the control plane and also accepts workloads.
	// This is the pragmatic choice for small clusters that must remain
	// expandable.
	RoleControllerWorker Role = "controller+worker"

	// RoleWorker runs workloads and joins an existing control plane.
	RoleWorker Role = "worker"
)

// Config is the root of the `corium:` block.
type Config struct {
	// Role determines what this node becomes. It is the only required field.
	Role Role `yaml:"role" json:"role"`

	// Cluster carries cluster-wide identity and reachability settings.
	Cluster Cluster `yaml:"cluster,omitempty" json:"cluster,omitempty"`

	// Network configures pod and service addressing and the CNI.
	Network Network `yaml:"network,omitempty" json:"network,omitempty"`

	// Storage selects the control plane datastore. Ignored for workers.
	Storage Storage `yaml:"storage,omitempty" json:"storage,omitempty"`

	// Join describes how this node authenticates to an existing cluster.
	// Required for workers and for controllers joining an existing control
	// plane; meaningless for the node that bootstraps the cluster.
	Join Join `yaml:"join,omitempty" json:"join,omitempty"`

	// Node carries kubelet-level attributes for this machine.
	Node Node `yaml:"node,omitempty" json:"node,omitempty"`

	// Addons are Helm charts installed at bootstrap through the k0s Helm
	// extensions mechanism. No Helm binary or in-cluster operator is involved.
	Addons []Addon `yaml:"addons,omitempty" json:"addons,omitempty"`

	// HA configures a highly available control plane.
	HA HA `yaml:"ha,omitempty" json:"ha,omitempty"`

	// RAID declares software RAID arrays assembled from this node's spare
	// disks. It does not cover the disk the OS booted from: see the RAIDArray
	// documentation.
	RAID []RAIDArray `yaml:"raid,omitempty" json:"raid,omitempty"`

	// ZFS declares ZFS pools built from this node's data disks, on the same
	// footing as RAID: created by corium-agent at first boot, before k0s. It
	// does not cover the disk the OS booted from. See the ZFSPool documentation
	// and docs/adr/0007-zfs-data-disks.md.
	ZFS []ZFSPool `yaml:"zfs,omitempty" json:"zfs,omitempty"`

	// WireGuard declares host WireGuard interfaces, brought up before k0s so a
	// cluster can run over an encrypted overlay between hosts. See the
	// WireGuardInterface documentation and docs/adr/0006-host-wireguard-overlay.md.
	WireGuard []WireGuardInterface `yaml:"wireguard,omitempty" json:"wireguard,omitempty"`

	// Upgrades controls whether the node updates itself.
	Upgrades Upgrades `yaml:"upgrades,omitempty" json:"upgrades,omitempty"`

	// API configures the node's management API. It is off unless asked for.
	API API `yaml:"api,omitempty" json:"api,omitempty"`

	// K0s is the escape hatch to the underlying k0s configuration.
	K0s K0s `yaml:"k0s,omitempty" json:"k0s,omitempty"`
}

// HA configures a highly available control plane.
//
// The hard part of an HA control plane is not running three API servers, it is
// giving clients one address that survives losing any one of them. The usual
// answer is an external load balancer, which is a second thing to build and
// keep alive before the cluster exists.
//
// Corium uses k0s's built-in control plane load balancing instead: the
// controllers elect a leader over VRRP and one of them holds a virtual IP. No
// external load balancer, and nothing to provision ahead of the cluster.
//
// Note what is deliberately absent: certificates. Controllers two and three
// join with a token, and k0s ships them the cluster CA over its join API. No
// PKI material ever appears in a Corium configuration.
type HA struct {
	// Enabled turns on control plane load balancing.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// VirtualIP is the address clients use to reach the control plane,
	// in CIDR form: "192.168.0.200/24". Whichever controller currently holds
	// the VRRP election answers on it.
	VirtualIP string `yaml:"virtualIP,omitempty" json:"virtualIP,omitempty"`

	// Interface carries the VRRP advertisements. Left empty, k0s picks the
	// interface holding the default route, which is right on most machines.
	Interface string `yaml:"interface,omitempty" json:"interface,omitempty"`

	// VirtualRouterID identifies this VRRP group, 1-255. It must be unique
	// within the broadcast domain: two clusters sharing an ID on the same
	// segment will fight over each other's elections.
	VirtualRouterID int `yaml:"virtualRouterID,omitempty" json:"virtualRouterID,omitempty"`

	// AuthPass authenticates VRRP advertisements between controllers. It must
	// be identical on every controller. Keepalived truncates it to eight
	// characters, so anything longer is silently shortened -- Corium rejects
	// it rather than letting two nodes disagree about a password they both
	// think they set.
	AuthPass string `yaml:"authPass,omitempty" json:"authPass,omitempty"`

	// AuthPassFrom resolves AuthPass at first boot, so the shared secret need
	// not sit in instance metadata.
	AuthPassFrom *SecretSource `yaml:"authPassFrom,omitempty" json:"authPassFrom,omitempty"`

	// UnicastPeers lists the other controllers' addresses. Set this when the
	// controllers cannot reach each other by multicast -- most clouds block
	// it, so on anything but a flat L2 network this is required.
	UnicastPeers []string `yaml:"unicastPeers,omitempty" json:"unicastPeers,omitempty"`
}

// Cluster carries cluster-wide identity and reachability settings.
type Cluster struct {
	// Name identifies the cluster. It is cosmetic but ends up in generated
	// kubeconfig contexts, so it is worth setting.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Endpoint is the address other nodes and kubectl use to reach the control
	// plane: a load balancer, a VIP, or the single controller's own address.
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`

	// SubjectAltNames are additional names and addresses included in the API
	// server certificate. Endpoint is included automatically.
	SubjectAltNames []string `yaml:"subjectAltNames,omitempty" json:"subjectAltNames,omitempty"`
}

// CNI identifies the container network interface plugin.
type CNI string

const (
	// CNIKubeRouter is the k0s default: a single, low-overhead component
	// providing networking, network policy and service proxying.
	CNIKubeRouter CNI = "kuberouter"

	// CNICalico offers a richer network policy implementation.
	CNICalico CNI = "calico"

	// CNICustom installs nothing. The operator is responsible for deploying a
	// CNI before the cluster becomes usable.
	CNICustom CNI = "custom"
)

// Network configures cluster addressing.
type Network struct {
	// PodCIDR is the address range allocated to pods.
	PodCIDR string `yaml:"podCIDR,omitempty" json:"podCIDR,omitempty"`

	// ServiceCIDR is the address range allocated to services.
	ServiceCIDR string `yaml:"serviceCIDR,omitempty" json:"serviceCIDR,omitempty"`

	// CNI selects the network plugin.
	CNI CNI `yaml:"cni,omitempty" json:"cni,omitempty"`
}

// StorageType identifies the control plane datastore backend.
type StorageType string

const (
	// StorageEtcd is the embedded etcd cluster. It is the only backend that
	// supports multiple controllers.
	StorageEtcd StorageType = "etcd"

	// StorageSQLite backs the control plane with SQLite through kine. It is
	// limited to a single controller.
	StorageSQLite StorageType = "sqlite"
)

// Storage selects the control plane datastore.
type Storage struct {
	// Type selects the backend.
	Type StorageType `yaml:"type,omitempty" json:"type,omitempty"`
}

// Join describes how a node authenticates to an existing cluster.
//
// Exactly one of Token or TokenFrom may be set. Inline tokens are convenient
// for labs and a liability in production: anything that can read the instance
// metadata can join the cluster. Prefer TokenFrom, and prefer short-lived
// tokens over long-lived ones.
type Join struct {
	// Token is a k0s join token supplied inline.
	Token string `yaml:"token,omitempty" json:"token,omitempty"`

	// TokenFrom resolves the join token at first boot.
	TokenFrom *SecretSource `yaml:"tokenFrom,omitempty" json:"tokenFrom,omitempty"`
}

// SecretSource resolves a secret at first boot, so that credentials need not be
// written into a configuration that is often readable by anything that can
// reach the instance metadata. Exactly one of URL or File may be set.
type SecretSource struct {
	// URL is fetched over HTTPS. Plain HTTP is rejected.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// File is read from the local filesystem, typically placed there by a
	// cloud-init write_files entry or by an out-of-band provisioning step.
	File string `yaml:"file,omitempty" json:"file,omitempty"`

	// AuthFile contains a bearer token presented when fetching URL. Its
	// contents are never logged.
	AuthFile string `yaml:"authFile,omitempty" json:"authFile,omitempty"`

	// WaitFor keeps retrying until the secret appears, for at most this long.
	// A Go duration such as "15m". Empty means do not wait.
	//
	// This is what lets a whole cluster start at once. A joining node can boot
	// before the node that mints its token has finished bootstrapping, wait,
	// and join when the token shows up -- instead of failing and needing an
	// operator to sequence the machines by hand.
	//
	// Waiting applies only while the secret is absent. A rejected credential
	// fails immediately, because retrying a wrong password for fifteen minutes
	// helps nobody and hides the mistake.
	WaitFor string `yaml:"waitFor,omitempty" json:"waitFor,omitempty"`
}

// Node carries kubelet-level attributes for this machine.
type Node struct {
	// Name is this node's hostname, and therefore the name it registers under
	// in Kubernetes.
	//
	// Leave it empty and Corium derives a stable name from the machine ID
	// unless something has already set a real hostname. It must be unique
	// within the cluster: two nodes sharing a name do not fail loudly, they
	// take turns overwriting each other's Node object.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Labels are applied to the Kubernetes node object.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`

	// Taints are applied to the Kubernetes node object.
	Taints []Taint `yaml:"taints,omitempty" json:"taints,omitempty"`
}

// Taint is a Kubernetes node taint.
type Taint struct {
	Key    string `yaml:"key" json:"key"`
	Value  string `yaml:"value,omitempty" json:"value,omitempty"`
	Effect string `yaml:"effect" json:"effect"`
}

// Addon is a Helm chart installed at bootstrap.
type Addon struct {
	// Name is the Helm release name.
	Name string `yaml:"name" json:"name"`

	// Chart is the qualified chart reference, such as "jetstack/cert-manager".
	Chart string `yaml:"chart" json:"chart"`

	// Version pins the chart version. Leaving it empty resolves to the latest
	// available version, which makes the node's outcome depend on when it
	// booted — pin it.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// Namespace is where the release is installed. Defaults to "default".
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

	// Repository declares the chart repository. It may be omitted when another
	// addon already declares the same repository.
	Repository *Repository `yaml:"repository,omitempty" json:"repository,omitempty"`

	// Values are the chart values, passed through unmodified.
	Values map[string]any `yaml:"values,omitempty" json:"values,omitempty"`
}

// Repository is a Helm chart repository.
type Repository struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
}

// UpgradePolicy says how far a node goes on its own when a newer image exists.
type UpgradePolicy string

const (
	// UpgradeNone leaves upgrades entirely to the operator. This is the
	// default: a Kubernetes node that reboots unprompted is an outage nobody
	// scheduled.
	UpgradeNone UpgradePolicy = "none"

	// UpgradeDownload stages a newer image without rebooting. The reboot stays
	// yours to schedule, but it becomes near-instant because the download and
	// the deployment already happened.
	UpgradeDownload UpgradePolicy = "download"

	// UpgradeApply downloads, drains the node, and reboots on its own.
	//
	// A drain that cannot finish -- a pod disruption budget refusing an
	// eviction -- cancels the upgrade rather than forcing it, and the node
	// tries again later.
	//
	// Draining needs cluster admin credentials, which only a node running a
	// control plane has locally. A plain worker reboots undrained.
	UpgradeApply UpgradePolicy = "apply"
)

// Filesystems a RAID array can be formatted with.
const (
	// RAIDFilesystemExt4 is the default, for the same reason the root
	// filesystem is ext4: it mounts on every kernel in service and grows
	// online. See docs/adr/0002-root-filesystem.md.
	RAIDFilesystemExt4 = "ext4"

	// RAIDFilesystemXFS is available for arrays that hold large files, where
	// its allocator behaves better. The kernel incompatibility that rules it
	// out for the root filesystem does not apply here: a data array is created
	// on the running node, not mounted by a build host.
	RAIDFilesystemXFS = "xfs"

	// RAIDFilesystemNone leaves the array unformatted, for a workload that
	// wants the raw block device.
	RAIDFilesystemNone = "none"
)

// RAIDArray declares one software RAID array built from whole disks.
//
// This covers spare disks, not the disk the OS booted from. Putting the root
// filesystem on an array is an install-time decision: by the time this code
// runs the root is already deployed and mounted, and nothing here can move it.
// A node that needs a redundant root is installed that way from the ISO, which
// the RAID documentation describes.
//
// What this earns over writing mdadm into cloud-init's runcmd, which is the
// honest alternative since cloud-init has no RAID support of its own:
//
//   - The array exists before k0s starts. A runcmd races the kubelet, and
//     losing that race means containerd writes to the mount point before the
//     array is mounted over it, where the data is invisible afterwards.
//   - It refuses to destroy data. A device that already carries a filesystem,
//     a partition table or another array's metadata stops the bootstrap
//     instead of being overwritten.
//   - It is idempotent. An array that already exists is adopted, not rebuilt.
type RAIDArray struct {
	// Name identifies the array. It becomes /dev/md/<name>, and must be unique
	// on the node.
	Name string `yaml:"name" json:"name"`

	// Level is the RAID level: 0, 1, 5, 6 or 10.
	//
	// Level 0 is striping with no redundancy. It is accepted because it is
	// occasionally what someone wants for scratch space, but losing any member
	// loses the array.
	Level int `yaml:"level" json:"level"`

	// Devices are the block devices to build the array from, by path. Whole
	// disks (/dev/sdb), not partitions.
	//
	// Prefer stable paths -- /dev/disk/by-id/... -- over kernel names. Kernel
	// names are assigned in discovery order and can move between boots, which
	// on a first boot means building an array out of whichever disks happened
	// to enumerate first.
	Devices []string `yaml:"devices" json:"devices"`

	// Spares are devices kept idle and pulled in automatically when a member
	// fails. Meaningless for level 0.
	Spares []string `yaml:"spares,omitempty" json:"spares,omitempty"`

	// Filesystem to create on the array: ext4 (default) or xfs. Set it to
	// "none" to leave the array unformatted, for a workload that wants the raw
	// block device.
	Filesystem string `yaml:"filesystem,omitempty" json:"filesystem,omitempty"`

	// MountPoint is where the array is mounted, and the entry written to
	// /etc/fstab so later boots mount it too. Empty means the array is
	// assembled but not mounted, which only makes sense with filesystem: none.
	MountPoint string `yaml:"mountPoint,omitempty" json:"mountPoint,omitempty"`

	// Wipe permits overwriting devices that already hold data.
	//
	// Off by default, and the default is the point: the failure mode of a disk
	// provisioning tool is destroying something irreplaceable, and a node that
	// refuses to boot is cheaper than one that silently erased a disk someone
	// meant to keep. Setting this true is an explicit statement that the
	// devices listed are expendable.
	Wipe bool `yaml:"wipe,omitempty" json:"wipe,omitempty"`
}

// ZFS vdev types, naming how the devices in one vdev are combined.
const (
	// ZFSVdevStripe concatenates its devices with no redundancy: losing any one
	// loses the vdev, and a vdev lost is the whole pool lost. It is the default
	// because it is what a single-disk vdev is, and a single data disk is the
	// common case; ask for it on purpose for anything larger.
	ZFSVdevStripe = "stripe"

	// ZFSVdevMirror keeps a full copy on every device in the vdev. Any one
	// survivor keeps the vdev serving.
	ZFSVdevMirror = "mirror"

	// ZFSVdevRAIDZ1 tolerates one failed device per vdev, like RAID 5.
	ZFSVdevRAIDZ1 = "raidz"

	// ZFSVdevRAIDZ2 tolerates two failed devices per vdev, like RAID 6.
	ZFSVdevRAIDZ2 = "raidz2"

	// ZFSVdevRAIDZ3 tolerates three failed devices per vdev.
	ZFSVdevRAIDZ3 = "raidz3"
)

// ZFSPool declares one ZFS storage pool built from whole disks.
//
// It sits alongside RAIDArray rather than replacing it: mdadm gives a block
// device an existing filesystem is laid on, while ZFS is the volume manager and
// the filesystem at once, with checksumming, compression and snapshots that a
// data disk holding a local persistent volume or an image cache actually wants.
// A node picks one or the other per set of disks; a device claimed by a pool
// cannot also be claimed by an array.
//
// Like RAID, this covers data disks and not the disk the OS booted from. By the
// time corium-agent reads this block the root filesystem is deployed and
// mounted, and nothing here can move it. Root on ZFS is a much larger question —
// it needs the module in the initramfs and bootc has no declarative equivalent
// of a ZFS root — and is deliberately out of scope. See
// docs/adr/0007-zfs-data-disks.md.
//
// What this earns over writing zpool into cloud-init's runcmd, which is the
// honest alternative since cloud-init has no ZFS support of its own, is exactly
// what RAID earns: the pool exists before k0s starts, it refuses to destroy
// data, and it is idempotent.
type ZFSPool struct {
	// Name identifies the pool. It becomes the top-level dataset name and, by
	// default, the mount point /<name>, so it must be unique on the node.
	Name string `yaml:"name" json:"name"`

	// Vdevs are the pool's virtual devices. A pool stripes across all of them,
	// so its redundancy is that of its least redundant vdev and it survives only
	// as long as every vdev does. At least one is required.
	Vdevs []ZFSVdev `yaml:"vdevs" json:"vdevs"`

	// MountPoint overrides where the pool's root dataset is mounted. Empty
	// leaves ZFS's default of /<name>. Set it to "none" or "legacy" to leave the
	// root dataset unmounted, for a pool whose datasets are mounted individually.
	MountPoint string `yaml:"mountPoint,omitempty" json:"mountPoint,omitempty"`

	// Options are pool-level properties, passed to `zpool create -o`. The one
	// most worth setting is ashift (ashift: "12" for 4K-sector disks), because
	// it is fixed for the life of the pool and cannot be changed afterwards.
	Options map[string]string `yaml:"options,omitempty" json:"options,omitempty"`

	// FilesystemOptions are properties set on the pool's root dataset and
	// inherited by every dataset under it, passed to `zpool create -O`. This is
	// where compression (compression: lz4) belongs.
	FilesystemOptions map[string]string `yaml:"filesystemOptions,omitempty" json:"filesystemOptions,omitempty"`

	// Datasets are child filesystems created within the pool, each able to carry
	// its own mount point and properties. A pool with no datasets listed is a
	// single filesystem mounted at the pool's mount point.
	Datasets []ZFSDataset `yaml:"datasets,omitempty" json:"datasets,omitempty"`

	// Wipe permits overwriting devices that already hold data. Off by default,
	// for the same reason as RAIDArray.Wipe: silently consuming a disk someone
	// meant to keep is the one failure this refuses to make convenient.
	Wipe bool `yaml:"wipe,omitempty" json:"wipe,omitempty"`
}

// ZFSVdev is one virtual device within a pool: a group of whole disks combined
// at a single redundancy level.
type ZFSVdev struct {
	// Type is how the devices are combined: stripe (default), mirror, raidz,
	// raidz2 or raidz3.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`

	// Devices are the block devices making up this vdev, by path. Whole disks,
	// not partitions.
	//
	// Prefer stable paths — /dev/disk/by-id/... — over kernel names, for the
	// same reason RAID does: kernel names are assigned in discovery order and
	// can name a different disk on the first boot than the one you meant.
	Devices []string `yaml:"devices" json:"devices"`
}

// ZFSDataset declares one filesystem within a pool.
type ZFSDataset struct {
	// Name is the dataset name relative to the pool: "data" becomes
	// <pool>/data. Slashes create a hierarchy, "k0s/containerd".
	Name string `yaml:"name" json:"name"`

	// MountPoint is where the dataset is mounted. Empty inherits the pool's
	// layout (<pool mount point>/<name>). Set it to "none" or "legacy" to leave
	// it unmounted.
	MountPoint string `yaml:"mountPoint,omitempty" json:"mountPoint,omitempty"`

	// Properties are ZFS properties set on this dataset, such as compression,
	// recordsize or quota.
	Properties map[string]string `yaml:"properties,omitempty" json:"properties,omitempty"`
}

// WireGuardAddresses is one or more interface addresses in CIDR form.
//
// It exists so the `address` key accepts either a single CIDR scalar or a list:
// the common single-address case stays a scalar, while a dual-stack interface
// carries both its IPv4 and IPv6 addresses, matching what wg-quick's own
// comma-separated Address line allows.
type WireGuardAddresses []string

// UnmarshalYAML accepts either a scalar CIDR or a sequence of them.
func (a *WireGuardAddresses) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		// An empty or null scalar leaves the slice nil, so validation reports it
		// as required rather than as a malformed empty address.
		if value.Value == "" {
			return nil
		}

		*a = WireGuardAddresses{value.Value}

		return nil
	}

	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}

	*a = list

	return nil
}

// WireGuardInterface declares one host WireGuard interface, describing this
// node's participation in an encrypted overlay between hosts.
//
// It is provisioned by corium-agent at first boot, before k0s, on the same
// footing as a RAID array: the interface is up before the kubelet registers, and
// the k0s service is ordered to wait for it. What this earns over writing the
// same interface into cloud-init's write_files and runcmd -- the honest
// alternative, since the interface is otherwise a plain wg-quick config -- is the
// three things a recipe cannot reach, and they are why this is a field:
//
//   - It is read from the whole configuration source chain, not just cloud-init,
//     so it configures a node with no cloud-init datasource -- bare metal, PXE,
//     an appliance -- where write_files and runcmd never run at all.
//   - The overlay address becomes the address k0s registers (see NodeAddress),
//     rather than the physical NIC the kubelet would otherwise pick, which leaves
//     the node unreachable from across the overlay.
//   - The private key is resolved as a secret (see PrivateKeyFrom) instead of
//     sitting in cleartext in instance metadata.
//
// See docs/adr/0006-host-wireguard-overlay.md.
type WireGuardInterface struct {
	// Name is the interface name, such as "wg0". It becomes the wg-quick unit
	// instance, so it must be a valid Linux interface name and unique on the node.
	Name string `yaml:"name" json:"name"`

	// Address is this node's address on the overlay, in CIDR form:
	// "10.10.0.2/24". It is a host address with a prefix length, not a bare IP.
	//
	// It accepts a single CIDR or a list, so a dual-stack interface can carry
	// both its IPv4 and IPv6 addresses. When NodeAddress is set, the first
	// address listed is the one k0s registers with.
	Address WireGuardAddresses `yaml:"address" json:"address"`

	// ListenPort is the UDP port WireGuard listens on. Leave it unset on a
	// client-only node; it is required on any node a peer names in its endpoint,
	// because a node with no listen port cannot be dialled.
	ListenPort int `yaml:"listenPort,omitempty" json:"listenPort,omitempty"`

	// MTU overrides the interface MTU. Left unset, wg-quick derives one, which is
	// right on most links; set it only when the path needs a smaller value.
	MTU int `yaml:"mtu,omitempty" json:"mtu,omitempty"`

	// NodeAddress marks this interface's address as the one k0s registers with,
	// so the node is reachable over the overlay rather than at its physical NIC.
	// At most one interface across the block may set it.
	NodeAddress bool `yaml:"nodeAddress,omitempty" json:"nodeAddress,omitempty"`

	// PrivateKey is the interface private key, base64-encoded. Inline keys are
	// convenient for labs and a liability in production, exactly like an inline
	// join token: anything that can read the instance metadata can read the key.
	// Prefer PrivateKeyFrom.
	PrivateKey string `yaml:"privateKey,omitempty" json:"privateKey,omitempty"`

	// PrivateKeyFrom resolves the private key at first boot, so it need not sit
	// in instance metadata. Exactly one of PrivateKey or PrivateKeyFrom is set.
	PrivateKeyFrom *SecretSource `yaml:"privateKeyFrom,omitempty" json:"privateKeyFrom,omitempty"`

	// Peers are the other ends of the overlay this node talks to.
	Peers []WireGuardPeer `yaml:"peers,omitempty" json:"peers,omitempty"`
}

// WireGuardPeer is one remote end of a WireGuard interface.
type WireGuardPeer struct {
	// PublicKey is the peer's public key, base64-encoded. A public key is not a
	// secret, so it sits inline.
	PublicKey string `yaml:"publicKey" json:"publicKey"`

	// Endpoint is the peer's address, "host:port". Prefer an IP: a name has to
	// resolve before the network the overlay itself may gate is up.
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`

	// AllowedIPs are the ranges routed to this peer, each in CIDR form. WireGuard
	// routes by longest match, so two peers on one interface may not claim
	// overlapping ranges.
	AllowedIPs []string `yaml:"allowedIPs" json:"allowedIPs"`

	// PersistentKeepalive keeps a path open through NAT, in seconds. Set it on a
	// node behind NAT that must stay reachable; 25 is the usual value.
	PersistentKeepalive int `yaml:"persistentKeepalive,omitempty" json:"persistentKeepalive,omitempty"`

	// PresharedKey adds a symmetric layer on top of the public-key handshake,
	// base64-encoded. It is a secret, so prefer PresharedKeyFrom in production.
	PresharedKey string `yaml:"presharedKey,omitempty" json:"presharedKey,omitempty"`

	// PresharedKeyFrom resolves the preshared key at first boot. At most one of
	// PresharedKey or PresharedKeyFrom is set.
	PresharedKeyFrom *SecretSource `yaml:"presharedKeyFrom,omitempty" json:"presharedKeyFrom,omitempty"`
}

// Upgrades controls whether a node updates itself.
type Upgrades struct {
	// Automatic selects how far the node goes unattended. Defaults to none.
	Automatic UpgradePolicy `yaml:"automatic,omitempty" json:"automatic,omitempty"`

	// Schedule is a systemd OnCalendar expression saying when to check.
	// Defaults to daily. See systemd.time(7) for the syntax.
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
}

// APIMode is what corium-apid does on a node, derived from the API block
// rather than written by the operator.
type APIMode string

const (
	// APIModeDisabled runs no daemon and binds no port. It is what a node with
	// no api: block does, which is every node provisioned before the API
	// existed.
	APIModeDisabled APIMode = "disabled"

	// APIModeConfigured takes the operator CA from the configuration, inline
	// or resolved at first boot. Bootstrap is unaffected: the node joins its
	// cluster without waiting for anyone.
	APIModeConfigured APIMode = "configured"

	// APIModeMaintenance is a node nobody has claimed yet. It validates its
	// configuration, holds the bootstrap, and serves one RPC until an operator
	// enrols it from the console.
	APIModeMaintenance APIMode = "maintenance"
)

// API configures corium-apid, the node's management API.
//
// It is off by default, and that is a decision rather than caution: a
// privileged daemon listening on every machine in a fleet is a reasonable
// thing to refuse, and a node that has been running for a year should not
// acquire a listening port by being upgraded.
//
// Trust is anchored in an operator CA, of which the node is given the
// certificate and never the key. A certificate is public material, so unlike a
// join token it can sit in instance metadata in clear without leaking
// anything. See docs/adr/0004-management-api.md.
type API struct {
	// Enabled turns the daemon on. Setting OperatorCA or OperatorCAFrom
	// implies it, so it only needs writing to ask for maintenance mode, or to
	// state a refusal that nothing later overrides.
	//
	// It is a pointer because an absent Enabled and an explicit false are
	// different statements. Absent alongside an operator CA is the common
	// case; false alongside one is a configuration that contradicts itself,
	// and is rejected rather than resolved by a precedence rule.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// OperatorCA is the PEM-encoded certificate of the CA that signs the
	// client certificates this node will accept. The matching private key
	// stays with the operator and never reaches a node.
	OperatorCA string `yaml:"operatorCA,omitempty" json:"operatorCA,omitempty"`

	// OperatorCAFrom resolves OperatorCA at first boot, for a CA minted by the
	// same run that builds the cluster. Exactly one of the two may be set.
	OperatorCAFrom *SecretSource `yaml:"operatorCAFrom,omitempty" json:"operatorCAFrom,omitempty"`

	// AwaitConfig holds the bootstrap until an operator sends a configuration,
	// as well as until they claim the node.
	//
	// It is the explicit spelling of a wait a document usually asks for by
	// saying less: one that names no role already holds, because there is
	// nothing in it to build. This is for the document that names a role and
	// should wait anyway -- a fleet-wide cloud-config that says `role: worker`
	// and expects everything machine-specific to arrive over the API.
	//
	// Without it, a node whose document names a role builds that role the
	// moment it is claimed. That is mode C of ADR 4 working as designed rather
	// than a trap, but it is irreversible without `cctl reset`, which is why
	// cctl says so plainly before it claims anything.
	AwaitConfig bool `yaml:"awaitConfig,omitempty" json:"awaitConfig,omitempty"`

	// Insecure drops the pairing code from maintenance mode: the first client
	// to reach the node claims it, with nothing to prove.
	//
	// This is Talos's model, and Corium's default is deliberately not it --
	// see docs/adr/0004-management-api.md. Whoever wins the race owns the node
	// for the rest of its life, and the node then joins a cluster with
	// credentials its configuration supplied, under their CA.
	//
	// What makes it defensible where it is used is the rule the rest of the
	// design already enforces: an unclaimed node is in no cluster, so the
	// prize is a bare machine. On a provisioning network you control end to
	// end, or a bench, or a PXE fleet where visiting consoles is not a real
	// option, that is a fair trade. On anything shared it is not.
	//
	// A plain bool, unlike Enabled: absent and false say the same thing.
	// The node records that it was claimed this way, and says so afterwards.
	Insecure bool `yaml:"insecure,omitempty" json:"insecure,omitempty"`
}

// Mode reports what this configuration asks corium-apid to do.
func (a API) Mode() APIMode {
	trusted := a.OperatorCA != "" || a.OperatorCAFrom != nil

	switch {
	case a.Enabled != nil && !*a.Enabled:
		// Validation rejects an explicit false alongside an operator CA, so
		// reaching here with one set is impossible. Refusing to serve is still
		// the right answer if it somehow happens.
		return APIModeDisabled
	case trusted:
		return APIModeConfigured
	case a.Enabled != nil && *a.Enabled:
		return APIModeMaintenance
	default:
		return APIModeDisabled
	}
}

// OpenEnrolment reports whether the node will let anyone who reaches it claim
// it, with no pairing code.
//
// It is only ever true in maintenance mode: a node whose configuration names
// its owner was never unclaimed, and a node with no API serves nothing to open.
func (a API) OpenEnrolment() bool {
	return a.Insecure && a.Mode() == APIModeMaintenance
}

// HoldsBootstrap reports whether the node must wait to be claimed before it
// joins a cluster.
//
// A node waiting for an operator is not a cluster member, and the ordering is
// the point rather than an implementation detail: bootstrapping first would
// leave a machine that runs workloads and holds cluster credentials while
// still obeying whoever first reaches an unauthenticated port.
func (a API) HoldsBootstrap() bool {
	return a.Mode() == APIModeMaintenance
}

// K0s is the escape hatch to the underlying k0s configuration.
type K0s struct {
	// Patch is a strategic merge patch applied to the rendered k0s.yaml after
	// Corium has finished with it. It is passed through without interpretation,
	// so every k0s setting remains reachable — including ones Corium has never
	// heard of.
	//
	// Corium validates that the result is syntactically valid YAML and nothing
	// more. A patch that breaks the cluster is the operator's to own.
	Patch map[string]any `yaml:"patch,omitempty" json:"patch,omitempty"`
}

// Required reports whether a join token has been configured by any means.
func (j Join) Required() bool {
	return j.Token != "" || j.TokenFrom != nil
}

// IsController reports whether the role runs a control plane.
func (r Role) IsController() bool {
	return r == RoleSingle || r == RoleController || r == RoleControllerWorker
}

// IsWorker reports whether the role runs workloads.
func (r Role) IsWorker() bool {
	return r == RoleSingle || r == RoleControllerWorker || r == RoleWorker
}

// HoldsForConfiguration reports whether the bootstrap must wait for an operator
// to say what this node is before it builds anything.
//
// Two documents ask for that wait, and they ask for it in different tones. One
// says so outright with `api.awaitConfig`. The other simply names no role,
// which is the same statement made by omission -- and it is not a guess: a
// document with no role describes no node, so there is nothing the bootstrap
// could build from it even if it tried. Validation guarantees an empty role
// only reaches here alongside a running API, so the answer this waits for can
// always arrive.
func (c *Config) HoldsForConfiguration() bool {
	return c.API.AwaitConfig || c.Role == ""
}

// WireGuardNodeAddress returns the bare overlay address this node should register
// with k0s, or "" if no interface is marked as the node address.
//
// It is the address of the interface whose NodeAddress is set, with its prefix
// length stripped: the kubelet wants an address, not a CIDR. Validation
// guarantees at most one interface sets it.
func (c *Config) WireGuardNodeAddress() string {
	for i := range c.WireGuard {
		if c.WireGuard[i].NodeAddress && len(c.WireGuard[i].Address) > 0 {
			address, _, _ := strings.Cut(c.WireGuard[i].Address[0], "/")

			return address
		}
	}

	return ""
}
