package sqlkit

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// Reasons of ReplicaState.
const (
	ReasonOK            = "ok"
	ReasonNoReplica     = "no_replica"
	ReasonNotChecked    = "not_checked"
	ReasonLagExceeded   = "lag_exceeded"
	ReasonCheckFailed   = "check_failed"
	ReasonReplayUnknown = "replay_unknown"
)

const (
	lagQueryPrimary = `SELECT pg_current_wal_lsn()::text`
	lagQueryReplica = `SELECT pg_is_in_recovery(), pg_last_wal_replay_lsn()::text, ` +
		`pg_wal_lsn_diff(pg_last_wal_replay_lsn(), $1) >= 0, ` +
		`extract(epoch FROM now() - pg_last_xact_replay_timestamp())`
)

// ReplicaState is the current applicability of a replica.
type ReplicaState struct {
	// Name is the name of the replica; empty only in the summary State() and in the "no_replica" state.
	Name   string
	Usable bool
	Lag    time.Duration
	// Reason is "ok" | "no_replica" | "not_checked" | "lag_exceeded" | "check_failed" | "replay_unknown".
	Reason string
}

func (c *Cluster) checkTimeout() time.Duration {
	return min(c.cfg.interval, maxClusterLagCheckTimeout)
}

// runLagLoop checks the lag right away and then every interval until Close.
func (c *Cluster) runLagLoop(ctx context.Context) {
	defer close(c.done)

	c.checkOnce(ctx)

	t := time.NewTicker(c.cfg.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.checkOnce(ctx)
		}
	}
}

type lagResult struct {
	lag    time.Duration
	reason string
}

// checkOnce does one lag check: the LSN of primary once, then all replicas in parallel, each with its own
// timeout. It stores the new states and calls the hook for every replica whose Usable or Reason changed.
func (c *Cluster) checkOnce(ctx context.Context) {
	results := make([]lagResult, len(c.replicas))

	primaryLSN, err := c.primaryLSN(ctx)
	if err != nil {
		for i := range results {
			results[i] = lagResult{reason: ReasonCheckFailed}
		}
	} else {
		var wg sync.WaitGroup

		for i, r := range c.replicas {
			wg.Add(1)

			go func() {
				defer wg.Done()

				rctx, cancel := context.WithTimeout(ctx, c.checkTimeout())
				defer cancel()

				lag, reason := measureLag(rctx, r.conn, primaryLSN)
				results[i] = lagResult{lag: lag, reason: reason}
			}()
		}

		wg.Wait()
	}

	// stores and hooks are sequential: the hook is never called concurrently
	for i, r := range c.replicas {
		prev := *r.state.Load()
		next := nextReplicaState(prev, results[i].lag, results[i].reason, c.cfg.maxLag)
		r.state.Store(&next)

		if c.cfg.hook != nil && (next.Usable != prev.Usable || next.Reason != prev.Reason) {
			c.cfg.hook(next)
		}
	}
}

func (c *Cluster) primaryLSN(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.checkTimeout())
	defer cancel()

	var lsn string
	if err := c.primaryConn.QueryRowContext(ctx, lagQueryPrimary).Scan(&lsn); err != nil {
		return "", err //nolint:wrapcheck // only the fact of failure matters: all replicas get check_failed
	}

	return lsn, nil
}

// measureLag returns the lag of the replica or, if it is unknown, ReasonCheckFailed / ReasonReplayUnknown.
// On success the reason is ReasonOK.
func measureLag(ctx context.Context, replica *sqlx.DB, primaryLSN string) (time.Duration, string) {
	var (
		inRecovery bool
		replayLSN  sql.NullString
		caughtUp   sql.NullBool
		seconds    sql.NullFloat64
	)

	err := replica.QueryRowContext(ctx, lagQueryReplica, primaryLSN).
		Scan(&inRecovery, &replayLSN, &caughtUp, &seconds)
	if err != nil {
		return 0, ReasonCheckFailed
	}

	switch {
	case !inRecovery:
		return 0, ReasonOK
	case caughtUp.Valid && caughtUp.Bool:
		return 0, ReasonOK
	case !seconds.Valid:
		return 0, ReasonReplayUnknown
	}

	return time.Duration(max(seconds.Float64, 0) * float64(time.Second)), ReasonOK
}

// nextReplicaState applies the hysteresis: usable -> unusable when lag > maxLag,
// unusable -> usable only when lag <= maxLag/2. The name is kept from prev.
func nextReplicaState(prev ReplicaState, lag time.Duration, reason string, maxLag time.Duration) ReplicaState {
	if reason != ReasonOK {
		return ReplicaState{Name: prev.Name, Reason: reason}
	}

	usable := lag <= maxLag/2
	if prev.Usable {
		usable = lag <= maxLag
	}

	if !usable {
		return ReplicaState{Name: prev.Name, Lag: lag, Reason: ReasonLagExceeded}
	}

	return ReplicaState{Name: prev.Name, Usable: true, Lag: lag, Reason: ReasonOK}
}

func (s ReplicaState) String() string {
	str := fmt.Sprintf("usable=%t lag=%s reason=%s", s.Usable, s.Lag, s.Reason)
	if s.Name != "" {
		str = "name=" + s.Name + " " + str
	}

	return str
}
