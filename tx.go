package sqlkit

import (
	"context"
	"errors"
)

// BeginTx begins a transaction on tr with options.
//
// If tr implements TxBeginner (the transactor of this library) it is used with opts. Otherwise,
// without opts, tr.Begin is called; with opts a foreign Transactor (e.g. a mock) can not honor them
// and ErrTxOptionNotSupported is returned.
func BeginTx(ctx context.Context, tr Transactor, opts ...TxOption) (context.Context, error) {
	if b, ok := tr.(TxBeginner); ok {
		return b.BeginTx(ctx, opts...)
	}

	if len(opts) > 0 {
		return nil, ErrTxOptionNotSupported
	}

	return tr.Begin(ctx)
}

// WithTx runs fn in a transaction: it commits if fn returns nil, rolls back if fn returns an error
// or panics (the panic is re-raised with the original value).
//
// WARNING: without Join(), a nested WithTx/Begin inside an open transaction opens a SECOND
// transaction on another connection: it does not see the uncommitted data of the first one and can
// deadlock on its rows. Pass Join() to reuse the transaction already in ctx; then the nested call
// does not commit or roll back, the outer level does.
func WithTx(ctx context.Context, tr Transactor, fn func(ctx context.Context) error, opts ...TxOption) (err error) {
	if newTxConfig(opts).join && tr.IsTx(ctx) {
		return fn(ctx)
	}

	txCtx, err := BeginTx(ctx, tr, opts...)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tr.Rollback(txCtx)
			panic(p)
		}
	}()

	if fnErr := fn(txCtx); fnErr != nil {
		if rbErr := tr.Rollback(txCtx); rbErr != nil {
			return errors.Join(fnErr, rbErr)
		}

		return fnErr
	}

	return tr.Commit(txCtx)
}
