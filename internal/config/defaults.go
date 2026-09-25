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

	// DefaultUpgradeSchedule checks once a day. systemd applies a randomised
	// delay on top, so a fleet does not converge on the registry at once.
	DefaultUpgradeSchedule = "daily"

	// DefaultBackupSchedule snapshots the control plane once a day. The timer
	// carries a randomised delay, so the controllers of one cluster do not all
	// stop to snapshot etcd at the same minute.
	DefaultBackupSchedule = "daily"

	// DefaultBackupPath is on the node's own disk, under Corium's state
	// directory. It is somewhere rather than nowhere; a backup that matters
	// belongs on a mount that outlives the machine.
	DefaultBackupPath = "/var/lib/corium/backups"

	// DefaultBackupKeep is a week of daily archives. Long enough to notice
	// something went wrong on Friday, short enough not to fill /var.
	DefaultBackupKeep = 7
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

	if c.Upgrades.Automatic == "" {
		c.Upgrades.Automatic = UpgradeNone
	}

	if c.Upgrades.Schedule == "" {
		c.Upgrades.Schedule = DefaultUpgradeSchedule
	}

	// Only for a block that asked for something. Filling these in
	// unconditionally would make every configuration carry a backup block,
	// which is how a setting nobody wrote ends up rejected on a worker.
	if c.Backup.Enabled {
		if c.Backup.Schedule == "" {
			c.Backup.Schedule = DefaultBackupSchedule
		}

		if c.Backup.Path == "" {
			c.Backup.Path = DefaultBackupPath
		}

		if c.Backup.Keep == 0 {
			c.Backup.Keep = DefaultBackupKeep
		}
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
