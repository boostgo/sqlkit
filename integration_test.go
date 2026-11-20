package sqlkit

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// Integration tests run only when the DSN of a live server is given:
//
//	SQLKIT_TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/sqlkit?sslmode=disable"
//	SQLKIT_TEST_CLICKHOUSE_DSN="http://default:@localhost:8123/default"
const (
	envPostgresDSN   = "SQLKIT_TEST_POSTGRES_DSN"
	envClickhouseDSN = "SQLKIT_TEST_CLICKHOUSE_DSN"
)

//go:embed testdata/postgres/*.sql
var postgresMigrations embed.FS

func requireEnv(t *testing.T, name string) string {
	t.Helper()

	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set", name)
	}

	return v
}

// openPostgres connects with driver to a clean, migrated database; the tables are dropped on cleanup.
func openPostgres(t *testing.T, driver string) *sqlx.DB {
	t.Helper()

	dsn := requireEnv(t, envPostgresDSN)
	ctx := context.Background()

	conn, err := ConnectFromConfig(Config{Driver: driver, DSN: dsn, TimeoutMs: 5000})
	if err != nil {
		t.Fatal(err)
	}

	drop := func() {
		_, _ = conn.ExecContext(ctx, "DROP TABLE IF EXISTS sqlkit_orders, sqlkit_users, sqlkit_migrations")
	}

	drop()
	t.Cleanup(func() {
		drop()
		_ = conn.Close()
	})

	err = NewMigrator(ctx, conn, "sqlkit").
		WithFS(postgresMigrations, "testdata/postgres").
		WithMigrationsTable("sqlkit_migrations").
		MigrateContext(ctx)
	if err != nil {
		t.Fatal(err)
	}

	return conn
}

func TestPostgresIntegration(t *testing.T) {
	for _, driver := range []string{PqDriver, PgxDriver} {
		t.Run(driver, func(t *testing.T) {
			conn := openPostgres(t, driver)
			db := NewDBWithOptions(conn, WithPaging(PagingCursor))
			tr := NewTransactor(conn)
			ctx := context.Background()
			base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

			// transactions: the second insert fails, so nothing is committed
			err := WithTx(ctx, tr, func(ctx context.Context) error {
				for _, email := range []string{"a@example.com", "a@example.com"} {
					if _, err := db.ExecContext(ctx, Rebind(db,
						"INSERT INTO sqlkit_users (email, created_at) VALUES (?, ?)"), email, base); err != nil {
						return err
					}
				}

				return nil
			})
			if !IsDuplicate(err) {
				t.Fatalf("unique: %v", err)
			}

			if fields, ok := DuplicateFields(err); !ok || !slices.Equal(fields, []string{"email"}) {
				t.Fatalf("DuplicateFields = %v, %v", fields, ok)
			}

			for i := range 7 {
				if _, err = db.ExecContext(ctx, Rebind(db,
					"INSERT INTO sqlkit_users (email, created_at) VALUES (?, ?)"),
					fmt.Sprintf("u%d@example.com", i), base.Add(time.Duration(i/2)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}

			_, err = db.ExecContext(ctx, "INSERT INTO sqlkit_orders (user_id, amount) VALUES ($1, $2)", -1, 1)
			if !IsForeignKey(err) {
				t.Fatalf("foreign key: %v", err)
			}

			_, err = db.ExecContext(ctx, "INSERT INTO sqlkit_users (email, age, created_at) VALUES ($1, $2, $3)",
				"x@example.com", -5, base)
			if !IsCheckViolation(err) {
				t.Fatalf("check: %v", err)
			}

			// cursor pagination over (created_at, id)
			var (
				got    []string
				cursor string
			)

			for {
				p := NewPager(db, PageRequest{Size: 3, Cursor: cursor}, Desc("created_at"), Desc("id"))
				q := "SELECT id, email, age, created_at FROM sqlkit_users"

				where, args := p.Where(Placeholder(DriverName(db), 1))
				if where != "" {
					q += " WHERE " + where
				}

				var rows []testUser
				if err = db.SelectContext(ctx, &rows, q+" "+p.OrderBy()+" "+p.LimitOffset(), args...); err != nil {
					t.Fatal(err)
				}

				page := PageOf(rows, p, func(u testUser) []any { return []any{u.CreatedAt, u.ID} })
				for _, u := range page.Items {
					got = append(got, u.Email)
				}

				if !page.HasMore {
					break
				}

				cursor = *page.NextCursor
			}

			want := []string{"u6@example.com", "u5@example.com", "u4@example.com", "u3@example.com",
				"u2@example.com", "u1@example.com", "u0@example.com"}
			if !slices.Equal(got, want) {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestClickhouseIntegration(t *testing.T) {
	dsn := requireEnv(t, envClickhouseDSN)

	conn, err := Connect(ChDriver, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	db := NewDB(conn)

	var n int
	if err = db.GetContext(context.Background(), &n, Rebind(db, "SELECT toInt32(?) + 1"), 41); err != nil {
		t.Fatal(err)
	}

	if n != 42 {
		t.Fatalf("n = %d", n)
	}

	err = NewMigrator(context.Background(), conn, "default").MigrateContext(context.Background())
	if !errors.Is(err, ErrMigrateUnsupportedDriver) {
		t.Fatalf("got %v", err)
	}
}
