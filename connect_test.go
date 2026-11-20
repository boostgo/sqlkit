package sqlkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestConnectFromConfig_AppliesPool(t *testing.T) {
	cfg := Config{
		Host: "h", Port: 5432, Username: "u", Password: "p", Database: "d",
		Driver:             "sqlmock",
		MaxOpenConnections: 7,
		MaxIdleConnections: 3,
	}

	mock := newDSNMock(t, cfg.ConnectionString())

	mock.ExpectPing()
	mock.ExpectClose()

	db, err := ConnectFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }()

	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", got)
	}

	// caller opts go after Config values
	cfg2 := cfg
	cfg2.Database = "d2"
	mock2 := newDSNMock(t, cfg2.ConnectionString())

	mock2.ExpectPing()
	mock2.ExpectClose()

	db2, err := ConnectFromConfig(cfg2, func(c *sqlx.DB) { c.SetMaxOpenConns(2) })
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db2.Close() }()

	if got := db2.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("caller opt must win, MaxOpenConnections = %d", got)
	}
}

func TestConnect_PingError_ClosesDB(t *testing.T) {
	const dsn = "s3-ping-error"

	mock := newDSNMock(t, dsn)

	pingErr := errors.New("down")
	mock.ExpectPing().WillReturnError(pingErr)
	mock.ExpectClose()

	db, err := Connect("sqlmock", dsn, time.Second)
	if db != nil {
		t.Fatal("db must be nil")
	}

	if !errors.Is(err, ErrPing) || !errors.Is(err, pingErr) {
		t.Fatalf("want ErrPing wrapping ping error, got %v", err)
	}

	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectShards_ShardError_ClosesOpened(t *testing.T) {
	mock1 := newDSNMock(t, "s3-shard-1")

	mock2 := newDSNMock(t, "s3-shard-2")

	mock1.ExpectPing()
	mock1.ExpectClose()
	mock2.ExpectPing().WillReturnError(errors.New("down"))
	mock2.ExpectClose()

	conns, err := ConnectShards("sqlmock", []ShardConnectString{
		{Key: "a", ConnectionString: "s3-shard-1"},
		{Key: "b", ConnectionString: "s3-shard-2"},
	}, nil, time.Second)
	if conns != nil || !errors.Is(err, ErrPing) {
		t.Fatalf("want nil, ErrPing; got %v, %v", conns, err)
	}

	if err = mock1.ExpectationsWereMet(); err != nil {
		t.Fatalf("opened shard not closed: %v", err)
	}

	if err = mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestShardQueryRowx_NoShard_RowWithError(t *testing.T) {
	c := NewShardDB(newConnections(nil, func(context.Context, []ShardConnect) ShardConnect { return nil }))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the error must survive even a canceled ctx

	row := c.QueryRowxContext(ctx, "SELECT 1")
	if row == nil {
		t.Fatal("row must not be nil")
	}

	if err := row.Err(); !errors.Is(err, ErrConnNotSelected) || !errors.Is(err, ErrQueryRowxContext) {
		t.Fatalf("Err() = %v", err)
	}

	var n int
	if err := row.Scan(&n); !errors.Is(err, ErrConnNotSelected) {
		t.Fatalf("Scan() = %v", err)
	}
}

func TestSelectContext_WrapsError(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("boom"))

	var dest []int
	err := NewDB(db).SelectContext(context.Background(), &dest, "SELECT 1")
	if !errors.Is(err, ErrSelectContext) {
		t.Fatalf("want ErrSelectContext, got %v", err)
	}
}

func TestShard_SelectorError_Wrapped(t *testing.T) {
	c := NewShardDB(newConnections(nil, func(context.Context, []ShardConnect) ShardConnect { return nil }))
	ctx := context.Background()

	if _, err := c.QueryContext(ctx, "SELECT 1"); !errors.Is(err, ErrQueryContext) || !errors.Is(err, ErrConnNotSelected) {
		t.Fatalf("QueryContext: %v", err)
	}

	if _, err := c.NamedQueryRowxContext(ctx, "SELECT :a", map[string]any{"a": 1}); !errors.Is(err, ErrNamedQueryRowxContext) ||
		!errors.Is(err, ErrConnNotSelected) {
		t.Fatalf("NamedQueryRowxContext: %v", err)
	}
}

func TestEachShard_SingleNotSupported(t *testing.T) {
	db, _ := newMockDB(t)

	err := NewDB(db).EachShard(nil)
	if !errors.Is(err, ErrMethodNotSupportedInSingle) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrate_EarlyError_ReleasesConn(t *testing.T) {
	t.Run("driver", func(t *testing.T) {
		db, mock := newMockDB(t)
		mock.ExpectQuery("CURRENT_DATABASE").WillReturnError(errors.New("boom"))

		err := NewMigrator(context.Background(), db, "d").Migrate()
		if !errors.Is(err, ErrMigrateGetDriver) {
			t.Fatalf("got %v", err)
		}

		if in := db.Stats().InUse; in != 0 {
			t.Fatalf("InUse = %d", in)
		}
	})

	driverOK := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("CURRENT_DATABASE").WillReturnRows(sqlmock.NewRows([]string{"d"}).AddRow("d"))
		mock.ExpectQuery("CURRENT_SCHEMA").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow("public"))
		mock.ExpectExec("pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectExec("pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 0))
	}

	t.Run("lock_timeout", func(t *testing.T) {
		db, mock := newMockDB(t)
		driverOK(mock)
		mock.ExpectExec("SET lock_timeout").WillReturnError(errors.New("boom"))

		err := NewMigrator(context.Background(), db, "d").Migrate()
		if !errors.Is(err, ErrMigrateLock) {
			t.Fatalf("got %v", err)
		}

		if in := db.Stats().InUse; in != 0 {
			t.Fatalf("InUse = %d", in)
		}
	})

	t.Run("migrations_dir", func(t *testing.T) {
		db, mock := newMockDB(t)
		driverOK(mock)
		mock.ExpectExec("SET lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))

		err := NewMigrator(context.Background(), db, "d").
			WithMigrationsDirectory(t.TempDir() + "/missing").Migrate()
		if !errors.Is(err, ErrMigrateReadMigrationsDir) {
			t.Fatalf("got %v", err)
		}

		if in := db.Stats().InUse; in != 0 {
			t.Fatalf("InUse = %d", in)
		}
	})
}
