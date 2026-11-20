package sqlkit

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// TransactorConnectionProvider sql multipleTransaction interface
type TransactorConnectionProvider interface {
	BeginTxx(ctx context.Context, opts *sql.TxOptions) (*sqlx.Tx, error)
}

type sqlTransactor struct {
	provider TransactorConnectionProvider
}

// NewTransactor creates SQL multipleTransactor
func NewTransactor(provider TransactorConnectionProvider) Transactor {
	return &sqlTransactor{
		provider: provider,
	}
}

func (st *sqlTransactor) Key() string {
	return transactionKey
}

// joinedTxKey marks a context whose transaction was joined (not opened) by BeginTx with Join().
type joinedTxKey struct{}

// TxBeginner is implemented by the transactor of this library (NewTransactor).
// Other Transactor implementations (e.g. mocks) are not required to implement it.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts ...TxOption) (context.Context, error)
}

func (st *sqlTransactor) Begin(ctx context.Context) (context.Context, error) {
	return st.BeginTx(ctx)
}

// BeginTx begins a transaction with options. Begin(ctx) is BeginTx(ctx) without options.
func (st *sqlTransactor) BeginTx(ctx context.Context, opts ...TxOption) (context.Context, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	cfg := newTxConfig(opts)

	if cfg.join {
		if tx, ok := GetTx(ctx); ok {
			return context.WithValue(ctx, joinedTxKey{}, tx), nil
		}
	}

	tx, err := st.provider.BeginTxx(ctx, &sql.TxOptions{
		Isolation: cfg.isolation,
		ReadOnly:  cfg.readOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTransactorBegin, err)
	}

	return SetTx(ctx, tx), nil
}

// isJoined reports whether the transaction in ctx was joined and not opened at this level.
func isJoined(ctx context.Context) bool {
	joined, _ := ctx.Value(joinedTxKey{}).(*sqlx.Tx)
	if joined == nil {
		return false
	}

	tx, ok := GetTx(ctx)

	return ok && tx == joined
}

func (st *sqlTransactor) Commit(ctx context.Context) error {
	tx, ok := GetTx(ctx)
	if !ok || isJoined(ctx) {
		return nil
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrTransactorCommit, err)
	}

	return nil
}

func (st *sqlTransactor) Rollback(ctx context.Context) error {
	tx, ok := GetTx(ctx)
	if !ok || isJoined(ctx) {
		return nil
	}

	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("%w: %w", ErrTransactorRollback, err)
	}

	return nil
}

func (st *sqlTransactor) IsTx(ctx context.Context) bool {
	if ctx == nil {
		return false
	}

	_, ok := GetTx(ctx)
	return ok
}

func (st *sqlTransactor) Rewrite(original, toCopy context.Context) context.Context {
	return RewriteTx(st.Key(), original, toCopy)
}
