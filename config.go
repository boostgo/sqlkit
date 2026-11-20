package sqlkit

import (
	"time"
)

// Config describes one database connection. It can be loaded from JSON or YAML.
//
// PostgreSQL and ClickHouse use Host, Port, Username, Password and Database (or DSN). SQLite
// (Driver "sqlite") uses DSN as the file path, ":memory:" or a "file:" URI plus the SQLite options.
type Config struct {
	// Host is the database host.
	Host string `json:"host" yaml:"host"`
	// Port is the database port.
	Port int `json:"port" yaml:"port"`
	// Username is the database user.
	Username string `json:"username" yaml:"username"`
	// Password is the password of the database user.
	Password string `json:"password" yaml:"password"`
	// Database is the database name.
	Database string `json:"database" yaml:"database"`
	// TimeoutMs is the timeout of the initial ping in milliseconds; 0 means no timeout.
	TimeoutMs int `json:"timeout_ms" yaml:"timeoutMs"`

	// DSN is a ready connection string used as is instead of Host/Port/... For Driver "sqlite" it is
	// the database file path, ":memory:" or a "file:" URI, and the pragmas of SQLite are added to it.
	DSN string `json:"dsn" yaml:"dsn"`

	// MigrationHost overrides Host for migrations; empty means Host.
	MigrationHost string `json:"migration_host" yaml:"migrationHost"`
	// MigrationPort overrides Port for migrations; 0 means Port.
	MigrationPort int `json:"migration_port" yaml:"migrationPort"`
	// MigrationUsername overrides Username for migrations; empty means Username.
	MigrationUsername string `json:"migration_username" yaml:"migrationUsername"`
	// MigrationPassword overrides Password for migrations; empty means Password.
	MigrationPassword string `json:"migration_password" yaml:"migrationPassword"`
	// MigrationDatabase overrides Database for migrations; empty means Database.
	MigrationDatabase string `json:"migration_database" yaml:"migrationDatabase"`

	// BinaryParameters adds binary_parameters=yes to a lib/pq connection string.
	BinaryParameters bool `json:"binary_parameters" yaml:"binaryParameters"`
	// MaxOpenConnections is the pool size; 0 means the default (10).
	MaxOpenConnections int `json:"max_open_connections" yaml:"maxOpenConnections"`
	// MaxIdleConnections is the number of idle connections kept; 0 means the default (10).
	MaxIdleConnections int `json:"max_idle_connections" yaml:"maxIdleConnections"`
	// MaxLifetime is the max connection lifetime in seconds; 0 means the default (30m).
	MaxLifetime int `json:"max_lifetime" yaml:"maxLifetime"`
	// MaxIdleTime is the max connection idle time in seconds; 0 means the default (5m).
	MaxIdleTime int `json:"max_idle_time" yaml:"maxIdleTime"`
	// ReadTimeout is the ClickHouse read timeout in seconds; 0 means the driver default.
	ReadTimeout int `json:"read_timeout" yaml:"readTimeout"`
	// WriteTimeout is the ClickHouse write timeout in seconds; 0 means the driver default.
	WriteTimeout int `json:"write_timeout" yaml:"writeTimeout"`
	// Driver is "postgres" (default), "pgx", "clickhouse" or "sqlite".
	Driver string `json:"driver" yaml:"driver"`

	// SSLMode is the sslmode of the connection string; empty means "disable".
	SSLMode string `json:"ssl_mode" yaml:"sslMode"`
	// ClickhouseScheme is "http" or "https"; empty means "http" if env LOCAL=true, otherwise "https".
	ClickhouseScheme string `json:"clickhouse_scheme" yaml:"clickhouseScheme"`

	// SQLite holds the pragmas of a SQLite connection (Driver "sqlite" only).
	SQLite SQLiteOptions `json:"sqlite" yaml:"sqlite"`
}

// Validate checks that the config has everything needed to connect: DSN for SQLite or when DSN
// is set, otherwise Host, Port, Database, Username and Password.
func (c *Config) Validate() error {
	if c.Driver == SQLiteDriver {
		if c.DSN == "" {
			return ErrConfigNoDSN
		}

		return nil
	}

	if c.DSN != "" {
		return nil
	}

	if c.Host == "" {
		return ErrConfigNoHost
	}

	if c.Port == 0 {
		return ErrConfigNoPort
	}

	if c.Database == "" {
		return ErrConfigNoDatabase
	}

	if c.Username == "" {
		return ErrConfigNoUsername
	}

	if c.Password == "" {
		return ErrConfigNoPassword
	}

	return nil
}

// ConnectionString returns the connection string of the config for its driver: DSN as is (with the
// pragmas for SQLite), a ClickHouse URL or a libpq key=value string.
func (c *Config) ConnectionString() string {
	if dsn, ok := c.rawDSN(); ok {
		return dsn
	}

	connector := NewConnector().
		Host(c.Host).
		Port(c.Port).
		Username(c.Username).
		Password(c.Password).
		Database(c.Database).
		BinaryParameters(c.BinaryParameters).
		MaxOpenConnections(c.MaxOpenConnections).
		MaxIdleConnections(c.MaxIdleConnections).
		ConnectionMaxLifetime(time.Duration(c.MaxLifetime) * time.Second).
		MaxIdleTime(time.Duration(c.MaxIdleTime) * time.Second).
		ReadTimeout(c.ReadTimeout).
		WriteTimeout(c.WriteTimeout).
		SSLMode(c.SSLMode).
		ClickhouseScheme(c.ClickhouseScheme)

	if c.Driver == ChDriver {
		return connector.BuildClickhouse()
	}

	return connector.Build()
}

// MigrationConnectionString is ConnectionString with the Migration* overrides applied.
func (c *Config) MigrationConnectionString() string {
	if dsn, ok := c.rawDSN(); ok {
		return dsn
	}

	connector := NewConnector().
		Host(c.Host).
		Port(c.Port).
		Username(c.Username).
		Password(c.Password).
		Database(c.Database).
		MigrationHost(c.MigrationHost).
		MigrationPort(c.MigrationPort).
		MigrationUsername(c.MigrationUsername).
		MigrationPassword(c.MigrationPassword).
		MigrationDatabase(c.MigrationDatabase).
		BinaryParameters(c.BinaryParameters).
		MaxOpenConnections(c.MaxOpenConnections).
		MaxIdleConnections(c.MaxIdleConnections).
		ConnectionMaxLifetime(time.Duration(c.MaxLifetime) * time.Second).
		MaxIdleTime(time.Duration(c.MaxIdleTime) * time.Second).
		ReadTimeout(c.ReadTimeout).
		WriteTimeout(c.WriteTimeout).
		SSLMode(c.SSLMode).
		ClickhouseScheme(c.ClickhouseScheme)

	if c.Driver == ChDriver {
		return connector.BuildClickhouse()
	}

	return connector.BuildMigration()
}

// rawDSN returns the connection string built from DSN, if the config uses one.
func (c *Config) rawDSN() (string, bool) {
	if c.Driver == SQLiteDriver {
		return SQLiteDSN(c.DSN, c.SQLite), true
	}

	if c.DSN != "" {
		return c.DSN, true
	}

	return "", false
}
