package sqlkit

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jmoiron/sqlx"
)

// errConnector is a driver.Connector whose Connect always fails with err.
type errConnector struct{ err error }

func (c errConnector) Connect(context.Context) (driver.Conn, error) { return nil, c.err }

func (c errConnector) Driver() driver.Driver { return errDriver(c) }

type errDriver struct{ err error }

func (d errDriver) Open(string) (driver.Conn, error) { return nil, d.err }

// errRow returns a *sqlx.Row whose Err and Scan return err.
//
// sqlx.Row has no public constructor, so the error is produced by a connector that fails to connect.
// The row holds no connection, so closing the throwaway DB right away is safe.
func errRow(ctx context.Context, err error) *sqlx.Row {
	db := sqlx.NewDb(sql.OpenDB(errConnector{err: err}), "errrow")
	row := db.QueryRowxContext(context.WithoutCancel(ctx), "SELECT 1")
	_ = db.Close()

	return row
}
