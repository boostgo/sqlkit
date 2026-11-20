package sqlkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// ClusterConfig is the config of ConnectCluster: a primary and optional replicas of PostgreSQL.
type ClusterConfig struct {
	Primary Config `json:"primary" yaml:"primary"`
	// ReplicasEnabled is the switch of the replicas: false means they are not even opened.
	ReplicasEnabled bool            `json:"replicas_enabled" yaml:"replicasEnabled"`
	Replicas        []ReplicaConfig `json:"replicas" yaml:"replicas"`
	// MaxLagSeconds is WithMaxLag in seconds; 0 means the default (30s).
	MaxLagSeconds int `json:"max_lag_seconds" yaml:"maxLagSeconds"`
	// LagCheckIntervalSeconds is WithLagCheckInterval in seconds; 0 means the default (5s).
	LagCheckIntervalSeconds int `json:"lag_check_interval_seconds" yaml:"lagCheckIntervalSeconds"`
}

// ReplicaConfig is the config of one replica. Its zero fields are taken from ClusterConfig.Primary
// (see ConnectCluster), Host and Name are required.
type ReplicaConfig struct {
	Name   string           `json:"name" yaml:"name"`
	Config `yaml:",inline"` // json: embedded without a tag, the fields are flat
}

// clusterDialer is what ConnectCluster uses to open pools (replaced in tests).
type clusterDialer struct {
	primary func(Config) (*sqlx.DB, error) // with ping
	replica func(Config) (*sqlx.DB, error) // without ping
}

var defaultClusterDialer = clusterDialer{
	primary: func(cfg Config) (*sqlx.DB, error) { return ConnectFromConfig(cfg) },
	replica: openFromConfig,
}

// ConnectCluster connects to the primary (with ping) and, if cfg.ReplicasEnabled, opens the replicas
// without ping: an unavailable replica does not stop the start, it stays in "check_failed" and starts
// serving after a successful lag check. A zero field of a replica config is taken from cfg.Primary:
// Port, Username, Password, Database, Driver, SSLMode, TimeoutMs, BinaryParameters, MaxOpenConnections,
// MaxIdleConnections, MaxLifetime, MaxIdleTime, ReadTimeout, WriteTimeout. Migration* and ClickhouseScheme
// are not inherited.
//
// Options from cfg are applied first, then opts (they win). The returned Cluster owns the connections:
// Close stops the lag check and closes the replicas and the primary.
func ConnectCluster(cfg ClusterConfig, opts ...ClusterOption) (*Cluster, error) {
	return connectCluster(cfg, defaultClusterDialer, opts...)
}

func connectCluster(cfg ClusterConfig, dial clusterDialer, opts ...ClusterOption) (*Cluster, error) {
	if err := validateClusterConfig(cfg); err != nil {
		return nil, err
	}

	primary, err := dial.primary(cfg.Primary)
	if err != nil {
		return nil, err
	}

	owned := []*sqlx.DB{}
	cfgOpts := []ClusterOption{}

	closeOpened := func() error {
		var closeErr error
		for _, conn := range owned {
			closeErr = errors.Join(closeErr, conn.Close())
		}

		return errors.Join(closeErr, primary.Close())
	}

	if cfg.ReplicasEnabled {
		for _, rc := range cfg.Replicas {
			conn, openErr := dial.replica(inheritReplicaConfig(cfg.Primary, rc.Config))
			if openErr != nil {
				return nil, errors.Join(fmt.Errorf("replica %q: %w", rc.Name, openErr), closeOpened())
			}

			owned = append(owned, conn)
			cfgOpts = append(cfgOpts, WithNamedReplica(rc.Name, conn))
		}
	}

	if cfg.MaxLagSeconds > 0 {
		cfgOpts = append(cfgOpts, WithMaxLag(time.Duration(cfg.MaxLagSeconds)*time.Second))
	}

	if cfg.LagCheckIntervalSeconds > 0 {
		cfgOpts = append(cfgOpts, WithLagCheckInterval(time.Duration(cfg.LagCheckIntervalSeconds)*time.Second))
	}

	c, err := newCluster(primary, append(cfgOpts, opts...)...)
	if err != nil {
		return nil, errors.Join(err, closeOpened())
	}

	c.owned = append(c.owned, owned...)
	c.owned = append(c.owned, primary)
	c.start()

	return c, nil
}

func validDriver(name string) bool {
	return name == "" || name == PqDriver || name == PgxDriver
}

func validateClusterConfig(cfg ClusterConfig) error {
	if !validDriver(cfg.Primary.Driver) {
		return fmt.Errorf("%w: unsupported primary driver %q", ErrClusterOption, cfg.Primary.Driver)
	}

	if cfg.MaxLagSeconds < 0 {
		return fmt.Errorf("%w: max lag seconds must be >= 0, got %d", ErrClusterOption, cfg.MaxLagSeconds)
	}

	if cfg.LagCheckIntervalSeconds < 0 {
		return fmt.Errorf("%w: lag check interval seconds must be >= 0, got %d",
			ErrClusterOption, cfg.LagCheckIntervalSeconds)
	}

	if !cfg.ReplicasEnabled {
		return nil
	}

	names := make(map[string]struct{}, len(cfg.Replicas))

	for i, r := range cfg.Replicas {
		switch {
		case r.Name == "":
			return fmt.Errorf("%w: replica #%d has no name", ErrClusterOption, i)
		case r.Host == "":
			return fmt.Errorf("%w: replica %q has no host", ErrClusterOption, r.Name)
		case !validDriver(r.Driver):
			return fmt.Errorf("%w: unsupported driver %q of replica %q", ErrClusterOption, r.Driver, r.Name)
		}

		if _, dup := names[r.Name]; dup {
			return fmt.Errorf("%w: duplicate replica name %q", ErrClusterOption, r.Name)
		}

		names[r.Name] = struct{}{}
	}

	return nil
}

// inheritReplicaConfig is the config of a replica: its own non-zero fields over the primary's.
func inheritReplicaConfig(primary, replica Config) Config {
	out := Config{Host: replica.Host}

	out.Port = firstNonZero(replica.Port, primary.Port)
	out.Username = firstNonZero(replica.Username, primary.Username)
	out.Password = firstNonZero(replica.Password, primary.Password)
	out.Database = firstNonZero(replica.Database, primary.Database)
	out.Driver = firstNonZero(replica.Driver, primary.Driver)
	out.SSLMode = firstNonZero(replica.SSLMode, primary.SSLMode)
	out.TimeoutMs = firstNonZero(replica.TimeoutMs, primary.TimeoutMs)
	out.BinaryParameters = replica.BinaryParameters || primary.BinaryParameters
	out.MaxOpenConnections = firstNonZero(replica.MaxOpenConnections, primary.MaxOpenConnections)
	out.MaxIdleConnections = firstNonZero(replica.MaxIdleConnections, primary.MaxIdleConnections)
	out.MaxLifetime = firstNonZero(replica.MaxLifetime, primary.MaxLifetime)
	out.MaxIdleTime = firstNonZero(replica.MaxIdleTime, primary.MaxIdleTime)
	out.ReadTimeout = firstNonZero(replica.ReadTimeout, primary.ReadTimeout)
	out.WriteTimeout = firstNonZero(replica.WriteTimeout, primary.WriteTimeout)

	return out
}

func firstNonZero[T comparable](v, fallback T) T {
	var zero T
	if v != zero {
		return v
	}

	return fallback
}
