package sqlkit

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// transactionKey is the string key of a transaction in context. It is returned by Transactor.Key and
// also read by GetTx, so contexts built with context.WithValue(ctx, tr.Key(), tx) work too.
const transactionKey = "sqlkit:tx"

// txCtxKey is the typed context key under which SetTx stores a transaction.
type txCtxKey struct{}

// SetTx returns a copy of ctx that carries tx. The clients of this package run their queries in tx
// when they get such a context.
func SetTx(ctx context.Context, tx *sqlx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// GetTx returns the transaction carried by ctx, if any.
//
// The typed key of SetTx is checked first, then the string key "sqlkit:tx" (Transactor.Key).
func GetTx(ctx context.Context) (*sqlx.Tx, bool) {
	if tx, ok := ctx.Value(txCtxKey{}).(*sqlx.Tx); ok && tx != nil {
		return tx, true
	}

	//nolint:staticcheck // SA1029: public string key, see Transactor.Key
	if tx, ok := ctx.Value(transactionKey).(*sqlx.Tx); ok && tx != nil {
		return tx, true
	}

	return nil, false
}

// RunTransaction run "actions" by created transaction object.
//
// The transaction is rolled back if transactionActions returns an error or panics
// (the panic is re-raised).
//
// Deprecated: use RunTransactionContext.
func RunTransaction(conn *sqlx.DB, transactionActions func(tx *sqlx.Tx) error) error {
	return RunTransactionContext(context.Background(), conn, transactionActions)
}

// RunTransactionContext runs transactionActions in a transaction begun with ctx.
//
// The transaction is rolled back if transactionActions returns an error or panics
// (the panic is re-raised); otherwise it is committed and the Commit error is returned.
func RunTransactionContext(ctx context.Context, conn *sqlx.DB, transactionActions func(tx *sqlx.Tx) error) (err error) {
	transaction, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = transaction.Rollback()
			panic(p)
		}
	}()

	if err = transactionActions(transaction); err != nil {
		_ = transaction.Rollback()

		return err
	}

	return transaction.Commit()
}

// Atomic runs fn in a transaction. If ctx already holds a transaction, fn joins it.
//
// The transaction is rolled back if fn returns an error or panics (the panic is re-raised);
// otherwise it is committed and the Commit error is returned.
//
// Deprecated: use WithTx with Join().
func Atomic(ctx context.Context, conn *sqlx.DB, fn func(ctx context.Context) error) error {
	return WithTx(ctx, NewTransactor(conn), fn, Join())
}
