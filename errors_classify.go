package sqlkit

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// PostgreSQL SQLSTATE codes of constraint violations.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgNotNullViolation    = "23502"
	pgCheckViolation      = "23514"
)

// pgCode returns SQLSTATE and Detail of a PostgreSQL error from lib/pq or pgx.
//
//nolint:unparam // detail is part of the helper contract; current callers need only the code
func pgCode(err error) (code, detail string, ok bool) {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), pqErr.Detail, true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.Detail, true
	}

	return "", "", false
}

// sqliteError returns the modernc.org/sqlite error in the chain of err.
func sqliteError(err error) (*sqlite.Error, bool) {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr, true
	}

	return nil, false
}

// classifySentinel returns the sentinel of a constraint violation in err (nil if there is none).
func classifySentinel(err error) error {
	if code, _, ok := pgCode(err); ok {
		switch code {
		case pgUniqueViolation:
			return ErrDuplicate
		case pgForeignKeyViolation:
			return ErrForeignKeyViolation
		case pgNotNullViolation:
			return ErrNotNull
		case pgCheckViolation:
			return ErrCheckViolation
		}

		return nil
	}

	if sqliteErr, ok := sqliteError(err); ok {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return ErrDuplicate
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return ErrForeignKeyViolation
		case sqlite3.SQLITE_CONSTRAINT_NOTNULL:
			return ErrNotNull
		case sqlite3.SQLITE_CONSTRAINT_CHECK:
			return ErrCheckViolation
		}
	}

	return nil
}

// Classify maps a constraint violation of PostgreSQL (lib/pq or pgx) or SQLite (modernc.org/sqlite)
// to ErrDuplicate, ErrForeignKeyViolation, ErrNotNull or ErrCheckViolation, keeping the original
// error in the chain (errors.As still finds *pq.Error, *pgconn.PgError or *sqlite.Error).
//
//	PostgreSQL 23505, SQLite UNIQUE / PRIMARY KEY -> ErrDuplicate
//	PostgreSQL 23503, SQLite FOREIGN KEY          -> ErrForeignKeyViolation
//	PostgreSQL 23502, SQLite NOT NULL             -> ErrNotNull
//	PostgreSQL 23514, SQLite CHECK                -> ErrCheckViolation
//
// Any other error is returned as is (never nil for a non-nil err); Classify(nil) is nil.
func Classify(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, ErrDuplicate) || errors.Is(err, ErrForeignKeyViolation) ||
		errors.Is(err, ErrNotNull) || errors.Is(err, ErrCheckViolation) {
		return err
	}

	sentinel := classifySentinel(err)
	if sentinel == nil {
		return err
	}

	return fmt.Errorf("%w: %w", sentinel, err)
}

// IsDuplicate reports whether err is a unique (or primary key) violation.
func IsDuplicate(err error) bool { return errors.Is(Classify(err), ErrDuplicate) }

// IsForeignKey reports whether err is a foreign key violation.
func IsForeignKey(err error) bool { return errors.Is(Classify(err), ErrForeignKeyViolation) }

// IsNotNull reports whether err is a not-null violation.
func IsNotNull(err error) bool { return errors.Is(Classify(err), ErrNotNull) }

// IsCheckViolation reports whether err is a CHECK constraint violation.
func IsCheckViolation(err error) bool { return errors.Is(Classify(err), ErrCheckViolation) }

// IsNoRows reports whether err is sql.ErrNoRows.
func IsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// sqliteUniqueRe matches the message of a SQLite unique violation:
// "UNIQUE constraint failed: users.email, users.tenant_id".
var sqliteUniqueRe = regexp.MustCompile(`UNIQUE constraint failed: ([^()]+?)(?: \(\d+\))?$`)

// sqliteDuplicateFields returns the columns of a SQLite unique violation without the table prefix.
func sqliteDuplicateFields(e *sqlite.Error) ([]string, bool) {
	if e.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE && e.Code() != sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		return nil, false
	}

	m := sqliteUniqueRe.FindStringSubmatch(e.Error())
	if m == nil {
		return nil, false
	}

	cols := strings.Split(m[1], ", ")
	for i, c := range cols {
		if _, col, ok := strings.Cut(c, "."); ok {
			cols[i] = col
		}
	}

	return cols, true
}
