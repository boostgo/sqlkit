package sqlkit

import (
	"errors"
	"fmt"

	"github.com/boostgo/sqlkit/duplicate"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// Duplicate reports whether err is a unique violation and the guessed field name.
//
// Deprecated: use IsDuplicate and DuplicateFields.
func Duplicate(err error) (bool, string) {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23505" {
		return false, ""
	}

	if duplicateErr := duplicate.Handle(pqErr); duplicateErr != nil {
		return true, duplicateErr.Field
	}

	return false, ""
}

// HandleError maps constraint violations of lib/pq to sentinel errors; it returns nil for any other non-pq error.
//
// Deprecated: use Classify / Is*.
func HandleError(err error) error {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return nil
	}

	switch pqErr.Code {
	case "23505":
		if duplicateErr := duplicate.Handle(pqErr); duplicateErr != nil {
			return fmt.Errorf("%w: %s", ErrDuplicate, duplicateErr)
		}

		return nil
	case "23503":
		return fmt.Errorf("%w: %s", ErrForeignKeyViolation, pqErr.Detail)
	case "23502":
		return fmt.Errorf("%w: %s", ErrNotNull, pqErr.Detail)
	default:
		return err
	}
}

// DuplicateFields returns the columns of a unique violation.
//
// For PostgreSQL (*pq.Error and *pgconn.PgError, SQLSTATE 23505) the columns are taken from the error
// Detail; for SQLite (*sqlite.Error) from the message "UNIQUE constraint failed: t.a, t.b" (the table
// prefix is removed). If err is not a unique violation or the columns can not be parsed, it returns
// nil, false.
func DuplicateFields(err error) (fields []string, ok bool) {
	if sqliteErr, isSQLite := sqliteError(err); isSQLite {
		return sqliteDuplicateFields(sqliteErr)
	}

	var detail string

	var pqErr *pq.Error
	var pgErr *pgconn.PgError

	switch {
	case errors.As(err, &pqErr):
		if pqErr.Code != "23505" {
			return nil, false
		}

		detail = pqErr.Detail
	case errors.As(err, &pgErr):
		if pgErr.Code != "23505" {
			return nil, false
		}

		detail = pgErr.Detail
	default:
		return nil, false
	}

	fields, _, ok = duplicate.ParseDetail(detail)
	if !ok {
		return nil, false
	}

	return fields, true
}
