package sqlkit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
)

var _ DB = (*Cluster)(nil)

// sqlStateRecoveryConflict is "canceling statement due to conflict with recovery" (on a replica, not a
// serialization failure: replicas do not write).
const sqlStateRecoveryConflict = "40001"

// ReplicaBalancer chooses a replica among the usable ones and returns its index in usable.
// It is called only with a non-empty usable; an index outside [0, len(usable)) means usable[0].
type ReplicaBalancer func(usable []ReplicaState) int

// RoundRobin cycles through the usable replicas. It is safe for concurrent use.
func RoundRobin() ReplicaBalancer {
	var n atomic.Uint64

	return func(usable []ReplicaState) int {
		return int((n.Add(1) - 1) % uint64(len(usable))) //nolint:gosec // the remainder is < len(usable)
	}
}

// LeastLag chooses the usable replica with the minimal Lag; on a tie the first one in the order of the options.
func LeastLag() ReplicaBalancer {
	return func(usable []ReplicaState) int {
		best := 0

		for i, s := range usable {
			if s.Lag < usable[best].Lag {
				best = i
			}
		}

		return best
	}
}

type replica struct {
	name  string
	conn  *sqlx.DB
	db    DB
	state atomic.Pointer[ReplicaState]
}

// Cluster is a DB over a primary and any number of read replicas of PostgreSQL.
//
// Everything goes to primary except read methods called with PreferReplica(ctx) or RequireReplica(ctx),
// outside of a transaction, while a replica is usable (its replication lag is within WithMaxLag).
// The replica is chosen among the usable ones by the ReplicaBalancer (RoundRobin by default).
type Cluster struct {
	cfg         clusterConfig
	primary     DB
	primaryConn *sqlx.DB
	replicas    []*replica

	// owned are the connections closed by Close (set by ConnectCluster only).
	owned []*sqlx.DB

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// NewCluster creates a Cluster over primary. With replicas it starts a background lag check; call Close to stop it.
// Close does not close the given *sqlx.DB: their owner does.
func NewCluster(primary *sqlx.DB, opts ...ClusterOption) (*Cluster, error) {
	c, err := newCluster(primary, opts...)
	if err != nil {
		return nil, err
	}

	c.start()

	return c, nil
}

func newCluster(primary *sqlx.DB, opts ...ClusterOption) (*Cluster, error) {
	if primary == nil {
		return nil, ErrClusterNoPrimary
	}

	cfg := clusterConfig{
		balancer:      RoundRobin(),
		maxLag:        defaultClusterMaxLag,
		interval:      defaultClusterLagInterval,
		conflictRetry: defaultClusterConflictRetry,
	}

	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	c := &Cluster{
		cfg:         cfg,
		primary:     NewDB(primary),
		primaryConn: primary,
		done:        make(chan struct{}),
	}

	for _, spec := range cfg.replicas {
		r := &replica{name: spec.name, conn: spec.conn, db: NewDB(spec.conn)}
		r.state.Store(&ReplicaState{Name: spec.name, Reason: ReasonNotChecked})
		c.replicas = append(c.replicas, r)
	}

	return c, nil
}

// start launches the lag check goroutine if there are replicas.
func (c *Cluster) start() {
	if len(c.replicas) == 0 {
		close(c.done)

		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	go c.runLagLoop(ctx)
}

// Primary returns the client of the primary.
func (c *Cluster) Primary() DB { return c.primary }

// usable returns the usable replicas (except skip) with their states, in the order of the options.
func (c *Cluster) usable(skip *replica) ([]*replica, []ReplicaState) {
	var (
		rs     []*replica
		states []ReplicaState
	)

	for _, r := range c.replicas {
		if r == skip {
			continue
		}

		if st := *r.state.Load(); st.Usable {
			rs = append(rs, r)
			states = append(states, st)
		}
	}

	return rs, states
}

// pick chooses a usable replica (except skip) with the balancer; nil if there is none.
func (c *Cluster) pick(skip *replica) *replica {
	rs, states := c.usable(skip)
	if len(rs) == 0 {
		return nil
	}

	i := c.cfg.balancer(states)
	if i < 0 || i >= len(rs) {
		i = 0
	}

	return rs[i]
}

// Replica returns the client of a usable replica chosen by the balancer, or the primary
// if no replica is usable now or ctx has a tx.
func (c *Cluster) Replica(ctx context.Context) DB {
	// The check must precede the client choice: the replica client would take the tx from ctx too.
	if _, ok := GetTx(ctx); ok {
		return c.primary
	}

	if r := c.pick(nil); r != nil {
		return r.db
	}

	return c.primary
}

// States returns a copy of the states of all replicas in the order of the options; empty without replicas.
func (c *Cluster) States() []ReplicaState {
	states := make([]ReplicaState, len(c.replicas))
	for i, r := range c.replicas {
		states[i] = *r.state.Load()
	}

	return states
}

// State returns the summary state: without replicas {Reason: "no_replica"}; with a usable replica
// {Usable, the minimal Lag among usable, "ok"}; otherwise the Lag and Reason of the first replica.
func (c *Cluster) State() ReplicaState {
	if len(c.replicas) == 0 {
		return ReplicaState{Reason: ReasonNoReplica}
	}

	var (
		sum   ReplicaState
		found bool
	)

	for _, r := range c.replicas {
		st := *r.state.Load()
		if !st.Usable {
			continue
		}

		if !found || st.Lag < sum.Lag {
			sum.Lag = st.Lag
		}

		found = true
	}

	if found {
		sum.Usable, sum.Reason = true, ReasonOK

		return sum
	}

	first := *c.replicas[0].state.Load()

	return ReplicaState{Lag: first.Lag, Reason: first.Reason}
}

// Close stops the lag check and waits for it. A cluster made by ConnectCluster then closes its replicas and
// primary (errors are joined); a cluster made by NewCluster does NOT close the *sqlx.DB: their owner does.
// Repeated Close is a no-op and returns nil.
func (c *Cluster) Close() error {
	var err error

	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}

		<-c.done

		for _, conn := range c.owned {
			err = errors.Join(err, conn.Close())
		}
	})

	<-c.done

	return err
}

// pagingMode keeps the default paging mode of the primary client (see NewPager).
func (c *Cluster) pagingMode() PagingMode {
	if m, ok := c.primary.(pagingModer); ok {
		return m.pagingMode()
	}

	return PagingOffset
}

// readRoute is the node chosen for a read method.
type readRoute struct {
	db       DB
	replica  *replica // nil for primary
	required bool     // RequireReplica: a conflict is retried on another replica, never on primary
}

// route chooses the node of a read method; the first matching rule wins:
//  1. tx in ctx -> primary (the tx is open there)
//  2. ForcePrimary -> primary
//  3. RequireReplica -> a usable replica, else ErrReplicaUnavailable
//  4. PreferReplica -> a usable replica, else primary
//  5. primary
func (c *Cluster) route(ctx context.Context) (readRoute, error) {
	primary := readRoute{db: c.primary}

	if _, ok := GetTx(ctx); ok || forcedPrimary(ctx) {
		return primary, nil
	}

	switch {
	case ReplicaRequired(ctx):
		r := c.pick(nil)
		if r == nil {
			return primary, ErrReplicaUnavailable
		}

		return readRoute{db: r.db, replica: r, required: true}, nil
	case ReplicaPreferred(ctx):
		if r := c.pick(nil); r != nil {
			return readRoute{db: r.db, replica: r}, nil
		}
	}

	return primary, nil
}

// retryRoute returns where a read that failed with err on rt must be repeated once, if anywhere.
func (c *Cluster) retryRoute(rt readRoute, err error) (readRoute, bool) {
	if rt.replica == nil || err == nil || !c.cfg.conflictRetry {
		return rt, false
	}

	if code, _, ok := pgCode(err); !ok || code != sqlStateRecoveryConflict {
		return rt, false
	}

	if !rt.required {
		return readRoute{db: c.primary}, true
	}

	other := c.pick(rt.replica)
	if other == nil {
		return rt, false
	}

	return readRoute{db: other.db, replica: other, required: true}, true
}

func unavailable(method error) error { return fmt.Errorf("%w: %w", method, ErrReplicaUnavailable) }

// Connection returns the connection of the primary.
func (c *Cluster) Connection() *sqlx.DB { return c.primary.Connection() }

// DriverName returns the driver name of the primary.
func (c *Cluster) DriverName() string { return c.primaryConn.DriverName() }

// ExecContext always runs on the primary.
func (c *Cluster) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.primary.ExecContext(ctx, query, args...)
}

// NamedExecContext always runs on the primary.
func (c *Cluster) NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	return c.primary.NamedExecContext(ctx, query, arg)
}

// PrepareContext always runs on the primary.
func (c *Cluster) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return c.primary.PrepareContext(ctx, query)
}

// PrepareNamedContext always runs on the primary.
func (c *Cluster) PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	return c.primary.PrepareNamedContext(ctx, query)
}

// SelectContext is a read method: it is routed to a replica or the primary by the hints of ctx
// (see Cluster) and retried once on a recovery conflict.
func (c *Cluster) SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	rt, err := c.route(ctx)
	if err != nil {
		return unavailable(ErrSelectContext)
	}

	err = rt.db.SelectContext(ctx, dest, query, args...)
	if next, ok := c.retryRoute(rt, err); ok {
		return next.db.SelectContext(ctx, dest, query, args...)
	}

	return err
}

// GetContext is a read method: it is routed like SelectContext.
func (c *Cluster) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	rt, err := c.route(ctx)
	if err != nil {
		return unavailable(ErrGetContext)
	}

	err = rt.db.GetContext(ctx, dest, query, args...)
	if next, ok := c.retryRoute(rt, err); ok {
		return next.db.GetContext(ctx, dest, query, args...)
	}

	return err
}

// QueryContext retries only an error of the call itself; an error from rows.Next/rows.Err is returned as is.
func (c *Cluster) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	rt, err := c.route(ctx)
	if err != nil {
		return nil, unavailable(ErrQueryContext)
	}

	rows, err := rt.db.QueryContext(ctx, query, args...)
	if next, ok := c.retryRoute(rt, err); ok {
		return next.db.QueryContext(ctx, query, args...)
	}

	return rows, err
}

// QueryxContext retries only an error of the call itself; an error from rows.Next/rows.Err is returned as is.
func (c *Cluster) QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	rt, err := c.route(ctx)
	if err != nil {
		return nil, unavailable(ErrQueryxContext)
	}

	rows, err := rt.db.QueryxContext(ctx, query, args...)
	if next, ok := c.retryRoute(rt, err); ok {
		return next.db.QueryxContext(ctx, query, args...)
	}

	return rows, err
}

// QueryRowxContext retries by row.Err() before the row is returned. If a replica is required but none is
// usable, the returned row carries ErrReplicaUnavailable (row.Err(), row.Scan).
func (c *Cluster) QueryRowxContext(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	rt, err := c.route(ctx)
	if err != nil {
		return errRow(ctx, unavailable(ErrQueryRowxContext))
	}

	row := rt.db.QueryRowxContext(ctx, query, args...)
	if next, ok := c.retryRoute(rt, row.Err()); ok {
		return next.db.QueryRowxContext(ctx, query, args...)
	}

	return row
}

// NamedQueryRowxContext is a read method: it is routed like SelectContext.
func (c *Cluster) NamedQueryRowxContext(ctx context.Context, query string, arg any) (*sqlx.Row, error) {
	rt, err := c.route(ctx)
	if err != nil {
		return nil, unavailable(ErrNamedQueryRowxContext)
	}

	row, err := rt.db.NamedQueryRowxContext(ctx, query, arg)
	if next, ok := c.retryRoute(rt, err); ok {
		return next.db.NamedQueryRowxContext(ctx, query, arg)
	}

	return row, err
}

// EachShard is not supported by Cluster.
func (c *Cluster) EachShard(_ func(conn DB) error) error { return ErrMethodNotSupportedInSingle }

// EachShardAsync is not supported by Cluster.
func (c *Cluster) EachShardAsync(_ func(conn DB) error, _ ...int) error {
	return ErrMethodNotSupportedInSingle
}
