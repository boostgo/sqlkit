// Command postgres shows sqlkit with PostgreSQL: config, embedded migrations, a transaction,
// error classification and offset pagination.
//
//	SQLKIT_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" \
//		go run ./examples/postgres
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/boostgo/sqlkit"
)

//go:embed migrations/*.sql
var migrations embed.FS

type account struct {
	ID      int64  `db:"id"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("SQLKIT_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("set SQLKIT_POSTGRES_DSN, e.g. postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	}

	// Host/Port/Username/Password/Database work as well; DSN is used as is when set.
	conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{Driver: sqlkit.PgxDriver, DSN: dsn, TimeoutMs: 5000})
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx := context.Background()

	if err = sqlkit.NewMigrator(ctx, conn, "bank").WithFS(migrations, "migrations").MigrateContext(ctx); err != nil {
		return err
	}

	db := sqlkit.NewDB(conn)
	tr := sqlkit.NewTransactor(conn)

	err = sqlkit.WithTx(ctx, tr, func(ctx context.Context) error {
		for i := range 5 {
			if _, err := db.ExecContext(ctx,
				"INSERT INTO accounts (owner, balance, created_at) VALUES ($1, $2, $3) ON CONFLICT (owner) DO NOTHING",
				fmt.Sprintf("owner-%d", i), 100, time.Now().UTC()); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, "INSERT INTO accounts (owner, balance, created_at) VALUES ($1, $2, $3)",
		"owner-0", 1, time.Now().UTC())
	if sqlkit.IsDuplicate(err) {
		fields, _ := sqlkit.DuplicateFields(err)
		fmt.Println("duplicate, fields:", fields)
	}

	p := sqlkit.OffsetPager(sqlkit.PageRequest{Page: 1, Size: 2}, sqlkit.Asc("id"))
	if err = p.Err(); err != nil {
		return err
	}

	var rows []account
	if err = db.SelectContext(ctx, &rows,
		"SELECT id, owner, balance FROM accounts "+p.OrderBy()+" "+p.LimitOffset()); err != nil {
		return err
	}

	page := sqlkit.PageOf(rows, p, nil)
	fmt.Printf("page 1: %+v, has more: %t\n", page.Items, page.HasMore)

	return nil
}
