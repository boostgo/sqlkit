package sqlkit

import (
	"context"
)

// Transactor is common representation of transactions for any type of database.
//
// Reason to use this: hide from usecase/service layer of using "sql" or "mongo" database
type Transactor interface {
	Key() string
	IsTx(ctx context.Context) bool
	Begin(ctx context.Context) (context.Context, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	Rewrite(original, toCopy context.Context) context.Context
}

// Transaction interface using by Transactor.
//
// Deprecated: the library does not implement it; use Transactor.
type Transaction interface {
	Context() context.Context
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// RewriteTx take transaction key from original context and copy key to toCopy context.
//
// For the default key "sqlkit:tx" the transaction is copied via GetTx/SetTx.
func RewriteTx(key string, original context.Context, toCopy context.Context) context.Context {
	if key == transactionKey {
		if _, ok := GetTx(toCopy); ok {
			return toCopy
		}

		tx, ok := GetTx(original)
		if !ok {
			return toCopy
		}

		return SetTx(toCopy, tx)
	}

	if toCopy.Value(key) != nil {
		return toCopy
	}

	tx := original.Value(key)
	if tx == nil {
		return toCopy
	}

	//nolint:staticcheck // SA1029: public API with a string key
	return context.WithValue(toCopy, key, tx)
}
