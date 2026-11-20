package sqlkit

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

const (
	defaultClusterMaxLag        = 30 * time.Second
	defaultClusterLagInterval   = 5 * time.Second
	maxClusterLagCheckTimeout   = 2 * time.Second
	defaultClusterConflictRetry = true

	// defaultReplicaName is the name of the replica set by WithReplica.
	defaultReplicaName = "default"
)

type replicaSpec struct {
	name string
	conn *sqlx.DB
}

type clusterConfig struct {
	replicas      []replicaSpec
	balancer      ReplicaBalancer
	maxLag        time.Duration
	interval      time.Duration
	conflictRetry bool
	hook          func(ReplicaState)
}

// ClusterOption configures NewCluster.
type ClusterOption func(*clusterConfig) error

func (c *clusterConfig) indexOf(name string) int {
	for i, r := range c.replicas {
		if r.name == name {
			return i
		}
	}

	return -1
}

// WithReplica sets the replica named "default". A repeated call replaces it (the last one wins);
// nil removes it. nil means a cluster without this replica.
func WithReplica(replica *sqlx.DB) ClusterOption {
	return func(c *clusterConfig) error {
		i := c.indexOf(defaultReplicaName)

		switch {
		case replica == nil && i >= 0:
			c.replicas = append(c.replicas[:i], c.replicas[i+1:]...)
		case replica == nil:
		case i >= 0:
			c.replicas[i].conn = replica
		default:
			c.replicas = append(c.replicas, replicaSpec{name: defaultReplicaName, conn: replica})
		}

		return nil
	}
}

// WithNamedReplica adds a replica. It can be repeated; the order of the options is the order of States().
// db == nil is skipped. An empty or already used name gives ErrClusterOption.
func WithNamedReplica(name string, db *sqlx.DB) ClusterOption {
	return func(c *clusterConfig) error {
		if db == nil {
			return nil
		}

		if name == "" {
			return fmt.Errorf("%w: replica name must not be empty", ErrClusterOption)
		}

		if c.indexOf(name) >= 0 {
			return fmt.Errorf("%w: duplicate replica name %q", ErrClusterOption, name)
		}

		c.replicas = append(c.replicas, replicaSpec{name: name, conn: db})

		return nil
	}
}

// WithReplicaBalancer sets how a replica is chosen among the usable ones (default RoundRobin). b must not be nil.
func WithReplicaBalancer(b ReplicaBalancer) ClusterOption {
	return func(c *clusterConfig) error {
		if b == nil {
			return fmt.Errorf("%w: replica balancer must not be nil", ErrClusterOption)
		}

		c.balancer = b

		return nil
	}
}

// WithMaxLag sets the replication lag above which a replica is not used (default 30s). d must be > 0.
// A replica becomes usable again when its lag is back to maxLag/2 or less.
func WithMaxLag(d time.Duration) ClusterOption {
	return func(c *clusterConfig) error {
		if d <= 0 {
			return fmt.Errorf("%w: max lag must be > 0, got %s", ErrClusterOption, d)
		}

		c.maxLag = d

		return nil
	}
}

// WithLagCheckInterval sets the lag check period (default 5s). d must be > 0.
// The timeout of one check is min(d, 2s).
func WithLagCheckInterval(d time.Duration) ClusterOption {
	return func(c *clusterConfig) error {
		if d <= 0 {
			return fmt.Errorf("%w: lag check interval must be > 0, got %s", ErrClusterOption, d)
		}

		c.interval = d

		return nil
	}
}

// WithRecoveryConflictRetry enables one retry of a read that failed on a replica with SQLSTATE 40001
// "conflict with recovery" (default true): on primary after PreferReplica, on another usable replica
// after RequireReplica.
func WithRecoveryConflictRetry(on bool) ClusterOption {
	return func(c *clusterConfig) error {
		c.conflictRetry = on

		return nil
	}
}

// WithStateHook sets fn called with the state of one replica (State.Name is set) when its Usable or Reason
// changes (not on every check). fn is called from the lag check goroutine, one call at a time: keep it fast
// and do not call Cluster.Close from it.
func WithStateHook(fn func(ReplicaState)) ClusterOption {
	return func(c *clusterConfig) error {
		c.hook = fn

		return nil
	}
}
