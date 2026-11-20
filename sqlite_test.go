package sqlkit

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

//go:embed testdata/sqlite/*.sql
var sqliteMigrations embed.FS

type testUser struct {
	ID        int64     `db:"id"`
	Email     string    `db:"email"`
	Age       *int64    `db:"age"`
	CreatedAt time.Time `db:"created_at"`
}

// openSQLite opens a migrated SQLite database in a temp file; it is closed on cleanup.
func openSQLite(t *testing.T) *sqlx.DB {
	t.Helper()

	conn, err := ConnectFromConfig(Config{
		Driver: SQLiteDriver,
		DSN:    filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	if err = NewMigrator(context.Background(), conn, "test").
		WithFS(sqliteMigrations, "testdata/sqlite").
		MigrateContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	return conn
}

func insertUser(ctx context.Context, db DB, email string, createdAt time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		"INSERT INTO users (email, created_at) VALUES (?, ?)", email, createdAt)
	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func countUsers(t *testing.T, db DB) int {
	t.Helper()

	var n int
	if err := db.GetContext(context.Background(), &n, "SELECT count(*) FROM users"); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestSQLiteDSN(t *testing.T) {
	dsn := SQLiteDSN("app.db", SQLiteOptions{})
	for _, want := range []string{
		"app.db?",
		"busy_timeout%285000%29",
		"journal_mode%28WAL%29",
		"synchronous%28NORMAL%29",
		"foreign_keys%281%29",
		"_txlock=immediate",
		"_time_format=sqlite",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q does not contain %q", dsn, want)
		}
	}

	dsn = SQLiteDSN("file:app.db?cache=shared", SQLiteOptions{
		BusyTimeoutMs: -1, DisableForeignKeys: true, TxLock: "deferred", Pragmas: "cache_size(-2000); ;temp_store(MEMORY)",
	})
	for _, want := range []string{"file:app.db?cache=shared&", "foreign_keys%280%29", "_txlock=deferred",
		"cache_size%28-2000%29", "temp_store%28MEMORY%29"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q does not contain %q", dsn, want)
		}
	}

	if strings.Contains(dsn, "busy_timeout") {
		t.Errorf("busy_timeout must be disabled: %q", dsn)
	}
}

func TestSQLiteConfigValidate(t *testing.T) {
	cfg := Config{Driver: SQLiteDriver}
	if err := cfg.Validate(); !errors.Is(err, ErrConfigNoDSN) {
		t.Fatalf("got %v", err)
	}

	cfg.DSN = "app.db"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	if got := cfg.ConnectionString(); got != SQLiteDSN("app.db", SQLiteOptions{}) {
		t.Fatalf("ConnectionString = %q", got)
	}

	if got := cfg.MigrationConnectionString(); got != cfg.ConnectionString() {
		t.Fatalf("MigrationConnectionString = %q", got)
	}

	// a raw DSN of another driver is used as is and makes host/port optional
	pg := Config{Driver: PgxDriver, DSN: "postgres://u:p@localhost:5432/db"}
	if err := pg.Validate(); err != nil {
		t.Fatal(err)
	}

	if pg.ConnectionString() != pg.DSN {
		t.Fatalf("ConnectionString = %q", pg.ConnectionString())
	}
}

func TestSQLitePragmas(t *testing.T) {
	conn := openSQLite(t)
	ctx := context.Background()

	var (
		journal string
		fk      int
		busy    int
	)

	if err := conn.GetContext(ctx, &journal, "PRAGMA journal_mode"); err != nil {
		t.Fatal(err)
	}

	if err := conn.GetContext(ctx, &fk, "PRAGMA foreign_keys"); err != nil {
		t.Fatal(err)
	}

	if err := conn.GetContext(ctx, &busy, "PRAGMA busy_timeout"); err != nil {
		t.Fatal(err)
	}

	if journal != "wal" || fk != 1 || busy != DefaultSQLiteBusyTimeoutMs {
		t.Fatalf("journal_mode=%s foreign_keys=%d busy_timeout=%d", journal, fk, busy)
	}
}

func TestSQLiteMemoryUsesOneConnection(t *testing.T) {
	conn, err := ConnectFromConfig(Config{Driver: SQLiteDriver, DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if got := conn.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}

	ctx := context.Background()
	if _, err = conn.ExecContext(ctx, "CREATE TABLE t (v INTEGER)"); err != nil {
		t.Fatal(err)
	}

	db := NewDB(conn)
	if _, err = db.ExecContext(ctx, "INSERT INTO t (v) VALUES (?)", 42); err != nil {
		t.Fatal(err)
	}

	var v int
	if err = db.GetContext(ctx, &v, "SELECT v FROM t"); err != nil || v != 42 {
		t.Fatalf("v=%d err=%v", v, err)
	}
}

func TestSQLiteMigrator(t *testing.T) {
	conn := openSQLite(t)
	ctx := context.Background()

	// the second run has nothing to do and the pool stays open
	if err := NewMigrator(ctx, conn, "test").WithFS(sqliteMigrations, "testdata/sqlite").MigrateContext(ctx); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := conn.GetContext(ctx, &version, "SELECT version FROM schema_migrations"); err != nil {
		t.Fatal(err)
	}

	if version != 2 {
		t.Fatalf("version = %d", version)
	}

	var index string
	if err := conn.GetContext(ctx, &index,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'users_created_at_id'"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMigratorDirectoryAndTable(t *testing.T) {
	conn, err := ConnectFromConfig(Config{Driver: SQLiteDriver, DSN: filepath.Join(t.TempDir(), "m.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx := context.Background()

	err = NewMigrator(ctx, conn, "test").
		WithMigrationsDirectory("testdata/sqlite").
		WithMigrationsTable("app_migrations").
		MigrateContext(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var version int
	if err = conn.GetContext(ctx, &version, "SELECT version FROM app_migrations"); err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}

	err = NewMigrator(ctx, conn, "test").WithMigrationsDirectory(t.TempDir() + "/missing").MigrateContext(ctx)
	if !errors.Is(err, ErrMigrateReadMigrationsDir) {
		t.Fatalf("got %v", err)
	}
}

func TestSQLiteMigratorBrokenMigration(t *testing.T) {
	conn, err := ConnectFromConfig(Config{Driver: SQLiteDriver, DSN: filepath.Join(t.TempDir(), "b.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "0001_bad.up.sql"), "CREATE TABLE ok (id INTEGER); CREATE TABLEX broken;")

	err = NewMigrator(context.Background(), conn, "test").WithMigrationsDirectory(dir).
		MigrateContext(context.Background())
	if !errors.Is(err, ErrMigrateUp) {
		t.Fatalf("got %v", err)
	}
}

func TestSQLiteTransactor(t *testing.T) {
	conn := openSQLite(t)
	db := NewDB(conn)
	tr := NewTransactor(conn)
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("commit", func(t *testing.T) {
		err := WithTx(ctx, tr, func(ctx context.Context) error {
			_, err := insertUser(ctx, db, "commit@example.com", now)

			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rollback on error", func(t *testing.T) {
		boom := errors.New("boom")

		err := WithTx(ctx, tr, func(ctx context.Context) error {
			if _, err := insertUser(ctx, db, "rollback@example.com", now); err != nil {
				return err
			}

			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("join", func(t *testing.T) {
		err := WithTx(ctx, tr, func(ctx context.Context) error {
			if _, err := insertUser(ctx, db, "outer@example.com", now); err != nil {
				return err
			}

			// the inner level sees the uncommitted row of the outer one
			return WithTx(ctx, tr, func(ctx context.Context) error {
				var n int
				if err := db.GetContext(ctx, &n, "SELECT count(*) FROM users WHERE email = ?", "outer@example.com"); err != nil {
					return err
				}

				if n != 1 {
					return fmt.Errorf("inner tx sees %d rows", n)
				}

				_, err := insertUser(ctx, db, "inner@example.com", now)

				return err
			}, Join())
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("read only and isolation", func(t *testing.T) {
		err := WithTx(ctx, tr, func(ctx context.Context) error {
			var n int

			return db.GetContext(ctx, &n, "SELECT count(*) FROM users")
		}, ReadOnly(), Isolation(sql.LevelSerializable))
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("manual begin and rollback", func(t *testing.T) {
		txCtx, err := tr.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}

		if !tr.IsTx(txCtx) {
			t.Fatal("IsTx = false")
		}

		if _, err = insertUser(txCtx, db, "manual@example.com", now); err != nil {
			t.Fatal(err)
		}

		if err = tr.Rollback(txCtx); err != nil {
			t.Fatal(err)
		}
	})

	var emails []string
	if err := db.SelectContext(ctx, &emails, "SELECT email FROM users ORDER BY id"); err != nil {
		t.Fatal(err)
	}

	want := []string{"commit@example.com", "outer@example.com", "inner@example.com"}
	if !slices.Equal(emails, want) {
		t.Fatalf("emails = %v, want %v", emails, want)
	}
}

func TestSQLiteConcurrentWriteTransactions(t *testing.T) {
	conn := openSQLite(t)
	db := NewDB(conn)
	tr := NewTransactor(conn)

	const (
		workers = 8
		perJob  = 15
	)

	var wg sync.WaitGroup

	errs := make(chan error, workers*perJob)

	for w := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range perJob {
				// read, then write in the same transaction: with a deferred BEGIN this upgrade
				// fails with SQLITE_BUSY under contention; IMMEDIATE (the default) serializes it.
				errs <- WithTx(context.Background(), tr, func(ctx context.Context) error {
					var n int
					if err := db.GetContext(ctx, &n, "SELECT count(*) FROM users"); err != nil {
						return err
					}

					_, err := insertUser(ctx, db, fmt.Sprintf("w%d-%d@example.com", w, i), time.Now().UTC())

					return err
				})
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := countUsers(t, db); n != workers*perJob {
		t.Fatalf("users = %d", n)
	}
}

func TestSQLiteClassify(t *testing.T) {
	conn := openSQLite(t)
	db := NewDB(conn)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := insertUser(ctx, db, "a@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = insertUser(ctx, db, "a@example.com", now)
	if !IsDuplicate(err) || !errors.Is(Classify(err), ErrDuplicate) || !errors.Is(err, ErrExecContext) {
		t.Fatalf("unique: %v", err)
	}

	if fields, ok := DuplicateFields(err); !ok || !slices.Equal(fields, []string{"email"}) {
		t.Fatalf("DuplicateFields = %v, %v", fields, ok)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)", id, "b@example.com", now)
	if !IsDuplicate(err) {
		t.Fatalf("primary key: %v", err)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO orders (user_id, amount) VALUES (?, ?)", 999, 1)
	if !IsForeignKey(err) || IsDuplicate(err) {
		t.Fatalf("foreign key: %v", err)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO users (email, created_at) VALUES (?, ?)", nil, now)
	if !IsNotNull(err) {
		t.Fatalf("not null: %v", err)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO users (email, age, created_at) VALUES (?, ?, ?)", "c@example.com", -1, now)
	if !IsCheckViolation(err) {
		t.Fatalf("check: %v", err)
	}

	var u testUser

	err = db.GetContext(ctx, &u, "SELECT * FROM users WHERE id = ?", 12345)
	if !IsNoRows(err) {
		t.Fatalf("no rows: %v", err)
	}

	if _, ok := DuplicateFields(Classify(errors.New("other"))); ok {
		t.Fatal("DuplicateFields of a foreign error")
	}
}

func TestSQLiteNamedQueries(t *testing.T) {
	conn := openSQLite(t)
	db := NewDB(conn)
	ctx := context.Background()

	age := int64(30)
	u := testUser{Email: "named@example.com", Age: &age, CreatedAt: time.Now().UTC()}

	if _, err := db.NamedExecContext(ctx,
		"INSERT INTO users (email, age, created_at) VALUES (:email, :age, :created_at)", u); err != nil {
		t.Fatal(err)
	}

	row, err := db.NamedQueryRowxContext(ctx, "SELECT * FROM users WHERE email = :email", map[string]any{"email": u.Email})
	if err != nil {
		t.Fatal(err)
	}

	var got testUser
	if err = row.StructScan(&got); err != nil {
		t.Fatal(err)
	}

	if got.Email != u.Email || got.Age == nil || *got.Age != age || !got.CreatedAt.Equal(u.CreatedAt) {
		t.Fatalf("got %+v", got)
	}
}

func TestSQLiteRebindAndPlaceholder(t *testing.T) {
	conn := openSQLite(t)
	db := NewDB(conn)

	if got := DriverName(db); got != SQLiteDriver {
		t.Fatalf("DriverName = %q", got)
	}

	if got := Rebind(db, "a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("Rebind = %q", got)
	}

	pg := NewDB(sqlx.NewDb(conn.DB, PgxDriver))
	if got := Rebind(pg, "a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("Rebind = %q", got)
	}

	ph := Placeholder(SQLiteDriver, 5)
	if ph(1) != "?" || ph(2) != "?" {
		t.Fatal("sqlite placeholder")
	}

	ph = Placeholder(PqDriver, 5)
	if ph(1) != "$5" || ph(2) != "$6" {
		t.Fatal("postgres placeholder")
	}

	if Placeholder(ChDriver, 3)(nil) != "?" {
		t.Fatal("clickhouse placeholder")
	}

	if DriverName(nil) != "" {
		t.Fatal("DriverName(nil)")
	}
}

// seedUsers inserts n users; every 3 users share created_at, so the id breaks ties.
func seedUsers(t *testing.T, db DB, n int) {
	t.Helper()

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i := range n {
		// fractional seconds check that the text order of SQLite times is the time order
		created := base.Add(time.Duration(i/3) * 1500 * time.Millisecond)
		if _, err := insertUser(context.Background(), db, fmt.Sprintf("u%02d@example.com", i), created); err != nil {
			t.Fatal(err)
		}
	}
}

// listUsers is a repository method: one page of users, newest first.
func listUsers(ctx context.Context, db DB, req PageRequest, minID int64) (PageResult[testUser], error) {
	p := NewPager(db, req, Desc("created_at"), Desc("id"))
	if err := p.Err(); err != nil {
		return PageResult[testUser]{}, err
	}

	args := []any{minID}
	query := "SELECT id, email, age, created_at FROM users WHERE id >= ?"

	if where, wargs := p.Where(Placeholder(DriverName(db), len(args)+1)); where != "" {
		query += " AND " + where
		args = append(args, wargs...)
	}

	var rows []testUser
	if err := db.SelectContext(ctx, &rows, query+" "+p.OrderBy()+" "+p.LimitOffset(), args...); err != nil {
		return PageResult[testUser]{}, err
	}

	return PageOf(rows, p, func(u testUser) []any { return []any{u.CreatedAt, u.ID} }), nil
}

func collectPages(t *testing.T, db DB, size int) []string {
	t.Helper()

	var (
		emails []string
		cursor string
	)

	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("pagination does not end")
		}

		page, err := listUsers(context.Background(), db, PageRequest{Size: size, Cursor: cursor}, 0)
		if err != nil {
			t.Fatal(err)
		}

		for _, u := range page.Items {
			emails = append(emails, u.Email)
		}

		if !page.HasMore {
			return emails
		}

		cursor = *page.NextCursor
	}
}

func TestSQLitePagination(t *testing.T) {
	conn := openSQLite(t)

	const n = 25

	seedUsers(t, NewDB(conn), n)

	want := make([]string, n)
	for i := range n {
		want[i] = fmt.Sprintf("u%02d@example.com", n-1-i)
	}

	for _, mode := range []PagingMode{PagingOffset, PagingCursor} {
		for _, size := range []int{1, 4, 25, 100} {
			t.Run(fmt.Sprintf("mode=%d size=%d", mode, size), func(t *testing.T) {
				db := NewDBWithOptions(conn, WithPaging(mode))
				if got := collectPages(t, db, size); !slices.Equal(got, want) {
					t.Fatalf("got %v\nwant %v", got, want)
				}
			})
		}
	}
}

func TestSQLitePaginationInvalidCursor(t *testing.T) {
	conn := openSQLite(t)
	db := NewDBWithOptions(conn, WithPaging(PagingCursor))

	_, err := listUsers(context.Background(), db, PageRequest{Cursor: "%%%"}, 0)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("got %v", err)
	}
}
