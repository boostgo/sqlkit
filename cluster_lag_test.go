package sqlkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const testPrimaryLSN = "0/AA"

func expectPrimaryLSN(m sqlmock.Sqlmock) {
	m.ExpectQuery("pg_current_wal_lsn").WillReturnRows(sqlmock.NewRows([]string{"lsn"}).AddRow(testPrimaryLSN))
}

func expectReplica(m sqlmock.Sqlmock, inRecovery bool, caughtUp, seconds any) {
	m.ExpectQuery("pg_is_in_recovery").WithArgs(testPrimaryLSN).WillReturnRows(
		sqlmock.NewRows([]string{"rec", "lsn", "caught", "lag"}).AddRow(inRecovery, "0/10", caughtUp, seconds))
}

func check(t *testing.T, env *clusterEnv, inRecovery bool, caughtUp, seconds any) ReplicaState {
	t.Helper()

	expectPrimaryLSN(env.pm)
	expectReplica(env.rm, inRecovery, caughtUp, seconds)
	env.c.checkOnce(context.Background())
	checkMet(t, env)

	return env.c.State()
}

func TestClusterInitialState(t *testing.T) {
	pc, _ := newMockDB(t)
	rc, _ := newMockDB(t)

	c, err := newCluster(pc, WithReplica(rc))
	if err != nil {
		t.Fatal(err)
	}

	if st := c.State(); st.Usable || st.Reason != "not_checked" {
		t.Fatalf("%+v", st)
	}
}

func TestClusterLagCalc(t *testing.T) {
	cases := []struct {
		name       string
		inRecovery bool
		caughtUp   any
		seconds    any
		want       ReplicaState
	}{
		{"not in recovery", false, nil, nil, ReplicaState{Usable: true, Reason: "ok"}},
		{"caught up", true, true, 500.0, ReplicaState{Usable: true, Reason: "ok"}},
		{"behind, small lag", true, false, 2.5, ReplicaState{Usable: true, Lag: 2500 * time.Millisecond, Reason: "ok"}},
		{"behind, big lag", true, false, 31.0, ReplicaState{Lag: 31 * time.Second, Reason: "lag_exceeded"}},
		{"behind, null caught up", true, nil, 3.0, ReplicaState{Usable: true, Lag: 3 * time.Second, Reason: "ok"}},
		{"behind, negative seconds", true, false, -1.0, ReplicaState{Usable: true, Reason: "ok"}},
		{"behind, timestamp NULL", true, false, nil, ReplicaState{Reason: "replay_unknown"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestCluster(t, ReplicaState{Reason: "not_checked"})

			if got := check(t, env, tc.inRecovery, tc.caughtUp, tc.seconds); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestClusterLagCheckFailed(t *testing.T) {
	want := ReplicaState{Reason: "check_failed"}

	t.Run("primary step", func(t *testing.T) {
		env := newTestCluster(t, usable)
		env.pm.ExpectQuery("pg_current_wal_lsn").WillReturnError(errors.New("boom"))
		env.c.checkOnce(context.Background())
		checkMet(t, env)

		if got := env.c.State(); got != want {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("replica step", func(t *testing.T) {
		env := newTestCluster(t, usable)
		expectPrimaryLSN(env.pm)
		env.rm.ExpectQuery("pg_is_in_recovery").WillReturnError(errors.New("boom"))
		env.c.checkOnce(context.Background())
		checkMet(t, env)

		if got := env.c.State(); got != want {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		env := newTestCluster(t, usable, WithLagCheckInterval(30*time.Millisecond))
		expectPrimaryLSN(env.pm)
		env.rm.ExpectQuery("pg_is_in_recovery").WillDelayFor(time.Second).
			WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d"}).AddRow(true, "0/1", true, 0.0))

		started := time.Now()
		env.c.checkOnce(context.Background())

		if time.Since(started) > 700*time.Millisecond {
			t.Fatal("check did not time out")
		}

		if got := env.c.State(); got != want {
			t.Fatalf("%+v", got)
		}
	})
}

func TestClusterHysteresis(t *testing.T) {
	env := newTestCluster(t, ReplicaState{Reason: "not_checked"}, WithMaxLag(30*time.Second))

	steps := []struct {
		secs   float64
		usable bool
	}{
		{0, true},   // caught up enough
		{30, true},  // exactly maxLag: still usable
		{31, false}, // > maxLag
		{20, false}, // > maxLag/2: still unusable
		{15, true},  // <= maxLag/2
		{31, false},
	}

	for _, s := range steps {
		got := check(t, env, true, false, s.secs)
		if got.Usable != s.usable {
			t.Fatalf("lag %vs: usable=%v want %v (%+v)", s.secs, got.Usable, s.usable, got)
		}

		if !s.usable && got.Reason != "lag_exceeded" {
			t.Fatalf("reason %q", got.Reason)
		}
	}
}

func TestClusterStateHookOnlyOnChange(t *testing.T) {
	var got []ReplicaState

	env := newTestCluster(t, ReplicaState{Reason: "not_checked"},
		WithMaxLag(30*time.Second), WithStateHook(func(s ReplicaState) { got = append(got, s) }))

	check(t, env, false, nil, nil)   // not_checked -> ok: hook
	check(t, env, true, false, 1.0)  // ok, lag differs: no hook
	check(t, env, true, false, 2.0)  // no hook
	check(t, env, true, false, 31.0) // -> lag_exceeded: hook
	check(t, env, true, false, 40.0) // still lag_exceeded: no hook
	check(t, env, true, false, 20.0) // no hook
	check(t, env, true, false, 1.0)  // -> ok: hook

	reasons := make([]string, len(got))
	for i, s := range got {
		reasons[i] = s.Reason
	}

	if len(reasons) != 3 || reasons[0] != "ok" || reasons[1] != "lag_exceeded" || reasons[2] != "ok" {
		t.Fatalf("hook calls: %v", reasons)
	}
}

func TestClusterBackgroundCheckAndClose(t *testing.T) {
	pc, pm := newMockDB(t)
	rc, rm := newMockDB(t)

	changed := make(chan ReplicaState, 4)

	c, err := newCluster(pc, WithReplica(rc), WithLagCheckInterval(time.Hour),
		WithStateHook(func(s ReplicaState) { changed <- s }))
	if err != nil {
		t.Fatal(err)
	}

	expectPrimaryLSN(pm)
	expectReplica(rm, false, nil, nil)

	c.start()

	select {
	case s := <-changed:
		if !s.Usable || s.Reason != "ok" {
			t.Fatalf("%+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no first check")
	}

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}

	if err = c.Close(); err != nil { // second Close is a no-op
		t.Fatal(err)
	}

	waitMet(t, pm)
	waitMet(t, rm)
}

func TestClusterCloseWithoutReplica(t *testing.T) {
	pc, _ := newMockDB(t)

	c, err := NewCluster(pc)
	if err != nil {
		t.Fatal(err)
	}

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}
