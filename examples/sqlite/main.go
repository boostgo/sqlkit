// Command sqlite shows sqlkit with a local SQLite database: config, embedded migrations, a repository
// that uses the transactor, error classification and cursor pagination.
//
//	go run ./examples/sqlite
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/boostgo/sqlkit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrInsufficientFunds is a domain error of Transfer.
var ErrInsufficientFunds = errors.New("insufficient funds")

// Account is a row of the accounts table.
type Account struct {
	ID        int64     `db:"id"`
	Owner     string    `db:"owner"`
	Balance   int64     `db:"balance"`
	CreatedAt time.Time `db:"created_at"`
}

// Transfer is a row of the transfers table.
type Transfer struct {
	ID        int64     `db:"id"`
	FromID    int64     `db:"from_id"`
	ToID      int64     `db:"to_id"`
	Amount    int64     `db:"amount"`
	CreatedAt time.Time `db:"created_at"`
}

// Repository works with any sqlkit.DB; it never sees a transaction explicitly: the DB methods take
// it from ctx.
type Repository struct {
	db sqlkit.DB
	tr sqlkit.Transactor
}

// CreateAccount inserts an account. A taken owner name is reported as sqlkit.ErrDuplicate.
func (r *Repository) CreateAccount(ctx context.Context, owner string, balance int64) (int64, error) {
	var id int64

	err := r.db.GetContext(ctx, &id, sqlkit.Rebind(r.db,
		"INSERT INTO accounts (owner, balance, created_at) VALUES (?, ?, ?) RETURNING id"),
		owner, balance, time.Now().UTC())

	return id, sqlkit.Classify(err)
}

// Transfer moves amount between two accounts atomically.
func (r *Repository) Transfer(ctx context.Context, fromID, toID, amount int64) error {
	return sqlkit.WithTx(ctx, r.tr, func(ctx context.Context) error {
		var balance int64
		if err := r.db.GetContext(ctx, &balance,
			sqlkit.Rebind(r.db, "SELECT balance FROM accounts WHERE id = ?"), fromID); err != nil {
			return err
		}

		if balance < amount {
			return ErrInsufficientFunds // rolls the transaction back
		}

		for _, step := range []struct {
			id    int64
			delta int64
		}{{fromID, -amount}, {toID, amount}} {
			if _, err := r.db.ExecContext(ctx, sqlkit.Rebind(r.db,
				"UPDATE accounts SET balance = balance + ? WHERE id = ?"), step.delta, step.id); err != nil {
				return err
			}
		}

		_, err := r.db.ExecContext(ctx, sqlkit.Rebind(r.db,
			"INSERT INTO transfers (from_id, to_id, amount, created_at) VALUES (?, ?, ?, ?)"),
			fromID, toID, amount, time.Now().UTC())

		return sqlkit.Classify(err)
	})
}

// ListTransfers returns one page of transfers, newest first.
func (r *Repository) ListTransfers(ctx context.Context, req sqlkit.PageRequest) (sqlkit.PageResult[Transfer], error) {
	p := sqlkit.CursorPager(req, sqlkit.Desc("created_at"), sqlkit.Desc("id"))
	if err := p.Err(); err != nil {
		return sqlkit.PageResult[Transfer]{}, err // ErrInvalidCursor: a bad request
	}

	query := "SELECT id, from_id, to_id, amount, created_at FROM transfers"

	where, args := p.Where(sqlkit.Placeholder(sqlkit.DriverName(r.db), 1))
	if where != "" {
		query += " WHERE " + where
	}

	var rows []Transfer
	if err := r.db.SelectContext(ctx, &rows, query+" "+p.OrderBy()+" "+p.LimitOffset(), args...); err != nil {
		return sqlkit.PageResult[Transfer]{}, err
	}

	return sqlkit.PageOf(rows, p, func(t Transfer) []any { return []any{t.CreatedAt, t.ID} }), nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dir, err := os.MkdirTemp("", "sqlkit-sqlite-example")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	cfg := sqlkit.Config{
		Driver: sqlkit.SQLiteDriver,
		DSN:    filepath.Join(dir, "bank.db"),
		// zero values are fine; shown here for reference
		SQLite: sqlkit.SQLiteOptions{JournalMode: "WAL", BusyTimeoutMs: 5000, TxLock: "immediate"},
	}

	conn, err := sqlkit.ConnectFromConfig(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx := context.Background()

	if err = sqlkit.NewMigrator(ctx, conn, "bank").WithFS(migrations, "migrations").MigrateContext(ctx); err != nil {
		return err
	}

	repo := &Repository{db: sqlkit.NewDB(conn), tr: sqlkit.NewTransactor(conn)}

	alice, err := repo.CreateAccount(ctx, "alice", 100)
	if err != nil {
		return err
	}

	bob, err := repo.CreateAccount(ctx, "bob", 0)
	if err != nil {
		return err
	}

	if _, err = repo.CreateAccount(ctx, "alice", 1); errors.Is(err, sqlkit.ErrDuplicate) {
		fields, _ := sqlkit.DuplicateFields(err)
		fmt.Println("duplicate account, fields:", fields)
	}

	for _, amount := range []int64{10, 20, 30, 500} {
		if err = repo.Transfer(ctx, alice, bob, amount); err != nil {
			fmt.Printf("transfer %d: %v\n", amount, err)
		}
	}

	if err = repo.Transfer(ctx, alice, 404, 1); sqlkit.IsForeignKey(err) {
		fmt.Println("transfer to a missing account: foreign key violation")
	}

	req := sqlkit.PageRequest{Size: 2}
	for {
		page, err := repo.ListTransfers(ctx, req)
		if err != nil {
			return err
		}

		for _, t := range page.Items {
			fmt.Printf("transfer #%d: %d -> %d, amount %d\n", t.ID, t.FromID, t.ToID, t.Amount)
		}

		if !page.HasMore {
			break
		}

		req.Cursor = *page.NextCursor
	}

	var balances []Account
	if err = repo.db.SelectContext(ctx, &balances, "SELECT * FROM accounts ORDER BY id"); err != nil {
		return err
	}

	for _, a := range balances {
		fmt.Printf("%s: %d\n", a.Owner, a.Balance)
	}

	return nil
}
