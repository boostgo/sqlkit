// Command cluster shows a PostgreSQL primary with read replicas: reads marked with PreferReplica go to
// a replica whose replication lag is within the limit, everything else goes to the primary.
//
//	SQLKIT_PRIMARY_HOST=db-primary SQLKIT_REPLICA_HOSTS=db-ro1,db-ro2 go run ./examples/cluster
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/boostgo/sqlkit"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	primary := os.Getenv("SQLKIT_PRIMARY_HOST")
	if primary == "" {
		return errors.New("set SQLKIT_PRIMARY_HOST and SQLKIT_REPLICA_HOSTS (comma separated)")
	}

	cfg := sqlkit.ClusterConfig{
		Primary: sqlkit.Config{
			Host: primary, Port: 5432, Username: "postgres", Password: "postgres", Database: "postgres",
			MaxOpenConnections: 20,
		},
		ReplicasEnabled:         true,
		MaxLagSeconds:           30,
		LagCheckIntervalSeconds: 5,
	}

	for i, host := range strings.Split(os.Getenv("SQLKIT_REPLICA_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			// zero fields (port, credentials, pool) are inherited from the primary
			cfg.Replicas = append(cfg.Replicas, sqlkit.ReplicaConfig{
				Name: fmt.Sprintf("ro%d", i+1), Config: sqlkit.Config{Host: host},
			})
		}
	}

	cluster, err := sqlkit.ConnectCluster(cfg,
		sqlkit.WithReplicaBalancer(sqlkit.LeastLag()),
		sqlkit.WithStateHook(func(s sqlkit.ReplicaState) { log.Printf("replica state: %s", s) }),
	)
	if err != nil {
		return err // only an unavailable primary fails the start
	}
	defer cluster.Close()

	ctx := context.Background()

	var now string

	// a replica if one is usable, otherwise the primary
	if err = cluster.GetContext(sqlkit.PreferReplica(ctx), &now, "SELECT now()::text"); err != nil {
		return err
	}

	fmt.Println("read (prefer replica):", now)

	// a replica or an error, never the primary
	err = cluster.GetContext(sqlkit.RequireReplica(ctx), &now, "SELECT now()::text")
	if errors.Is(err, sqlkit.ErrReplicaUnavailable) {
		fmt.Println("no usable replica yet:", cluster.States())
	}

	fmt.Println("summary:", cluster.State())

	return nil
}
