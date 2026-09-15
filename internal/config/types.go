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

	// Upgrades controls whether the node updates itself.
	Upgrades Upgrades `yaml:"upgrades,omitempty" json:"upgrades,omitempty"`

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

	// UpgradeApply downloads and reboots on its own.
	//
	// It does not drain the node first, because nothing on the node knows how
	// to. Reasonable for a single-node cluster or a lab; on anything carrying
	// workloads you care about, prefer download and drive the reboot yourself.
	UpgradeApply UpgradePolicy = "apply"
)

// Upgrades controls whether a node updates itself.
type Upgrades struct {
	// Automatic selects how far the node goes unattended. Defaults to none.
	Automatic UpgradePolicy `yaml:"automatic,omitempty" json:"automatic,omitempty"`

	// Schedule is a systemd OnCalendar expression saying when to check.
	// Defaults to daily. See systemd.time(7) for the syntax.
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
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
