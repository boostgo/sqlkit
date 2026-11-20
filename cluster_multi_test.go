package sqlkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type multiEnv struct {
	c   *Cluster
	pm  sqlmock.Sqlmock
	pc  *sqlx.DB
	rms []sqlmock.Sqlmock
	rcs []*sqlx.DB
}

var replicaNames = []string{"a", "b", "c"}

// newMultiCluster returns a cluster (without lag goroutine) with one replica per state, named a, b, c.
func newMultiCluster(t *testing.T, states []ReplicaState, opts ...ClusterOption) *multiEnv {
	t.Helper()

	pc, pm := newMockDB(t)
	env := &multiEnv{pm: pm, pc: pc}

	all := make([]ClusterOption, 0, len(states))

	for i := range states {
		rc, rm := newMockDB(t)
		env.rcs, env.rms = append(env.rcs, rc), append(env.rms, rm)
		all = append(all, WithNamedReplica(replicaNames[i], rc))
	}

	c, err := newCluster(pc, append(all, opts...)...)
	if err != nil {
		t.Fatal(err)
	}

	for i, st := range states {
		st.Name = replicaNames[i]
		c.replicas[i].state.Store(&st)
	}

	env.c = c

	return env
}

func (e *multiEnv) met(t *testing.T) {
	t.Helper()

	if err := e.pm.ExpectationsWereMet(); err != nil {
		t.Fatalf("primary: %v", err)
	}

	for i, m := range e.rms {
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatalf("replica %s: %v", replicaNames[i], err)
		}
	}
}

var (
	down = ReplicaState{Reason: ReasonCheckFailed}

	methodSentinel = map[string]error{
		"SelectContext":         ErrSelectContext,
		"GetContext":            ErrGetContext,
		"QueryContext":          ErrQueryContext,
		"QueryxContext":         ErrQueryxContext,
		"QueryRowxContext":      ErrQueryRowxContext,
		"NamedQueryRowxContext": ErrNamedQueryRowxContext,
	}
)

// TestClusterPrecedenceTable covers the rules 1-5 of the table "who gets a read" for every method;
// a query on a wrong mock fails the test.
func TestClusterPrecedenceTable(t *testing.T) {
	type node int

	const (
		primary node = iota
		replicaA
		unavailableErr
	)

	scenarios := []struct {
		name   string
		states []ReplicaState
		ctx    func(context.Context) context.Context
		tx     bool
		want   node // for reads; writes always go to primary
	}{
		// rule 1: tx
		{"1 tx beats require", []ReplicaState{usable}, RequireReplica, true, primary},
		{"1 tx beats prefer", []ReplicaState{usable}, PreferReplica, true, primary},
		{"1 tx with no replica usable and require", []ReplicaState{down}, RequireReplica, true, primary},
		// rule 2: ForcePrimary
		{"2 force beats require", []ReplicaState{usable}, func(c context.Context) context.Context { return ForcePrimary(RequireReplica(c)) }, false, primary},
		{"2 require then force", []ReplicaState{usable}, func(c context.Context) context.Context { return RequireReplica(ForcePrimary(c)) }, false, primary},
		{"2 force beats prefer", []ReplicaState{usable}, func(c context.Context) context.Context { return ForcePrimary(PreferReplica(c)) }, false, primary},
		{"2 force + require, replicas down", []ReplicaState{down}, func(c context.Context) context.Context { return ForcePrimary(RequireReplica(c)) }, false, primary},
		// rule 3: RequireReplica
		{"3 require usable", []ReplicaState{usable}, RequireReplica, false, replicaA},
		{"3 require none usable", []ReplicaState{down}, RequireReplica, false, unavailableErr},
		{"3 require lag exceeded", []ReplicaState{{Reason: ReasonLagExceeded}}, RequireReplica, false, unavailableErr},
		{"3 require without replicas", nil, RequireReplica, false, unavailableErr},
		{"3 require beats prefer, usable", []ReplicaState{usable}, func(c context.Context) context.Context { return PreferReplica(RequireReplica(c)) }, false, replicaA},
		{"3 require beats prefer, none usable", []ReplicaState{down}, func(c context.Context) context.Context { return PreferReplica(RequireReplica(c)) }, false, unavailableErr},
		// rule 4: PreferReplica
		{"4 prefer usable", []ReplicaState{usable}, PreferReplica, false, replicaA},
		{"4 prefer none usable", []ReplicaState{down}, PreferReplica, false, primary},
		{"4 prefer without replicas", nil, PreferReplica, false, primary},
		// rule 5
		{"5 no hint", []ReplicaState{usable}, func(c context.Context) context.Context { return c }, false, primary},
	}

	for _, sc := range scenarios {
		for _, op := range routeOps() {
			t.Run(sc.name+"/"+op.name, func(t *testing.T) {
				env := newMultiCluster(t, sc.states)
				ctx := sc.ctx(context.Background())

				want := sc.want
				if !op.read && want != primary {
					want = primary
				}

				if sc.tx {
					env.pm.ExpectBegin()

					tx, err := env.pc.BeginTxx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}

					ctx = context.WithValue(ctx, txCtxKey{}, tx)
				}

				switch want {
				case primary:
					op.expect(env.pm)
				case replicaA:
					op.expect(env.rms[0])
				case unavailableErr:
				}

				err := op.call(ctx, env.c)

				if want == unavailableErr {
					if !errors.Is(err, ErrReplicaUnavailable) || !errors.Is(err, methodSentinel[op.name]) {
						t.Fatalf("want ErrReplicaUnavailable wrapped by %s, got %v", op.name, err)
					}
				} else if err != nil {
					t.Fatalf("call: %v (query went to the wrong pool?)", err)
				}

				if sc.tx {
					env.pm.ExpectRollback()

					if tx, ok := GetTx(ctx); ok {
						_ = tx.Rollback()
					}
				}

				env.met(t)
			})
		}
	}
}

func TestClusterRequireReplicaQueryRowxErrRow(t *testing.T) {
	env := newMultiCluster(t, []ReplicaState{down})

	row := env.c.QueryRowxContext(RequireReplica(context.Background()), "SELECT a")
	if err := row.Err(); !errors.Is(err, ErrReplicaUnavailable) {
		t.Fatalf("row.Err: %v", err)
	}

	var dst int
	if err := row.Scan(&dst); !errors.Is(err, ErrReplicaUnavailable) {
		t.Fatalf("Scan: %v", err)
	}

	env.met(t)
}

func TestClusterReplicaRequiredHint(t *testing.T) {
	ctx := context.Background()

	if ReplicaRequired(ctx) || !ReplicaRequired(RequireReplica(ctx)) {
		t.Fatal("RequireReplica")
	}

	if ReplicaRequired(ForcePrimary(RequireReplica(ctx))) || ReplicaRequired(RequireReplica(ForcePrimary(ctx))) {
		t.Fatal("ForcePrimary must override")
	}
}

// --- balancers ---

func TestClusterRoundRobin(t *testing.T) {
	env := newMultiCluster(t, []ReplicaState{usable, down, usable})
	ctx := PreferReplica(context.Background())

	// a, c, a, c: the unusable "b" is skipped
	for _, i := range []int{0, 2, 0, 2} {
		env.rms[i].ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(ctx, &dst, "SELECT a"); err != nil {
			t.Fatal(err)
		}
	}

	env.met(t)
}

func TestClusterRoundRobinConcurrent(t *testing.T) {
	rr := RoundRobin()
	states := make([]ReplicaState, 3)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		counts [3]int
	)

	for range 30 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 100 {
				i := rr(states)

				mu.Lock()
				counts[i]++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if counts != [3]int{1000, 1000, 1000} {
		t.Fatalf("uneven: %v", counts)
	}
}

func TestClusterLeastLag(t *testing.T) {
	states := []ReplicaState{
		{Usable: true, Lag: 5 * time.Second, Reason: ReasonOK},
		{Usable: true, Lag: time.Second, Reason: ReasonOK},
		{Usable: true, Lag: time.Second, Reason: ReasonOK},
	}

	env := newMultiCluster(t, states, WithReplicaBalancer(LeastLag()))
	ctx := PreferReplica(context.Background())

	for range 3 { // the minimum is "b"; "c" has the same lag but is later
		env.rms[1].ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(ctx, &dst, "SELECT a"); err != nil {
			t.Fatal(err)
		}
	}

	env.met(t)

	if got := LeastLag()(states[:1]); got != 0 {
		t.Fatalf("single: %d", got)
	}
}

func TestClusterBalancerOutOfRange(t *testing.T) {
	for _, idx := range []int{-1, 2, 100} {
		env := newMultiCluster(t, []ReplicaState{usable, usable},
			WithReplicaBalancer(func([]ReplicaState) int { return idx }))

		env.rms[0].ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(PreferReplica(context.Background()), &dst, "SELECT a"); err != nil {
			t.Fatal(err)
		}

		env.met(t)
	}
}

func TestClusterBalancerSeesOnlyUsable(t *testing.T) {
	var seen []ReplicaState

	env := newMultiCluster(t, []ReplicaState{down, usable, usable},
		WithReplicaBalancer(func(u []ReplicaState) int {
			seen = u

			return 1
		}))

	env.rms[2].ExpectQuery("SELECT").WillReturnRows(rowsA())

	var dst int
	if err := env.c.GetContext(PreferReplica(context.Background()), &dst, "SELECT a"); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 2 || seen[0].Name != "b" || seen[1].Name != "c" {
		t.Fatalf("balancer got %+v", seen)
	}

	env.met(t)
}

func TestClusterReplicaMethodBalances(t *testing.T) {
	env := newMultiCluster(t, []ReplicaState{usable, usable})
	ctx := context.Background()

	if env.c.Replica(ctx).Connection() != env.rcs[0] || env.c.Replica(ctx).Connection() != env.rcs[1] {
		t.Fatal("Replica must go round robin")
	}

	txCtx := context.WithValue(ctx, txCtxKey{}, &sqlx.Tx{})
	if env.c.Replica(txCtx).Connection() != env.pc {
		t.Fatal("tx in ctx must give primary")
	}

	env2 := newMultiCluster(t, []ReplicaState{down})
	if env2.c.Replica(ctx).Connection() != env2.pc {
		t.Fatal("no usable replica must give primary")
	}
}

// --- options ---

func TestClusterReplicaOptions(t *testing.T) {
	pc, _ := newMockDB(t)
	r1, _ := newMockDB(t)
	r2, _ := newMockDB(t)

	for name, opts := range map[string][]ClusterOption{
		"empty name":            {WithNamedReplica("", r1)},
		"duplicate name":        {WithNamedReplica("x", r1), WithNamedReplica("x", r2)},
		"default after default": {WithReplica(r1), WithNamedReplica("default", r2)},
		"nil balancer":          {WithReplicaBalancer(nil)},
	} {
		if _, err := newCluster(pc, opts...); !errors.Is(err, ErrClusterOption) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	t.Run("WithReplica twice replaces", func(t *testing.T) {
		c, err := newCluster(pc, WithNamedReplica("x", r1), WithReplica(r1), WithReplica(r2))
		if err != nil {
			t.Fatal(err)
		}

		if len(c.replicas) != 2 || c.replicas[1].name != "default" || c.replicas[1].conn != r2 {
			t.Fatalf("%+v", c.States())
		}
	})

	t.Run("nil replicas are skipped", func(t *testing.T) {
		c, err := newCluster(pc, WithReplica(nil), WithNamedReplica("", nil), WithNamedReplica("x", nil))
		if err != nil {
			t.Fatal(err)
		}

		if len(c.replicas) != 0 || c.State().Reason != ReasonNoReplica {
			t.Fatalf("%+v", c.States())
		}
	})

	t.Run("WithReplica(nil) removes default", func(t *testing.T) {
		c, err := newCluster(pc, WithReplica(r1), WithNamedReplica("x", r2), WithReplica(nil))
		if err != nil {
			t.Fatal(err)
		}

		if len(c.replicas) != 1 || c.replicas[0].name != "x" {
			t.Fatalf("%+v", c.States())
		}
	})
}

// --- states ---

func TestClusterSummaryState(t *testing.T) {
	t.Run("no replicas", func(t *testing.T) {
		pc, _ := newMockDB(t)

		c, err := newCluster(pc)
		if err != nil {
			t.Fatal(err)
		}

		if got := c.State(); got != (ReplicaState{Reason: ReasonNoReplica}) {
			t.Fatalf("%+v", got)
		}

		if len(c.States()) != 0 {
			t.Fatal("States must be empty")
		}
	})

	t.Run("usable: min lag among usable", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{
			{Usable: true, Lag: 7 * time.Second, Reason: ReasonOK},
			{Lag: time.Millisecond, Reason: ReasonLagExceeded},
			{Usable: true, Lag: 3 * time.Second, Reason: ReasonOK},
		})

		want := ReplicaState{Usable: true, Lag: 3 * time.Second, Reason: ReasonOK}
		if got := env.c.State(); got != want {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("none usable: first replica", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{
			{Lag: 40 * time.Second, Reason: ReasonLagExceeded},
			{Reason: ReasonCheckFailed},
		})

		want := ReplicaState{Lag: 40 * time.Second, Reason: ReasonLagExceeded}
		if got := env.c.State(); got != want {
			t.Fatalf("%+v", got)
		}
	})
}

func TestClusterStatesCopyInOrder(t *testing.T) {
	env := newMultiCluster(t, []ReplicaState{usable, down, usable})

	states := env.c.States()
	if len(states) != 3 || states[0].Name != "a" || states[1].Name != "b" || states[2].Name != "c" ||
		!states[0].Usable || states[1].Usable {
		t.Fatalf("%+v", states)
	}

	states[0].Usable = false

	if !env.c.States()[0].Usable {
		t.Fatal("States must return a copy")
	}
}

// --- lag of N replicas ---

func expectReplicaN(env *multiEnv, i int, inRecovery bool, caughtUp, seconds any) {
	expectReplica(env.rms[i], inRecovery, caughtUp, seconds)
}

func TestClusterLagOfManyReplicas(t *testing.T) {
	t.Run("one replica failed, others ok", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{{Reason: ReasonNotChecked}, {Reason: ReasonNotChecked}, {Reason: ReasonNotChecked}})

		expectPrimaryLSN(env.pm) // once for all replicas
		expectReplicaN(env, 0, false, nil, nil)
		env.rms[1].ExpectQuery("pg_is_in_recovery").WillReturnError(errors.New("boom"))
		expectReplicaN(env, 2, true, false, 2.0)

		env.c.checkOnce(context.Background())
		env.met(t)

		st := env.c.States()
		if !st[0].Usable || st[1].Reason != ReasonCheckFailed || st[1].Usable || !st[2].Usable || st[2].Lag != 2*time.Second {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("primary failed: all check_failed", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.pm.ExpectQuery("pg_current_wal_lsn").WillReturnError(errors.New("boom"))

		env.c.checkOnce(context.Background())
		env.met(t) // replicas were not asked

		for _, s := range env.c.States() {
			if s.Usable || s.Reason != ReasonCheckFailed {
				t.Fatalf("%+v", s)
			}
		}
	})

	t.Run("own hysteresis", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{{Reason: ReasonNotChecked}, {Reason: ReasonNotChecked}}, WithMaxLag(30*time.Second))

		step := func(a, b float64) []ReplicaState {
			expectPrimaryLSN(env.pm)
			expectReplicaN(env, 0, true, false, a)
			expectReplicaN(env, 1, true, false, b)
			env.c.checkOnce(context.Background())
			env.met(t)

			return env.c.States()
		}

		step(1, 1)

		if s := step(31, 20); s[0].Usable || !s[1].Usable { // a > maxLag; b still within maxLag
			t.Fatalf("%+v", s)
		}

		if s := step(20, 31); s[0].Usable || s[1].Usable { // a is still > maxLag/2; b > maxLag
			t.Fatalf("%+v", s)
		}

		if s := step(15, 20); !s[0].Usable || s[1].Usable { // a back at maxLag/2; b still > maxLag/2
			t.Fatalf("%+v", s)
		}
	})

	t.Run("replicas are checked in parallel with own timeout", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable, usable}, WithLagCheckInterval(100*time.Millisecond))
		expectPrimaryLSN(env.pm)

		for i := range 3 {
			env.rms[i].ExpectQuery("pg_is_in_recovery").WillDelayFor(time.Second).
				WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d"}).AddRow(true, "0/1", true, 0.0))
		}

		started := time.Now()
		env.c.checkOnce(context.Background())

		if d := time.Since(started); d > 280*time.Millisecond { // 3 sequential timeouts would be 300ms
			t.Fatalf("checks are not parallel: %s", d)
		}

		for _, s := range env.c.States() {
			if s.Reason != ReasonCheckFailed {
				t.Fatalf("%+v", s)
			}
		}
	})
}

func TestClusterHookPerReplicaOnChange(t *testing.T) {
	var got []ReplicaState

	env := newMultiCluster(t, []ReplicaState{{Reason: ReasonNotChecked}, {Reason: ReasonNotChecked}},
		WithStateHook(func(s ReplicaState) { got = append(got, s) }))

	step := func(a, b float64) {
		expectPrimaryLSN(env.pm)
		expectReplicaN(env, 0, true, false, a)
		expectReplicaN(env, 1, true, false, b)
		env.c.checkOnce(context.Background())
		env.met(t)
	}

	step(1, 1)  // a: ok, b: ok -> two hooks
	step(2, 40) // a: no change; b: lag_exceeded -> one hook
	step(3, 41) // no change
	step(4, 1)  // b: ok -> one hook

	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, fmt.Sprintf("%s:%s", s.Name, s.Reason))
	}

	want := []string{"a:ok", "b:ok", "b:lag_exceeded", "b:ok"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("hook calls: %v, want %v", names, want)
	}
}

// --- 40001 retry ---

func TestClusterRetryOnConflictManyReplicas(t *testing.T) {
	prefer := PreferReplica(context.Background())
	require := RequireReplica(context.Background())

	t.Run("prefer: once on primary", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(prefer, &dst, "SELECT a"); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		env.met(t)
	})

	t.Run("require: once on another replica", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[1].ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.GetContext(require, &dst, "SELECT a"); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		env.met(t)
	})

	t.Run("require: second conflict is returned, primary untouched", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[2].ExpectQuery("SELECT").WillReturnError(conflict()) // the balancer works on [b, c] and gives c

		var dst []int

		err := env.c.SelectContext(require, &dst, "SELECT a")
		if code, _, ok := pgCode(err); !ok || code != "40001" {
			t.Fatalf("%v", err)
		}

		env.met(t) // replica "b" was not asked: one retry only
	})

	t.Run("require: no other replica -> original error, primary untouched", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, down})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())

		var dst int

		err := env.c.GetContext(require, &dst, "SELECT a")
		if code, _, ok := pgCode(err); !ok || code != "40001" {
			t.Fatalf("%v", err)
		}

		env.met(t)
	})

	t.Run("require: QueryRowx and NamedQueryRowx", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[1].ExpectQuery("SELECT").WillReturnRows(rowsA())

		var dst int
		if err := env.c.QueryRowxContext(require, "SELECT a").Scan(&dst); err != nil || dst != 1 {
			t.Fatalf("%v %v", err, dst)
		}

		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[1].ExpectQuery("SELECT").WillReturnError(conflict())

		row, err := env.c.NamedQueryRowxContext(require, "SELECT a WHERE b = :b", map[string]any{"b": 1})
		if code, _, ok := pgCode(err); !ok || code != "40001" {
			t.Fatalf("row=%v err=%v", row, err)
		}

		env.met(t)
	})

	t.Run("require: Query and Queryx", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[1].ExpectQuery("SELECT").WillReturnRows(rowsA())
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())
		env.rms[1].ExpectQuery("SELECT").WillReturnRows(rowsA())

		rows, err := env.c.QueryContext(require, "SELECT a")
		if err != nil {
			t.Fatal(err)
		}

		_ = rows.Close()

		xrows, err := env.c.QueryxContext(require, "SELECT a")
		if err != nil {
			t.Fatal(err)
		}

		_ = xrows.Close()

		env.met(t)
	})

	t.Run("25006 is not retried", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable})
		env.rms[0].ExpectQuery("SELECT").WillReturnError(&pq.Error{Code: "25006"})

		var dst int
		if err := env.c.GetContext(require, &dst, "SELECT a"); err == nil {
			t.Fatal("expected error")
		}

		env.met(t)
	})

	t.Run("retry disabled", func(t *testing.T) {
		env := newMultiCluster(t, []ReplicaState{usable, usable}, WithRecoveryConflictRetry(false))
		env.rms[0].ExpectQuery("SELECT").WillReturnError(conflict())

		var dst int
		if err := env.c.GetContext(require, &dst, "SELECT a"); err == nil {
			t.Fatal("expected error")
		}

		env.met(t)
	})
}

func TestClusterCloseDoesNotCloseForeignConns(t *testing.T) {
	env := newMultiCluster(t, []ReplicaState{usable})
	close(env.c.done) // the lag goroutine was not started

	if err := env.c.Close(); err != nil {
		t.Fatal(err)
	}

	env.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())
	env.rms[0].ExpectQuery("SELECT").WillReturnRows(rowsA())

	var dst int
	if err := env.pc.GetContext(context.Background(), &dst, "SELECT a"); err != nil {
		t.Fatalf("primary was closed: %v", err)
	}

	if err := env.rcs[0].GetContext(context.Background(), &dst, "SELECT a"); err != nil {
		t.Fatalf("replica was closed: %v", err)
	}
}
