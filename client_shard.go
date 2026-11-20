package sqlkit

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
	"golang.org/x/sync/errgroup"
)

// ConnectionSelector chooses the shard for a query, typically by a key stored in ctx.
// Returning nil makes the query fail with ErrConnNotSelected.
type ConnectionSelector func(ctx context.Context, connections []ShardConnect) ShardConnect

type clientShard struct {
	connections *Connections
}

// NewShardDB creates DB implementation as shard client.
//
// Need to provide Connections object which contains multiple connections for sharding
func NewShardDB(connections *Connections) DB {
	return &clientShard{
		connections: connections,
	}
}

func (c *clientShard) Connection() *sqlx.DB {
	return nil
}

// DriverName returns the driver name of the first shard ("" without shards).
func (c *clientShard) DriverName() string {
	if c.connections == nil || len(c.connections.connections) == 0 {
		return ""
	}

	return c.connections.connections[0].Conn().DriverName()
}

func (c *clientShard) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrExecContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecContext, err)
	}

	var result sql.Result

	tx, ok := GetTx(ctx)
	if ok {
		result, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrExecContext, err)
		}

		return result, nil
	}

	result, err = raw.Conn().ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExecContext, err)
	}

	return result, nil
}

func (c *clientShard) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrQueryContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryContext, err)
	}

	var rows *sql.Rows

	tx, ok := GetTx(ctx)
	if ok {
		rows, err = tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQueryContext, err)
		}

		return rows, nil
	}

	rows, err = raw.Conn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryContext, err)
	}

	return rows, nil
}

func (c *clientShard) QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrQueryxContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryxContext, err)
	}

	var rows *sqlx.Rows

	tx, ok := GetTx(ctx)
	if ok {
		rows, err = tx.QueryxContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQueryxContext, err)
		}

		return rows, nil
	}

	rows, err = raw.Conn().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQueryxContext, err)
	}

	return rows, nil
}

func (c *clientShard) QueryRowxContext(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	raw, err := c.selectConnect(ctx)
	if err != nil {
		return errRow(ctx, fmt.Errorf("%w: %w", ErrQueryRowxContext, err))
	}

	tx, ok := GetTx(ctx)
	if ok {
		return tx.QueryRowxContext(ctx, query, args...)
	}

	return raw.Conn().QueryRowxContext(ctx, query, args...)
}

func (c *clientShard) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPrepareContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareContext, err)
	}

	var stmt *sql.Stmt

	tx, ok := GetTx(ctx)
	if ok {
		stmt, err = tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPrepareContext, err)
		}

		return stmt, nil
	}

	stmt, err = raw.Conn().PrepareContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareContext, err)
	}

	return stmt, nil
}

func (c *clientShard) NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, err)
	}

	tx, ok := GetTx(ctx)
	if ok {
		var result sql.Result
		result, err = tx.NamedExecContext(ctx, query, arg)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, err)
		}

		return result, nil
	}

	result, err := raw.Conn().NamedExecContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedExecContext, err)
	}

	return result, nil
}

func (c *clientShard) SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrSelectContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelectContext, err)
	}

	tx, ok := GetTx(ctx)
	if ok {
		if err = tx.SelectContext(ctx, dest, query, args...); err != nil {
			return fmt.Errorf("%w: %w", ErrSelectContext, err)
		}

		return nil
	}

	if err = raw.Conn().SelectContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("%w: %w", ErrSelectContext, err)
	}

	return nil
}

func (c *clientShard) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrGetContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGetContext, err)
	}

	tx, ok := GetTx(ctx)
	if ok {
		if err = tx.GetContext(ctx, dest, query, args...); err != nil {
			return fmt.Errorf("%w: %w", ErrGetContext, err)
		}

		return nil
	}

	if err = raw.Conn().GetContext(ctx, dest, query, args...); err != nil {
		return fmt.Errorf("%w: %w", ErrGetContext, err)
	}

	return nil
}

func (c *clientShard) PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, err)
	}

	var stmt *sqlx.NamedStmt

	tx, ok := GetTx(ctx)
	if ok {
		stmt, err = tx.PrepareNamedContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, err)
		}

		return stmt, nil
	}

	stmt, err = raw.Conn().PrepareNamedContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPrepareNamedContext, err)
	}

	return stmt, nil
}

func (c *clientShard) NamedQueryRowxContext(ctx context.Context, query string, arg any) (*sqlx.Row, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, ctx.Err())
	default:
	}

	raw, err := c.selectConnect(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, err)
	}

	// convert name vars to ?
	namedQuery, args, err := sqlx.Named(query, arg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, err)
	}

	tx, ok := GetTx(ctx)

	driverName := raw.Conn().DriverName()
	if ok {
		driverName = tx.DriverName()
	}

	// convert ? to the placeholders of the driver ($1, $2... by default)
	convertedQuery := sqlx.Rebind(bindType(driverName), namedQuery)

	var row *sqlx.Row
	if ok {
		row = tx.QueryRowxContext(ctx, convertedQuery, args...)
	} else {
		row = raw.Conn().QueryRowxContext(ctx, convertedQuery, args...)
	}

	if row.Err() != nil {
		return row, fmt.Errorf("%w: %w", ErrNamedQueryRowxContext, row.Err())
	}

	return row, nil
}

// EachShard runs provided fn function with every shard single connection
func (c *clientShard) EachShard(fn func(conn DB) error) error {
	return EachShard(c, fn)
}

// EachShardAsync do the same as EachShard but in parallel every shard.
//
// If provide "limit", count of goroutines will be limited
func (c *clientShard) EachShardAsync(fn func(conn DB) error, limit ...int) error {
	return EachShardAsync(c, fn, limit...)
}

func (c *clientShard) selectConnect(ctx context.Context) (ShardConnect, error) {
	return c.connections.Get(ctx)
}

// EachShard runs provided fn function with every shard single connection
func EachShard(conn DB, fn func(conn DB) error) (err error) {
	shardClient, ok := conn.(*clientShard)
	if !ok {
		return ErrConnectionIsNotShard
	}

	for _, shard := range shardClient.connections.connections {
		if err = fn(NewDB(shard.Conn())); err != nil {
			return err
		}
	}

	return nil
}

// EachShardAsync do the same as EachShard but in parallel every shard.
//
// If provide "limit", count of goroutines will be limited
func EachShardAsync(conn DB, fn func(conn DB) error, limit ...int) (err error) {
	shardClient, ok := conn.(*clientShard)
	if !ok {
		return ErrConnectionIsNotShard
	}

	wg := errgroup.Group{}
	if len(limit) > 0 && limit[0] > 0 {
		wg.SetLimit(limit[0])
	}

	for _, shard := range shardClient.connections.connections {
		wg.Go(func() error {
			return fn(NewDB(shard.Conn()))
		})
	}

	return wg.Wait()
}
