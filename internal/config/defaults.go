package config

// Default values applied to any field the operator left unset.
//
// These match k0s's own defaults where k0s has one, so that a Corium node and a
// hand-configured k0s node agree unless Corium has a specific reason to differ.
const (
	DefaultClusterName = "corium"
	DefaultPodCIDR     = "10.244.0.0/16"
	DefaultServiceCIDR = "10.96.0.0/12"
	DefaultCNI         = CNIKubeRouter
	DefaultNamespace   = "default"
)

// ApplyDefaults fills in unset fields. It is idempotent.
func (c *Config) ApplyDefaults() {
	if c.Cluster.Name == "" {
		c.Cluster.Name = DefaultClusterName
	}

	if c.Network.PodCIDR == "" {
		c.Network.PodCIDR = DefaultPodCIDR
	}

	if c.Network.ServiceCIDR == "" {
		c.Network.ServiceCIDR = DefaultServiceCIDR
	}

	if c.Network.CNI == "" {
		c.Network.CNI = DefaultCNI
	}

	if c.Storage.Type == "" {
		c.Storage.Type = c.Role.defaultStorage()
	}

	for i := range c.Addons {
		if c.Addons[i].Namespace == "" {
			c.Addons[i].Namespace = DefaultNamespace
		}
	}
}

// defaultStorage picks the datastore that suits the role.
//
// A single-node cluster gets SQLite: it cannot gain controllers later, so the
// operational cost of etcd buys nothing. Every other controller role gets etcd,
// because choosing SQLite would silently cap the cluster at one controller
// forever.
func (r Role) defaultStorage() StorageType {
	if r == RoleSingle {
		return StorageSQLite
	}

	return StorageEtcd
}
