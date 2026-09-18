// Package config defines the Corium configuration surface: the `corium:` block
// embedded in a cloud-config document, its defaults, and its validation rules.
//
// The schema is deliberately small. It covers the configurations that most
// operators need most of the time; anything beyond that is reached through the
// escape hatches (K0s.Patch, and raw cloud-init modules) rather than by growing
// another key here. A configuration key added to this file is permanent, ships
// to every node, and must be supported forever — add them reluctantly.
package config

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
