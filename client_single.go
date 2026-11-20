package sqlkit

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

type clientSingle struct {
	conn   *sqlx.DB
	paging PagingMode
}

// DBOption configures a client created by NewDBWithOptions.
type DBOption func(*clientSingle)

// WithPaging sets the default pagination approach used by NewPager (default: PagingOffset).
func WithPaging(mode PagingMode) DBOption {
	return func(c *clientSingle) { c.paging = mode }
}

// NewDB creates DB implementation by single client
func NewDB(conn *sqlx.DB) DB {
	return NewDBWithOptions(conn)
}

// NewDBWithOptions creates DB implementation by single client with options.
func NewDBWithOptions(conn *sqlx.DB, opts ...DBOption) DB {
	c := &clientSingle{conn: conn}
	for _, opt := range opts {
		opt(c)
	}

	return c
}

func (c *clientSingle) pagingMode() PagingMode { return c.paging }

func (c *clientSingle) Connection() *sqlx.DB {
	return c.conn
}

// DriverName returns the driver name of the connection.
func (c *clientSingle) DriverName() string {
	return c.conn.DriverName()
}

func (c *clientSingle) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrExecContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrExecContext, err)
		}

		return result, nil
	}

	result, err := c.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecContext, err)
	}

	return result, nil
}

func (c *clientSingle) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrQueryContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQueryContext, err)
		}

		return rows, nil
	}

	rows, err := c.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryContext, err)
	}

	return rows, nil
}

func (c *clientSingle) QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrQueryxContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		rows, err := tx.QueryxContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQueryxContext, err)
		}

		return rows, nil
	}

	rows, err := c.conn.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryxContext, err)
	}

	return rows, nil
}

func (c *clientSingle) QueryRowxContext(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	tx, ok := GetTx(ctx)
	if ok {
		return tx.QueryRowxContext(ctx, query, args...)
	}

	return c.conn.QueryRowxContext(ctx, query, args...)
}

func (c *clientSingle) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPrepareContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPrepareContext, err)
		}

		return stmt, nil
	}

	stmt, err := c.conn.PrepareContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareContext, err)
	}

	return stmt, nil
}

func (c *clientSingle) NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		result, err := tx.NamedExecContext(ctx, query, arg)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, err)
		}

		return result, nil
	}

	result, err := c.conn.NamedExecContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, err)
	}

	return result, nil
}

func (c *clientSingle) SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrSelectContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		if err := tx.SelectContext(ctx, dest, query, args...); err != nil {
			return fmt.Errorf("%w: %w", ErrSelectContext, err)
		}

		return nil
	}

	if err := c.conn.SelectContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("%w: %w", ErrSelectContext, err)
	}

	return nil
}

func (c *clientSingle) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrGetContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		if err := tx.GetContext(ctx, dest, query, args...); err != nil {
			return fmt.Errorf("%w: %w", ErrGetContext, err)
		}

		return nil
	}

	if err := c.conn.GetContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("%w: %w", ErrGetContext, err)
	}

	return nil
}

func (c *clientSingle) PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, ctx.Err())
	default:
	}

	tx, ok := GetTx(ctx)
	if ok {
		stmt, err := tx.PrepareNamedContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, err)
		}

		return stmt, nil
	}

	stmt, err := c.conn.PrepareNamedContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, err)
	}

	return stmt, nil
}

func (c *clientSingle) NamedQueryRowxContext(ctx context.Context, query string, arg any) (*sqlx.Row, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, ctx.Err())
	default:
	}

	// convert name vars to ?
	namedQuery, args, err := sqlx.Named(query, arg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, err)
	}

	tx, ok := GetTx(ctx)

	driverName := c.conn.DriverName()
	if ok {
		driverName = tx.DriverName()
	}

	// convert ? to the placeholders of the driver ($1, $2... by default)
	convertedQuery := sqlx.Rebind(bindType(driverName), namedQuery)

	var row *sqlx.Row
	if ok {
		row = tx.QueryRowxContext(ctx, convertedQuery, args...)
	} else {
		row = c.conn.QueryRowxContext(ctx, convertedQuery, args...)
	}

	if row.Err() != nil {
		return row, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, row.Err())
	}

	return row, nil
}

func (c *clientSingle) EachShard(_ func(conn DB) error) error {
	return ErrMethodNotSupportedInSingle
}

func (c *clientSingle) EachShardAsync(_ func(conn DB) error, _ ...int) error {
	return ErrMethodNotSupportedInSingle
}

// Page returns offset & limit by pagination
func Page(pageSize int64, page int) (offset int, limit int) {
	if page == 0 {
		page = 1
	}

	offset = (page - 1) * int(pageSize)
	limit = int(pageSize)
	return offset, limit
}
