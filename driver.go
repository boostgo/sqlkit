package sqlkit

import (
	"database/sql"
	"database/sql/driver"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/mailru/go-clickhouse"

	// registers the pure-Go (cgo-free) "sqlite" driver
	_ "modernc.org/sqlite"
)

// Driver names understood by the package.
const (
	// PqDriver is the github.com/lib/pq PostgreSQL driver. It is the default when no driver is given.
	PqDriver = "postgres"
	// PgxDriver is the github.com/jackc/pgx/v5 PostgreSQL driver (database/sql adapter).
	PgxDriver = "pgx"
	// ChDriver is the github.com/mailru/go-clickhouse ClickHouse driver (HTTP protocol).
	ChDriver = "clickhouse"
	// SQLiteDriver is the pure-Go modernc.org/sqlite driver (no cgo).
	SQLiteDriver = "sqlite"
)

func init() {
	// register drivers
	RegisterDriver(PqDriver, &pq.Driver{})
	RegisterDriver(PgxDriver, stdlib.GetDefaultDriver())
	sqlx.BindDriver(ChDriver, sqlx.QUESTION)
	sqlx.BindDriver(SQLiteDriver, sqlx.QUESTION)
	clickhouse.Map("") // reference the clickhouse package so that its driver is registered
}

// RegisterDriver registers driver under driverName via sql.Register, unless a driver with that name
// is already registered (sql.Register would panic).
func RegisterDriver(driverName string, driver driver.Driver) {
	if slices.Contains(sql.Drivers(), driverName) {
		return
	}

	sql.Register(driverName, driver)
}

// bindType returns the placeholder style of the driver; $N for unknown drivers.
func bindType(driverName string) int {
	if bt := sqlx.BindType(driverName); bt != sqlx.UNKNOWN {
		return bt
	}

	return sqlx.DOLLAR
}

// driverNamer is implemented by the clients of this package.
type driverNamer interface {
	DriverName() string
}

// DriverName returns the database/sql driver name used by db ("postgres", "pgx", "clickhouse",
// "sqlite", ...). For a shard client it is the driver of the shards; for a Cluster the driver of the
// primary. It returns "" if the driver can not be determined (e.g. a mock DB).
func DriverName(db DB) string {
	if db == nil {
		return ""
	}

	if n, ok := db.(driverNamer); ok {
		return n.DriverName()
	}

	if conn := db.Connection(); conn != nil {
		return conn.DriverName()
	}

	return ""
}

// Rebind converts a query written with "?" placeholders to the placeholder style of the driver of db:
// "$1, $2, ..." for PostgreSQL (and unknown drivers), "?" for SQLite and ClickHouse.
//
// It lets a repository keep one query text for every supported database.
func Rebind(db DB, query string) string {
	return sqlx.Rebind(bindType(DriverName(db)), query)
}

// Placeholder returns a placeholder func for Pager.Where that matches the bind style of driverName:
// Dollar(start) for PostgreSQL and unknown drivers, Question() for SQLite and ClickHouse
// (start is ignored for "?" placeholders: the arguments are positional).
func Placeholder(driverName string, start int) func(v any) string {
	if bindType(driverName) == sqlx.QUESTION {
		return Question()
	}

	return Dollar(start)
}

// Question returns a placeholder func for Pager.Where that always returns "?".
func Question() func(v any) string {
	return func(any) string { return "?" }
}

// Dollar returns a placeholder func for Pager.Where: "$start", "$start+1", ... It ignores the values.
func Dollar(start int) func(v any) string {
	n := start - 1

	return func(any) string {
		n++

		return "$" + strconv.Itoa(n)
	}
}
