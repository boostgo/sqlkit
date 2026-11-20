package sqlkit

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestTransactor_CommitCanceledCtx(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback() // the driver rolls back by itself on ctx cancel

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := NewTransactor(db)
	txCtx, err := tr.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	waitMet(t, mock)

	if err = tr.Commit(txCtx); !errors.Is(err, ErrTransactorCommit) {
		t.Fatalf("want ErrTransactorCommit, got %v", err)
	}
}

func TestTransactor_RollbackCanceledCtx(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := NewTransactor(db)
	txCtx, err := tr.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	waitMet(t, mock)

	if err = tr.Rollback(txCtx); !errors.Is(err, ErrTransactorRollback) {
		t.Fatalf("want ErrTransactorRollback, got %v", err)
	}
}

func TestAtomic_ErrorRollsBack(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	want := errors.New("boom")
	err := Atomic(context.Background(), db, func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAtomic_PanicRollsBackAndRepanics(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("want re-panic boom, got %v", p)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}()

	_ = Atomic(context.Background(), db, func(context.Context) error { panic("boom") })
	t.Fatal("unreachable")
}

func TestAtomic_CommitErrorReturned(t *testing.T) {
	db, mock := newMockDB(t)
	want := errors.New("commit failed")
	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(want)

	err := Atomic(context.Background(), db, func(ctx context.Context) error {
		if _, ok := GetTx(ctx); !ok {
			t.Error("tx must be in ctx")
		}

		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
}

func TestRunTransaction_PanicRollsBack(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("want re-panic boom, got %v", p)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}()

	_ = RunTransaction(db, func(*sqlx.Tx) error { panic("boom") })
	t.Fatal("unreachable")
}

func TestGetTx_StringKey(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	//nolint:staticcheck // SA1029: a context built by hand with the string key
	ctx := context.WithValue(context.Background(), "sqlkit:tx", tx)

	got, ok := GetTx(ctx)
	if !ok || got != tx {
		t.Fatal("tx under string key must be visible")
	}

	if !NewTransactor(db).IsTx(ctx) {
		t.Fatal("IsTx must see string key")
	}
}

func TestRewriteTx_TypedKey(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	tr := NewTransactor(db)
	orig, err := tr.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tr.Rollback(orig) }()

	if tr.Key() != "sqlkit:tx" {
		t.Fatalf("Key() = %q", tr.Key())
	}

	got := tr.Rewrite(orig, context.Background())
	if !tr.IsTx(got) {
		t.Fatal("tx must be copied to the new ctx")
	}

	origTx, _ := GetTx(orig)
	if gotTx, _ := GetTx(got); gotTx != origTx {
		t.Fatal("copied tx differs")
	}

	// toCopy that already has a tx is returned as is
	if again := tr.Rewrite(context.Background(), got); again != got {
		t.Fatal("ctx with tx must be returned as is")
	}
}
