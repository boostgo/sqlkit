package sqlkit

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

// SQLite defaults applied by SQLiteDSN for zero SQLiteOptions fields.
const (
	DefaultSQLiteJournalMode   = "WAL"
	DefaultSQLiteSynchronous   = "NORMAL"
	DefaultSQLiteBusyTimeoutMs = 5000
	DefaultSQLiteTxLock        = "immediate"
)

// SQLiteOptions are the per-connection settings of a SQLite database (driver "sqlite",
// modernc.org/sqlite). Zero values mean the defaults, which suit an application database:
// WAL journal, synchronous=NORMAL, foreign keys on, a 5s busy timeout and IMMEDIATE write
// transactions.
type SQLiteOptions struct {
	// JournalMode is PRAGMA journal_mode; empty means "WAL".
	JournalMode string `json:"journal_mode" yaml:"journalMode"`
	// Synchronous is PRAGMA synchronous; empty means "NORMAL" (safe with WAL).
	Synchronous string `json:"synchronous" yaml:"synchronous"`
	// BusyTimeoutMs is PRAGMA busy_timeout in milliseconds; 0 means 5000, a negative value disables it.
	BusyTimeoutMs int `json:"busy_timeout_ms" yaml:"busyTimeoutMs"`
	// DisableForeignKeys turns PRAGMA foreign_keys off (it is on by default).
	DisableForeignKeys bool `json:"disable_foreign_keys" yaml:"disableForeignKeys"`
	// TxLock is the BEGIN mode of read-write transactions: "deferred", "immediate" or "exclusive";
	// empty means "immediate", which avoids SQLITE_BUSY when a read transaction upgrades to a write.
	// Read-only transactions (ReadOnly()) always use a plain deferred BEGIN.
	TxLock string `json:"tx_lock" yaml:"txLock"`
	// Pragmas are extra pragmas applied to every connection, separated by ";" and written without
	// the PRAGMA keyword, e.g. "cache_size(-20000); temp_store(MEMORY)". A string (not a slice)
	// keeps Config comparable.
	Pragmas string `json:"pragmas" yaml:"pragmas"`
}

// SQLiteDSN builds a modernc.org/sqlite connection string for path with the pragmas of opts.
//
// path is a file path ("app.db", "/var/lib/app/data.db"), ":memory:" or a "file:" URI. Query
// parameters already present in path are kept; the pragmas of opts are added after them. Time
// values are written in the SQLite format ("_time_format=sqlite"), which sorts correctly as text
// and keeps cursor pagination over time columns stable.
func SQLiteDSN(path string, opts SQLiteOptions) string {
	q := url.Values{}

	busy := opts.BusyTimeoutMs
	if busy == 0 {
		busy = DefaultSQLiteBusyTimeoutMs
	}

	if busy > 0 {
		q.Add("_pragma", "busy_timeout("+strconv.Itoa(busy)+")")
	}

	q.Add("_pragma", "journal_mode("+firstNonZero(opts.JournalMode, DefaultSQLiteJournalMode)+")")
	q.Add("_pragma", "synchronous("+firstNonZero(opts.Synchronous, DefaultSQLiteSynchronous)+")")

	if opts.DisableForeignKeys {
		q.Add("_pragma", "foreign_keys(0)")
	} else {
		q.Add("_pragma", "foreign_keys(1)")
	}

	for _, p := range strings.Split(opts.Pragmas, ";") {
		if p = strings.TrimSpace(p); p != "" {
			q.Add("_pragma", p)
		}
	}

	q.Set("_txlock", firstNonZero(opts.TxLock, DefaultSQLiteTxLock))
	q.Set("_time_format", "sqlite")

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	return path + sep + q.Encode()
}

// isSQLiteMemory reports whether dsn of driverName is an in-memory SQLite database. Every connection
// to such a database gets its own empty database, so the pool must hold exactly one connection.
func isSQLiteMemory(driverName, dsn string) bool {
	if driverName != SQLiteDriver {
		return false
	}

	path, query, _ := strings.Cut(dsn, "?")

	return path == ":memory:" || path == "file::memory:" || path == "" ||
		strings.Contains(query, "mode=memory")
}

// sqliteMemoryPool keeps the only connection of an in-memory SQLite database alive forever.
func sqliteMemoryPool(conn *sqlx.DB) {
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)
	conn.SetConnMaxIdleTime(0)
}
