package sqlkit_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing/fstest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	"github.com/boostgo/sqlkit"
)

func ExampleWithTx() {
	raw, mock, _ := sqlmock.New()
	defer raw.Close()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	conn := sqlx.NewDb(raw, "postgres")
	db := sqlkit.NewDB(conn)
	tr := sqlkit.NewTransactor(conn)

	err := sqlkit.WithTx(context.Background(), tr, func(ctx context.Context) error {
		_, err := db.ExecContext(ctx, "UPDATE t SET a = 1")

		return err
	})
	fmt.Println(err)
	// Output: <nil>
}

func ExampleOffsetPager() {
	p := sqlkit.OffsetPager(sqlkit.PageRequest{Page: 3, Size: 20}, sqlkit.Desc("created_at"), sqlkit.Desc("id"))

	fmt.Println(p.OrderBy())
	fmt.Println(p.LimitOffset())
	// Output:
	// ORDER BY created_at DESC, id DESC
	// LIMIT 21 OFFSET 40
}

func ExampleCursorPager() {
	type row struct {
		ID        int64
		CreatedAt time.Time
	}

	ts := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	opts := []sqlkit.PagerOption{sqlkit.Desc("created_at"), sqlkit.Desc("id")}

	// first page returned one extra row, so there is a next page
	first := sqlkit.CursorPager(sqlkit.PageRequest{Size: 1}, opts...)
	res := sqlkit.PageOf([]row{{2, ts}, {1, ts}}, first, func(r row) []any { return []any{r.CreatedAt, r.ID} })

	// second page request
	next := sqlkit.CursorPager(sqlkit.PageRequest{Size: 1, Cursor: *res.NextCursor}, opts...)
	where, args := next.Where(sqlkit.Dollar(1))

	fmt.Println(where)
	fmt.Println(args[1])
	fmt.Println(next.OrderBy(), next.LimitOffset())
	// Output:
	// (created_at, id) < ($1, $2)
	// 2
	// ORDER BY created_at DESC, id DESC LIMIT 2
}

func ExamplePageOf() {
	p := sqlkit.OffsetPager(sqlkit.PageRequest{Size: 2}, sqlkit.Asc("id"))

	rows := []int{1, 2, 3} // size+1 rows were fetched
	res := sqlkit.PageOf(rows, p, nil)

	fmt.Println(res.Items, res.HasMore, res.NextCursor != nil)
	// Output: [1 2] true true
}

func ExampleNewCluster() {
	rawPrimary, primaryMock, _ := sqlmock.New()
	defer rawPrimary.Close()

	rawA, mockA, _ := sqlmock.New()
	defer rawA.Close()

	rawB, mockB, _ := sqlmock.New()
	defer rawB.Close()

	// lag check on start: the LSN of primary once, then both replicas
	primaryMock.ExpectQuery("pg_current_wal_lsn").
		WillReturnRows(sqlmock.NewRows([]string{"lsn"}).AddRow("0/1"))

	for _, m := range []sqlmock.Sqlmock{mockA, mockB} {
		m.ExpectQuery("pg_is_in_recovery").
			WillReturnRows(sqlmock.NewRows([]string{"rec", "lsn", "caught", "lag"}).AddRow(true, "0/1", true, 0.0))
	}

	cluster, err := sqlkit.NewCluster(sqlx.NewDb(rawPrimary, "postgres"),
		sqlkit.WithNamedReplica("a", sqlx.NewDb(rawA, "postgres")),
		sqlkit.WithNamedReplica("b", sqlx.NewDb(rawB, "postgres")),
		sqlkit.WithReplicaBalancer(sqlkit.RoundRobin()),
		sqlkit.WithMaxLag(30*time.Second),
	)
	if err != nil {
		fmt.Println(err)

		return
	}
	defer cluster.Close() // does not close primary and replicas: the owner does

	// a real service does not wait: until the first check everything goes to primary
	for st := cluster.States(); !st[0].Usable || !st[1].Usable; st = cluster.States() {
		time.Sleep(time.Millisecond)
	}

	// a heavy list may be read from a replica, and may NOT fall back to primary: RequireReplica
	mockA.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	ctx := sqlkit.RequireReplica(context.Background())

	var ids []int
	err = cluster.SelectContext(ctx, &ids, "SELECT id FROM items")
	fmt.Println(ids, err)
	// Output: [1] <nil>
}

// ExampleConnectCluster shows the config of a service: a replica takes the zero fields from primary.
func ExampleConnectCluster() {
	cfg := sqlkit.ClusterConfig{
		Primary: sqlkit.Config{
			Host: "db-primary", Port: 5432, Username: "app", Password: "secret", Database: "app",
			MaxOpenConnections: 20,
		},
		ReplicasEnabled: true,
		Replicas: []sqlkit.ReplicaConfig{
			{Name: "ro1", Config: sqlkit.Config{Host: "db-ro1"}},
			{Name: "ro2", Config: sqlkit.Config{Host: "db-ro2", MaxOpenConnections: 50}},
		},
		MaxLagSeconds:           30,
		LagCheckIntervalSeconds: 5,
	}

	cluster, err := sqlkit.ConnectCluster(cfg,
		sqlkit.WithReplicaBalancer(sqlkit.LeastLag()),
		sqlkit.WithStateHook(func(s sqlkit.ReplicaState) { log.Printf("replica %s: %s", s.Name, s.Reason) }),
	)
	if err != nil {
		log.Print(err) // the primary is unavailable; an unavailable replica does not fail the start

		return
	}
	defer cluster.Close() // stops the lag check, closes the replicas and the primary

	var ids []int

	err = cluster.SelectContext(sqlkit.RequireReplica(context.Background()), &ids, "SELECT id FROM items")
	if errors.Is(err, sqlkit.ErrReplicaUnavailable) {
		log.Print("no usable replica")
	}
}

// Example_sqlite opens a SQLite database from a config, applies embedded migrations, writes in a
// transaction and reads the rows page by page with a cursor.
func Example_sqlite() {
	dir, _ := os.MkdirTemp("", "sqlkit-example")
	defer os.RemoveAll(dir)

	conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{
		Driver: sqlkit.SQLiteDriver,
		DSN:    filepath.Join(dir, "app.db"), // WAL, foreign_keys=on, busy_timeout=5000 by default
	})
	if err != nil {
		fmt.Println(err)

		return
	}
	defer conn.Close()

	ctx := context.Background()

	// usually a //go:embed migrations/*.sql embed.FS
	migrations := fstest.MapFS{
		"migrations/0001_notes.up.sql": {Data: []byte(
			"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL UNIQUE)")},
	}
	if err = sqlkit.NewMigrator(ctx, conn, "app").WithFS(migrations, "migrations").MigrateContext(ctx); err != nil {
		fmt.Println(err)

		return
	}

	db := sqlkit.NewDBWithOptions(conn, sqlkit.WithPaging(sqlkit.PagingCursor))
	tr := sqlkit.NewTransactor(conn)

	err = sqlkit.WithTx(ctx, tr, func(ctx context.Context) error {
		for _, title := range []string{"alpha", "beta", "gamma"} {
			if _, err := db.ExecContext(ctx, "INSERT INTO notes (title) VALUES (?)", title); err != nil {
				return err
			}
		}

		return nil
	})
	fmt.Println("insert:", err)

	_, err = db.ExecContext(ctx, "INSERT INTO notes (title) VALUES (?)", "alpha")
	fmt.Println("duplicate:", sqlkit.IsDuplicate(err))

	type note struct {
		ID    int64  `db:"id"`
		Title string `db:"title"`
	}

	cursor := ""
	for {
		p := sqlkit.NewPager(db, sqlkit.PageRequest{Size: 2, Cursor: cursor}, sqlkit.Asc("id"))

		query := "SELECT id, title FROM notes"
		where, args := p.Where(sqlkit.Placeholder(sqlkit.DriverName(db), 1))
		if where != "" {
			query += " WHERE " + where
		}

		var rows []note
		if err = db.SelectContext(ctx, &rows, query+" "+p.OrderBy()+" "+p.LimitOffset(), args...); err != nil {
			fmt.Println(err)

			return
		}

		page := sqlkit.PageOf(rows, p, func(n note) []any { return []any{n.ID} })
		fmt.Println(page.Items, page.HasMore)

		if !page.HasMore {
			break
		}

		cursor = *page.NextCursor
	}
	// Output:
	// insert: <nil>
	// duplicate: true
	// [{1 alpha} {2 beta}] true
	// [{3 gamma}] false
}

func ExampleClassify() {
	conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{Driver: sqlkit.SQLiteDriver, DSN: ":memory:"})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	ctx := context.Background()
	db := sqlkit.NewDB(conn)

	_, _ = db.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE)")
	_, _ = db.ExecContext(ctx, "INSERT INTO users (email) VALUES (?)", "a@example.com")
	_, err = db.ExecContext(ctx, "INSERT INTO users (email) VALUES (?)", "a@example.com")

	err = sqlkit.Classify(err)
	fields, _ := sqlkit.DuplicateFields(err)

	fmt.Println(errors.Is(err, sqlkit.ErrDuplicate), fields)
	// Output: true [email]
}

func ExampleRebind() {
	query := "SELECT * FROM users WHERE tenant_id = ? AND email = ?"

	pg := sqlkit.NewDB(sqlx.NewDb(nil, sqlkit.PgxDriver))
	lite := sqlkit.NewDB(sqlx.NewDb(nil, sqlkit.SQLiteDriver))

	fmt.Println(sqlkit.Rebind(pg, query))
	fmt.Println(sqlkit.Rebind(lite, query))
	// Output:
	// SELECT * FROM users WHERE tenant_id = $1 AND email = $2
	// SELECT * FROM users WHERE tenant_id = ? AND email = ?
}

func ExampleSQLiteDSN() {
	fmt.Println(sqlkit.SQLiteDSN("app.db", sqlkit.SQLiteOptions{BusyTimeoutMs: 1000}))
	// Output: app.db?_pragma=busy_timeout%281000%29&_pragma=journal_mode%28WAL%29&_pragma=synchronous%28NORMAL%29&_pragma=foreign_keys%281%29&_time_format=sqlite&_txlock=immediate
}
