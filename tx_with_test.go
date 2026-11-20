package sqlkit

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestWithTx_FnError_RollsBack(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	want := errors.New("boom")
	err := WithTx(context.Background(), NewTransactor(db), func(ctx context.Context) error {
		if !NewTransactor(db).IsTx(ctx) {
			t.Error("fn must run in tx")
		}

		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithTx_FnAndRollbackError_Joined(t *testing.T) {
	db, mock := newMockDB(t)
	rb := errors.New("rb failed")
	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(rb)

	want := errors.New("boom")
	err := WithTx(context.Background(), NewTransactor(db), func(context.Context) error { return want })
	if !errors.Is(err, want) || !errors.Is(err, rb) {
		t.Fatalf("got %v", err)
	}
}

func TestWithTx_Panic_RollsBackAndRepanics(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	type custom struct{ n int }

	defer func() {
		if p := recover(); p != (custom{1}) {
			t.Fatalf("want original panic value, got %v", p)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}()

	_ = WithTx(context.Background(), NewTransactor(db), func(context.Context) error { panic(custom{1}) })
	t.Fatal("unreachable")
}

func TestWithTx_CommitError_Returned(t *testing.T) {
	db, mock := newMockDB(t)
	want := errors.New("commit failed")
	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(want)

	err := WithTx(context.Background(), NewTransactor(db), func(context.Context) error { return nil })
	if !errors.Is(err, want) || !errors.Is(err, ErrTransactorCommit) {
		t.Fatalf("got %v", err)
	}
}

func TestWithTx_Success_Commits(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectCommit()

	if err := WithTx(context.Background(), NewTransactor(db), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithTx_Join_ReusesOuterTx(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectCommit()

	tr := NewTransactor(db)
	err := WithTx(context.Background(), tr, func(ctx context.Context) error {
		outer, _ := GetTx(ctx)

		return WithTx(ctx, tr, func(inner context.Context) error {
			if got, _ := GetTx(inner); got != outer {
				t.Error("inner must reuse outer tx")
			}

			return nil
		}, Join())
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTx_Join_InnerCommitNoop(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectCommit()

	tr := NewTransactor(db)
	outer, err := tr.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	inner, err := BeginTx(outer, tr, Join())
	if err != nil {
		t.Fatal(err)
	}

	if err = tr.Commit(inner); err != nil { // no-op
		t.Fatal(err)
	}

	if err = tr.Rollback(inner); err != nil { // no-op
		t.Fatal(err)
	}

	if err = tr.Commit(outer); err != nil {
		t.Fatal(err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTx_NoJoin_OpensSecondTx(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectBegin()
	mock.ExpectCommit()
	mock.ExpectCommit()

	tr := NewTransactor(db)
	outer, err := tr.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	inner, err := tr.Begin(outer)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := GetTx(outer)
	if b, _ := GetTx(inner); a == b {
		t.Fatal("without Join a second tx must be opened")
	}

	if err = tr.Commit(inner); err != nil {
		t.Fatal(err)
	}

	if err = tr.Commit(outer); err != nil {
		t.Fatal(err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTx_JoinedCtx_OwnTxStillCommits(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin() // outer
	mock.ExpectBegin() // own tx inside the joined level
	mock.ExpectCommit()
	mock.ExpectCommit()

	tr := NewTransactor(db)
	outer, err := tr.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	joined, err := BeginTx(outer, tr, Join())
	if err != nil {
		t.Fatal(err)
	}

	own, err := tr.Begin(joined) // no Join: a real new tx, but the joined marker is still in ctx
	if err != nil {
		t.Fatal(err)
	}

	if err = tr.Commit(own); err != nil {
		t.Fatal(err)
	}

	if err = tr.Commit(outer); err != nil {
		t.Fatal(err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// optsProvider records TxOptions passed to BeginTxx.
type optsProvider struct {
	db   *sqlx.DB
	opts *sql.TxOptions
}

func (p *optsProvider) BeginTxx(ctx context.Context, opts *sql.TxOptions) (*sqlx.Tx, error) {
	p.opts = opts

	return p.db.BeginTxx(ctx, opts)
}

func TestBeginTx_ReadOnlyIsolation(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectBegin()
	mock.ExpectRollback()
	mock.ExpectRollback()

	p := &optsProvider{db: db}
	tr := NewTransactor(p)

	ctx, err := BeginTx(context.Background(), tr, ReadOnly(), Isolation(sql.LevelSerializable))
	if err != nil {
		t.Fatal(err)
	}

	if p.opts == nil || !p.opts.ReadOnly || p.opts.Isolation != sql.LevelSerializable {
		t.Fatalf("opts = %+v", p.opts)
	}

	_ = tr.Rollback(ctx)

	// defaults stay as before: ReadCommitted, read-write
	ctx, err = tr.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if p.opts.ReadOnly || p.opts.Isolation != sql.LevelReadCommitted {
		t.Fatalf("default opts = %+v", p.opts)
	}

	_ = tr.Rollback(ctx)
}

// foreignTransactor is a Transactor without TxBeginner (like service mocks).
type foreignTransactor struct{ began bool }

func (f *foreignTransactor) Key() string                                  { return "x" }
func (f *foreignTransactor) IsTx(context.Context) bool                    { return false }
func (f *foreignTransactor) Commit(context.Context) error                 { return nil }
func (f *foreignTransactor) Rollback(context.Context) error               { return nil }
func (f *foreignTransactor) Rewrite(o, _ context.Context) context.Context { return o }
func (f *foreignTransactor) Begin(ctx context.Context) (context.Context, error) {
	f.began = true

	return ctx, nil
}

func TestBeginTx_ForeignTransactorWithOptions_Error(t *testing.T) {
	f := &foreignTransactor{}

	if _, err := BeginTx(context.Background(), f, ReadOnly()); !errors.Is(err, ErrTxOptionNotSupported) {
		t.Fatalf("got %v", err)
	}

	if f.began {
		t.Fatal("Begin must not be called")
	}

	if _, err := BeginTx(context.Background(), f); err != nil || !f.began {
		t.Fatalf("without options Begin is used: %v", err)
	}
}

func TestRunTransactionContext_Panic_RollsBack(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("got %v", p)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}()

	_ = RunTransactionContext(context.Background(), db, func(*sqlx.Tx) error { panic("boom") })
	t.Fatal("unreachable")
}
