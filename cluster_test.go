package sqlkit

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type clusterEnv struct {
	c      *Cluster
	pm, rm sqlmock.Sqlmock
	pc, rc *sqlx.DB
}

// newTestCluster returns a cluster (without lag goroutine) in the given state.
func newTestCluster(t *testing.T, st ReplicaState, opts ...ClusterOption) *clusterEnv {
	t.Helper()

	pc, pm := newMockDB(t)
	rc, rm := newMockDB(t)

	c, err := newCluster(pc, append([]ClusterOption{WithReplica(rc)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}

	st.Name = defaultReplicaName
	c.replicas[0].state.Store(&st)

	return &clusterEnv{c: c, pm: pm, rm: rm, pc: pc, rc: rc}
}

var usable = ReplicaState{Usable: true, Reason: "ok"}

func conflict() error {
	return &pq.Error{Code: "40001", Message: "canceling statement due to conflict with recovery"}
}

type routeOp struct {
	name   string
	read   bool
	expect func(m sqlmock.Sqlmock)
	call   func(ctx context.Context, db DB) error
}

func rowsA() *sqlmock.Rows { return sqlmock.NewRows([]string{"a"}).AddRow(1) }

func routeOps() []routeOp {
	exp := func(kind string) func(m sqlmock.Sqlmock) {
		return func(m sqlmock.Sqlmock) {
			switch kind {
			case "query":
				m.ExpectQuery("SELECT").WillReturnRows(rowsA())
			case "exec":
				m.ExpectExec("INSERT").WillReturnResult(sqlmock.NewResult(0, 1))
			case "prepare":
				m.ExpectPrepare("INSERT")
			}
		}
	}

	return []routeOp{
		{"SelectContext", true, exp("query"), func(ctx context.Context, db DB) error {
			var dst []int

			return db.SelectContext(ctx, &dst, "SELECT a")
		}},
		{"GetContext", true, exp("query"), func(ctx context.Context, db DB) error {
			var dst int

			return db.GetContext(ctx, &dst, "SELECT a")
		}},
		{"QueryContext", true, exp("query"), func(ctx context.Context, db DB) error {
			rows, err := db.QueryContext(ctx, "SELECT a")
			if err != nil {
				return err
			}

			return rows.Close()
		}},
		{"QueryxContext", true, exp("query"), func(ctx context.Context, db DB) error {
			rows, err := db.QueryxContext(ctx, "SELECT a")
			if err != nil {
				return err
			}

			return rows.Close()
		}},
		{"QueryRowxContext", true, exp("query"), func(ctx context.Context, db DB) error {
			var dst int

			return db.QueryRowxContext(ctx, "SELECT a").Scan(&dst)
		}},
		{"NamedQueryRowxContext", true, exp("query"), func(ctx context.Context, db DB) error {
			var dst int

			row, err := db.NamedQueryRowxContext(ctx, "SELECT a WHERE b = :b", map[string]any{"b": 1})
			if err != nil {
				return err
			}

			return row.Scan(&dst)
		}},
		{"ExecContext", false, exp("exec"), func(ctx context.Context, db DB) error {
			_, err := db.ExecContext(ctx, "INSERT INTO t VALUES (1)")

			return err
		}},
		{"NamedExecContext", false, exp("exec"), func(ctx context.Context, db DB) error {
			_, err := db.NamedExecContext(ctx, "INSERT INTO t (a) VALUES (:a)", map[string]any{"a": 1})

			return err
		}},
		{"NamedExecContext batch", false, exp("exec"), func(ctx context.Context, db DB) error {
			batch := []map[string]any{{"a": 1, "b": 2}, {"a": 3, "b": 4}}
			_, err := db.NamedExecContext(ctx, "INSERT INTO t (a, b) VALUES (:a, :b)", batch)

			return err
		}},
		{"PrepareContext", false, exp("prepare"), func(ctx context.Context, db DB) error {
			stmt, err := db.PrepareContext(ctx, "INSERT INTO t VALUES (1)")
			if err != nil {
				return err
			}

			return stmt.Close()
		}},
		{"PrepareNamedContext", false, exp("prepare"), func(ctx context.Context, db DB) error {
			stmt, err := db.PrepareNamedContext(ctx, "INSERT INTO t (a) VALUES (:a)")
			if err != nil {
				return err
			}

			return stmt.Close()
		}},
	}
}

func TestClusterRouting(t *testing.T) {
	scenarios := []struct {
		name        string
		state       ReplicaState
		noReplica   bool
		ctx         func(context.Context) context.Context
		tx          bool
		readReplica bool // expected for read methods; writes always go to primary
	}{
		{name: "no hint", state: usable, ctx: func(c context.Context) context.Context { return c }},
		{name: "prefer replica", state: usable, ctx: PreferReplica, readReplica: true},
		{name: "force primary over prefer", state: usable, ctx: func(c context.Context) context.Context { return ForcePrimary(PreferReplica(c)) }},
		{name: "force primary before prefer", state: usable, ctx: func(c context.Context) context.Context { return PreferReplica(ForcePrimary(c)) }},
		{name: "tx in ctx", state: usable, ctx: PreferReplica, tx: true},
		{name: "not_checked", state: ReplicaState{Reason: "not_checked"}, ctx: PreferReplica},
		{name: "lag_exceeded", state: ReplicaState{Reason: "lag_exceeded"}, ctx: PreferReplica},
		{name: "check_failed", state: ReplicaState{Reason: "check_failed"}, ctx: PreferReplica},
		{name: "replay_unknown", state: ReplicaState{Reason: "replay_unknown"}, ctx: PreferReplica},
		{name: "no_replica", state: ReplicaState{Reason: "no_replica"}, noReplica: true, ctx: PreferReplica},
	}

	for _, sc := range scenarios {
		for _, op := range routeOps() {
			t.Run(sc.name+"/"+op.name, func(t *testing.T) {
				env := newTestCluster(t, sc.state)
				db := DB(env.c)
				target := env.pm

				if sc.noReplica {
					pc, pm := newMockDB(t)

					c, err := NewCluster(pc)
					if err != nil {
						t.Fatal(err)
					}

					t.Cleanup(func() { _ = c.Close() })

					db, target, env = c, pm, &clusterEnv{pm: pm, rm: nil}
				} else if sc.readReplica && op.read {
					target = env.rm
				}

				ctx := sc.ctx(context.Background())

				if sc.tx {
					env.pm.ExpectBegin()

					tx, err := env.pc.BeginTxx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}

					ctx = context.WithValue(ctx, txCtxKey{}, tx)

					defer func() { _ = tx.Rollback() }()
				}

				op.expect(target)

				if err := op.call(ctx, db); err != nil {
					t.Fatalf("call: %v (query went to the wrong pool?)", err)
				}

				if sc.tx {
					env.pm.ExpectRollback()

					if tx, ok := GetTx(ctx); ok {
						_ = tx.Rollback()
					}
				}

				if env.pm != nil {
					if err := env.pm.ExpectationsWereMet(); err != nil {
						t.Fatalf("primary: %v", err)
					}
				}

				if env.rm != nil {
					if err := env.rm.ExpectationsWereMet(); err != nil {
						t.Fatalf("replica: %v", err)
					}
				}
			})
		}
	}
}

func TestClusterConnectionAndShards(t *testing.T) {
	env := newTestCluster(t, usable)

	if env.c.Connection() != env.pc {
		t.Fatal("Connection must be the primary")
	}

	if env.c.Primary().Connection() != env.pc {
		t.Fatal("Primary() must be the primary")
	}

	if err := env.c.EachShard(func(DB) error { return nil }); !errors.Is(err, ErrMethodNotSupportedInSingle) {
		t.Fatalf("EachShard: %v", err)
	}

	if err := env.c.EachShardAsync(func(DB) error { return nil }); !errors.Is(err, ErrMethodNotSupportedInSingle) {
		t.Fatalf("EachShardAsync: %v", err)
	}
}

func TestClusterReplicaMethod(t *testing.T) {
	env := newTestCluster(t, usable)

	if env.c.Replica(context.Background()).Connection() != env.rc {
		t.Fatal("usable replica expected")
	}

	tx := &sqlx.Tx{}
	if env.c.Replica(context.WithValue(context.Background(), txCtxKey{}, tx)).Connection() != env.pc {
		t.Fatal("tx in ctx must give primary")
	}

	env.c.replicas[0].state.Store(&ReplicaState{Name: defaultReplicaName, Reason: "lag_exceeded"})

	if env.c.Replica(context.Background()).Connection() != env.pc {
		t.Fatal("unusable replica must give primary")
	}
}

func TestClusterTxCtxAfterCommitStaysPrimary(t *testing.T) {
	env := newTestCluster(t, usable)

	env.pm.ExpectBegin()
	env.pm.ExpectCommit()

	tx, err := env.pc.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	ctx := PreferReplica(context.WithValue(context.Background(), txCtxKey{}, tx))

	// the dead tx is still "a tx in ctx": the read must not go to the replica (and fails on the dead tx)
	var dst []int
	if err = env.c.SelectContext(ctx, &dst, "SELECT a"); err == nil {
		t.Fatal("expected error from the committed tx")
	}

	if err = env.rm.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClusterOptions(t *testing.T) {
	pc, _ := newMockDB(t)

	if _, err := NewCluster(nil); !errors.Is(err, ErrClusterNoPrimary) {
		t.Fatalf("nil primary: %v", err)
	}

	for name, opt := range map[string]ClusterOption{
		"maxLag 0":    WithMaxLag(0),
		"maxLag <0":   WithMaxLag(-1),
		"interval 0":  WithLagCheckInterval(0),
		"interval <0": WithLagCheckInterval(-1),
	} {
		if _, err := NewCluster(pc, opt); !errors.Is(err, ErrClusterOption) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	c, err := NewCluster(pc, WithReplica(nil))
	if err != nil {
		t.Fatal(err)
	}

	if st := c.State(); st.Usable || st.Reason != "no_replica" {
		t.Fatalf("state %+v", st)
	}

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClusterCtxHints(t *testing.T) {
	ctx := context.Background()
	if ReplicaPreferred(ctx) {
		t.Fatal("default must not prefer replica")
	}

	if !ReplicaPreferred(PreferReplica(ctx)) {
		t.Fatal("PreferReplica")
	}

	if ReplicaPreferred(ForcePrimary(PreferReplica(ctx))) || ReplicaPreferred(PreferReplica(ForcePrimary(ctx))) {
		t.Fatal("ForcePrimary must override")
	}
}

func TestClusterPagingModeFromPrimary(t *testing.T) {
	env := newTestCluster(t, usable)

	if env.c.pagingMode() != PagingOffset {
		t.Fatal("default paging mode expected")
	}
}

// --- recovery conflict retry ---

func TestClusterRetryOnConflict(t *testing.T) {
	ctx := PreferReplica(context.Background())

	t.Run("Select", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst []int
		if err := env.c.SelectContext(ctx, &dst, "SELECT a"); err != nil || len(dst) != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		checkMet(t, env)
	})

	t.Run("Get", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(ctx, &dst, "SELECT a"); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		checkMet(t, env)
	})

	t.Run("QueryRowx", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.QueryRowxContext(ctx, "SELECT a").Scan(&dst); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		checkMet(t, env)
	})

	t.Run("NamedQueryRowx", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		row, err := env.c.NamedQueryRowxContext(ctx, "SELECT a WHERE b = :b", map[string]any{"b": 1})
		if err != nil {
			t.Fatal(err)
		}

		var dst int
		if err = row.Scan(&dst); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		checkMet(t, env)
	})

	t.Run("Query and Queryx", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		rows, err := env.c.QueryContext(ctx, "SELECT a")
		if err != nil {
			t.Fatal(err)
		}

		_ = rows.Close()

		xrows, err := env.c.QueryxContext(ctx, "SELECT a")
		if err != nil {
			t.Fatal(err)
		}

		_ = xrows.Close()

		checkMet(t, env)
	})

	t.Run("second conflict from primary is returned", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnError(conflict())

		var dst []int

		err := env.c.SelectContext(ctx, &dst, "SELECT a")
		if code, _, ok := pgCode(err); !ok || code != "40001" {
			t.Fatalf("expected 40001, got %v", err)
		}

		checkMet(t, env)
	})

	t.Run("error from rows.Next is not retried", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnRows(rowsA().AddRow(2).RowError(1, conflict()))

		rows, err := env.c.QueryxContext(ctx, "SELECT a")
		if err != nil {
			t.Fatal(err)
		}

		for rows.Next() {
		}

		if code, _, ok := pgCode(rows.Err()); !ok || code != "40001" {
			t.Fatalf("rows.Err: %v", rows.Err())
		}

		_ = rows.Close()

		checkMet(t, env)
	})

	t.Run("25006 is not retried", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.rm.ExpectQuery("SELECT").WillReturnError(&pq.Error{Code: "25006"})

		var dst []int

		err := env.c.SelectContext(ctx, &dst, "SELECT a")
		if code, _, ok := pgCode(err); !ok || code != "25006" {
			t.Fatalf("expected 25006, got %v", err)
		}

		checkMet(t, env)
	})

	t.Run("network error is not retried", func(t *testing.T) {
		env := newTestCluster(t, usable)
		boom := errors.New("connection reset")
		env.rm.ExpectQuery("SELECT").WillReturnError(boom)

		var dst int
		if err := env.c.GetContext(ctx, &dst, "SELECT a"); !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}

		checkMet(t, env)
	})

	t.Run("retry disabled", func(t *testing.T) {
		env := newTestCluster(t, usable, WithRecoveryConflictRetry(false))
		env.rm.ExpectQuery("SELECT").WillReturnError(conflict())

		var dst int
		if err := env.c.GetContext(ctx, &dst, "SELECT a"); err == nil {
			t.Fatal("expected error")
		}

		checkMet(t, env)
	})

	t.Run("conflict on primary is not retried", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.pm.ExpectQuery("SELECT").WillReturnError(conflict())

		var dst int
		if err := env.c.GetContext(context.Background(), &dst, "SELECT a"); err == nil {
			t.Fatal("expected error")
		}

		checkMet(t, env)
	})
}

func checkMet(t *testing.T, env *clusterEnv) {
	t.Helper()

	if err := env.pm.ExpectationsWereMet(); err != nil {
		t.Fatalf("primary: %v", err)
	}

	if err := env.rm.ExpectationsWereMet(); err != nil {
		t.Fatalf("replica: %v", err)
	}
}
