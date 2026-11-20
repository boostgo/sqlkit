package sqlkit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"
)

// DefaultMigrationsDirectory is the directory read by a Migrator without WithMigrationsDirectory/WithFS.
const DefaultMigrationsDirectory = "./migrations"

// Migrator applies golang-migrate "up" migrations (files like 0001_init.up.sql) to a PostgreSQL
// (drivers "postgres", "pgx") or SQLite (driver "sqlite") database. The migrations are read from a
// directory on disk (default "./migrations") or from an fs.FS such as an embed.FS (WithFS).
type Migrator struct {
	ctx                 context.Context
	conn                *sqlx.DB
	database            string
	migrationsDirectory string
	fsys                fs.FS
	table               string
}

// NewMigrator creates a Migrator for conn. database is the name of the database reported by
// golang-migrate (used in its logs and errors). ctx is used only by the deprecated Migrate;
// MigrateContext takes its own.
func NewMigrator(
	ctx context.Context,
	conn *sqlx.DB,
	database string,
) *Migrator {
	return &Migrator{
		ctx:                 ctx,
		conn:                conn,
		database:            database,
		migrationsDirectory: DefaultMigrationsDirectory,
	}
}

// WithMigrationsDirectory reads the migrations from directory on disk.
func (m *Migrator) WithMigrationsDirectory(directory string) *Migrator {
	m.migrationsDirectory = directory
	m.fsys = nil

	return m
}

// WithFS reads the migrations from dir of fsys, typically an embed.FS:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	sqlkit.NewMigrator(ctx, conn, "app").WithFS(migrations, "migrations")
func (m *Migrator) WithFS(fsys fs.FS, dir string) *Migrator {
	m.fsys = fsys
	m.migrationsDirectory = dir

	return m
}

// WithMigrationsTable sets the name of the table where golang-migrate stores the schema version
// (default "schema_migrations").
func (m *Migrator) WithMigrationsTable(table string) *Migrator {
	m.table = table

	return m
}

// Migrate runs migrations with the ctx given to NewMigrator.
//
// Deprecated: use MigrateContext.
func (m *Migrator) Migrate() error {
	return m.MigrateContext(m.ctx)
}

// MigrateContext applies all pending up migrations. No pending migrations is not an error.
//
// The driver is chosen by the driver name of the connection: "sqlite" uses the SQLite driver of
// golang-migrate on the pool itself; anything else is treated as PostgreSQL and runs on one
// dedicated connection with lock_timeout = 60s. ClickHouse is not supported.
func (m *Migrator) MigrateContext(ctx context.Context) error {
	switch m.conn.DriverName() {
	case SQLiteDriver:
		return m.migrateSQLite(ctx)
	case ChDriver:
		return fmt.Errorf("%w: %s", ErrMigrateUnsupportedDriver, ChDriver)
	default:
		return m.migratePostgres(ctx)
	}
}

func (m *Migrator) source() (source.Driver, error) {
	fsys, dir := m.fsys, m.migrationsDirectory
	if fsys == nil {
		fsys, dir = os.DirFS(m.migrationsDirectory), "."
	}

	src, err := iofs.New(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMigrateReadMigrationsDir, err)
	}

	return src, nil
}

func (m *Migrator) migratePostgres(ctx context.Context) error {
	nativeConn, err := m.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrateOpenConn, err)
	}

	driver, err := postgres.WithConnection(ctx, nativeConn, &postgres.Config{MigrationsTable: m.table})
	if err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrMigrateGetDriver, err), nativeConn.Close())
	}

	_, err = nativeConn.ExecContext(ctx, "SET lock_timeout = '60s';")
	if err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrMigrateLock, err), driver.Close())
	}

	src, err := m.source()
	if err != nil {
		return errors.Join(err, driver.Close())
	}

	migrator, err := migrate.NewWithInstance("iofs", src, m.database, driver)
	if err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrMigrateReadMigrationsDir, err), src.Close(), driver.Close())
	}
	defer migrator.Close() //nolint:errcheck // best effort, the migration result matters

	return up(migrator)
}

func (m *Migrator) migrateSQLite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrMigrateOpenConn, err)
	}

	// The SQLite driver of golang-migrate closes the *sql.DB it was given on Close, so the
	// migrator itself is never closed here: only the source is. The pool stays usable.
	driver, err := sqlitemigrate.WithInstance(m.conn.DB, &sqlitemigrate.Config{
		MigrationsTable: m.table,
		DatabaseName:    m.database,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrateGetDriver, err)
	}

	src, err := m.source()
	if err != nil {
		return err
	}
	defer src.Close() //nolint:errcheck // best effort, the migration result matters

	migrator, err := migrate.NewWithInstance("iofs", src, m.database, driver)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMigrateReadMigrationsDir, err)
	}

	return up(migrator)
}

func up(migrator *migrate.Migrate) error {
	if err := migrator.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			return nil
		}

		return fmt.Errorf("%w: %w", ErrMigrateUp, err)
	}

	return nil
}
