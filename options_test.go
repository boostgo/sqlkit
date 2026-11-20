package sqlkit

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestWithMaxOpen_ZeroIdleStillApplies(t *testing.T) {
	db, _ := newMockDB(t)

	WithMaxOpen(5)(db)

	if got := db.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}
}

func TestConnectorBuild_SSLMode(t *testing.T) {
	c := NewConnector().Host("h").Port(1).Username("u").Password("p").Database("d")

	if got, want := c.Build(), "host=h port=1 user=u password=p dbname=d sslmode=disable"; got != want {
		t.Fatalf("got %q", got)
	}

	if got, want := c.SSLMode("require").Build(), "host=h port=1 user=u password=p dbname=d sslmode=require"; got != want {
		t.Fatalf("got %q", got)
	}

	if got, want := c.BuildMigration(), "host=h port=1 user=u password=p dbname=d sslmode=require"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestBuildClickhouse_Scheme(t *testing.T) {
	c := NewConnector().Host("h").Port(1).Username("u").Password("p").Database("d")

	t.Setenv("LOCAL", "true")

	if got := c.BuildClickhouse(); got != "http://u:p@h:1/d?" {
		t.Fatalf("LOCAL: %q", got)
	}

	if got := c.ClickhouseScheme("https").BuildClickhouse(); got != "https://u:p@h:1/d?" {
		t.Fatalf("explicit must win: %q", got)
	}

	t.Setenv("LOCAL", "")

	if got := NewConnector().Host("h").Port(1).BuildClickhouse(); got[:8] != "https://" {
		t.Fatalf("default: %q", got)
	}
}

func TestNamedQueryRowx_BindByDriver(t *testing.T) {
	for driver, want := range map[string]string{
		"clickhouse": "SELECT ?",
		"postgres":   "SELECT $1",
		"unknown-xx": "SELECT $1",
	} {
		t.Run(driver, func(t *testing.T) {
			raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}

			db := sqlx.NewDb(raw, driver)
			t.Cleanup(func() { _ = db.Close() })

			mock.ExpectQuery(want).WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"a"}).AddRow(1))

			row, err := NewDB(db).NamedQueryRowxContext(context.Background(), "SELECT :a", map[string]any{"a": 1})
			if err != nil {
				t.Fatal(err)
			}

			var n int
			if err = row.Scan(&n); err != nil {
				t.Fatal(err)
			}

			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigrateContext_CanceledCtx(t *testing.T) {
	db, _ := newMockDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewMigrator(context.Background(), db, "d").MigrateContext(ctx)
	if !errors.Is(err, ErrMigrateOpenConn) {
		t.Fatalf("got %v", err)
	}

	if in := db.Stats().InUse; in != 0 {
		t.Fatalf("InUse = %d", in)
	}
}
