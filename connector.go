package sqlkit

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Connector builder for creating connection
type Connector struct {
	host             string
	port             int
	username         string
	password         string
	database         string
	schema           string
	sslMode          string
	clickhouseScheme string
	binaryParameters bool
	writeTimeout     int
	readTimeout      int

	migrationHost     string
	migrationPort     int
	migrationUsername string
	migrationPassword string
	migrationDatabase string

	timeout time.Duration

	maxOpenConnections int
	maxIdleConnections int
	maxConnLifetime    time.Duration
	maxIdleTime        time.Duration
}

// Pool defaults. Lifetime is below the idle timeout of the load balancer; connections are not
// re-created every few seconds.
const (
	defaultMaxOpenConnections = 10
	defaultMaxIdleConnections = 10
	defaultMaxConnLifetime    = 30 * time.Minute
	defaultMaxIdleTime        = 5 * time.Minute
)

// NewConnector creates Connector builder
func NewConnector() *Connector {
	return &Connector{
		maxOpenConnections: defaultMaxOpenConnections,
		maxIdleConnections: defaultMaxIdleConnections,
		maxConnLifetime:    defaultMaxConnLifetime,
		maxIdleTime:        defaultMaxIdleTime,
	}
}

// Host set host of database
func (connector *Connector) Host(host string) *Connector {
	connector.host = host
	return connector
}

// MigrationHost set host of database for migration
func (connector *Connector) MigrationHost(host string) *Connector {
	connector.migrationHost = host
	return connector
}

// Port set port of database
func (connector *Connector) Port(port int) *Connector {
	connector.port = port
	return connector
}

// MigrationPort set port of database for migration
func (connector *Connector) MigrationPort(port int) *Connector {
	connector.migrationPort = port
	return connector
}

// Username set username of database user
func (connector *Connector) Username(username string) *Connector {
	connector.username = username
	return connector
}

// MigrationUsername set username of database user for migration
func (connector *Connector) MigrationUsername(username string) *Connector {
	connector.migrationUsername = username
	return connector
}

// Password set password of database user
func (connector *Connector) Password(password string) *Connector {
	connector.password = password
	return connector
}

// MigrationPassword set password of database user for migration
func (connector *Connector) MigrationPassword(password string) *Connector {
	connector.migrationPassword = password
	return connector
}

// Database set database name
func (connector *Connector) Database(database string) *Connector {
	connector.database = database
	return connector
}

// MigrationDatabase set database name for migration
func (connector *Connector) MigrationDatabase(database string) *Connector {
	connector.migrationDatabase = database
	return connector
}

// SSLMode sets the sslmode of the connection string (empty means "disable").
func (connector *Connector) SSLMode(mode string) *Connector {
	connector.sslMode = mode
	return connector
}

// ClickhouseScheme sets the URL scheme for ClickHouse ("http"/"https").
//
// Empty means "http" if the environment variable LOCAL=true, otherwise "https".
func (connector *Connector) ClickhouseScheme(scheme string) *Connector {
	connector.clickhouseScheme = scheme
	return connector
}

// Schema set schema name
func (connector *Connector) Schema(schema string) *Connector {
	connector.schema = schema
	return connector
}

// BinaryParameters set binary_parameters=yes param
func (connector *Connector) BinaryParameters(binaryParameters bool) *Connector {
	connector.binaryParameters = binaryParameters
	return connector
}

// ReadTimeout set readTimeout parameter.
//
// For clickhouse
func (connector *Connector) ReadTimeout(readTimeout int) *Connector {
	connector.readTimeout = readTimeout
	return connector
}

// WriteTimeout set writeTimeout parameter.
//
// For clickhouse
func (connector *Connector) WriteTimeout(writeTimeout int) *Connector {
	connector.writeTimeout = writeTimeout
	return connector
}

// Timeout set timeout for connect & ping (via [context.Context])
func (connector *Connector) Timeout(timeout time.Duration) *Connector {
	connector.timeout = timeout
	return connector
}

// MaxOpenConnections set max open connections option for connection setting
func (connector *Connector) MaxOpenConnections(maxOpenConnections int) *Connector {
	connector.maxOpenConnections = maxOpenConnections
	return connector
}

// MaxIdleConnections set max idle connections option for connection setting
func (connector *Connector) MaxIdleConnections(maxIdleConnections int) *Connector {
	connector.maxIdleConnections = maxIdleConnections
	return connector
}

// MaxIdleTime set max idle time option for connection setting
func (connector *Connector) MaxIdleTime(maxIdleTime time.Duration) *Connector {
	connector.maxIdleTime = maxIdleTime
	return connector
}

// ConnectionMaxLifetime set max connection lifetime option for connection setting
func (connector *Connector) ConnectionMaxLifetime(connectionMaxLifetime time.Duration) *Connector {
	connector.maxConnLifetime = connectionMaxLifetime
	return connector
}

// Build connection string
func (connector *Connector) Build() string {
	var binaryParameters string
	if connector.binaryParameters {
		binaryParameters = " binary_parameters=yes"
	}

	var schema string
	if connector.schema != "" {
		schema = " search_path=" + quoteDSNValue(connector.schema)
	}

	return dsn(
		connector.sslMode,
		connector.host, connector.port,
		connector.username, connector.password,
		connector.database,
		binaryParameters,
		schema,
	)
}

// BuildMigration connection string
func (connector *Connector) BuildMigration() string {
	var binaryParameters string
	if connector.binaryParameters {
		binaryParameters = " binary_parameters=yes"
	}

	var schema string
	if connector.schema != "" {
		schema = " search_path=" + quoteDSNValue(connector.schema)
	}

	host := connector.host
	if connector.migrationHost != "" {
		host = connector.migrationHost
	}

	port := connector.port
	if connector.migrationPort != 0 {
		port = connector.migrationPort
	}

	username := connector.username
	if connector.migrationUsername != "" {
		username = connector.migrationUsername
	}

	password := connector.password
	if connector.migrationPassword != "" {
		password = connector.migrationPassword
	}

	database := connector.database
	if connector.migrationDatabase != "" {
		database = connector.migrationDatabase
	}

	return dsn(connector.sslMode, host, port, username, password, database, binaryParameters, schema)
}

// BuildClickhouse builds a ClickHouse URL. Login and password are escaped via url.UserPassword.
func (connector *Connector) BuildClickhouse() string {
	scheme := connector.clickhouseScheme
	if scheme == "" {
		scheme = "https"
		if os.Getenv("LOCAL") == "true" {
			scheme = "http"
		}
	}

	var query []string
	if connector.readTimeout > 0 {
		query = append(query, fmt.Sprintf("read_timeout=%ds", connector.readTimeout))
	}

	if connector.writeTimeout > 0 {
		query = append(query, fmt.Sprintf("write_timeout=%ds", connector.writeTimeout))
	}

	u := url.URL{
		Scheme:   scheme,
		User:     url.UserPassword(connector.username, connector.password),
		Host:     net.JoinHostPort(connector.host, strconv.Itoa(connector.port)),
		Path:     "/" + connector.database,
		RawQuery: strings.Join(query, "&"),
		// keep the trailing "?" as before
		ForceQuery: true,
	}

	return u.String()
}

// dsn builds a libpq key=value connection string with escaped values.
func dsn(sslMode, host string, port int, user, password, database, binaryParameters, schema string) string {
	if sslMode == "" {
		sslMode = "disable"
	}

	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s%s%s",
		quoteDSNValue(host), port,
		quoteDSNValue(user), quoteDSNValue(password),
		quoteDSNValue(database),
		quoteDSNValue(sslMode),
		binaryParameters,
		schema,
	)
}

// quoteDSNValue quotes a value of a libpq key=value connection string.
//
// An empty value or one with whitespace, ' or \ is wrapped in single quotes with \ and ' escaped;
// other values are returned as is.
func quoteDSNValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\r\v\f'\\") {
		return v
	}

	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)

	return "'" + r.Replace(v) + "'"
}

// String calls Build method
func (connector *Connector) String() string {
	return connector.Build()
}

// Connect calls Build method and call Connect function
func (connector *Connector) Connect(
	driverName string,
	options ...func(connection *sqlx.DB),
) (*sqlx.DB, error) {
	options = append(
		options,
		MaxConnectionsOption(connector.maxOpenConnections, connector.maxIdleConnections),
		MaxTimeOption(connector.maxConnLifetime, connector.maxIdleTime),
	)

	connectionString := connector.Build()
	if driverName == ChDriver {
		connectionString = connector.BuildClickhouse()
	}

	return Connect(
		driverName,
		connectionString,
		connector.timeout,
		options...,
	)
}

// ConnectMigration calls BuildMigration method and call Connect function
func (connector *Connector) ConnectMigration(
	driverName string,
	options ...func(connection *sqlx.DB),
) (*sqlx.DB, error) {
	options = append(
		options,
		MaxConnectionsOption(connector.maxOpenConnections, connector.maxIdleConnections),
		MaxTimeOption(connector.maxConnLifetime, connector.maxIdleTime),
	)

	connectionString := connector.BuildMigration()
	if driverName == ChDriver {
		connectionString = connector.BuildClickhouse()
	}

	return Connect(
		driverName,
		connectionString,
		connector.timeout,
		options...,
	)
}

// ConnectionString returns built connection string by provided params for sqlx lib
func ConnectionString(
	host string,
	port int,
	username, password string,
	database string,
	binaryParameters bool,
) string {
	return NewConnector().
		Host(host).
		Port(port).
		Username(username).
		Password(password).
		Database(database).
		BinaryParameters(binaryParameters).
		String()
}

// MaxConnectionsOption sets max open & idle connections
func MaxConnectionsOption(open, idle int) func(conn *sqlx.DB) {
	return func(conn *sqlx.DB) {
		if open == 0 || idle == 0 {
			return
		}

		conn.SetMaxOpenConns(open)
		conn.SetMaxIdleConns(idle)
	}
}

// MaxTimeOption sets connection max lifetime & max idle time settings
func MaxTimeOption(lifetime, idle time.Duration) func(conn *sqlx.DB) {
	return func(conn *sqlx.DB) {
		if lifetime == 0 || idle == 0 {
			return
		}

		conn.SetConnMaxLifetime(lifetime)
		conn.SetConnMaxIdleTime(idle)
	}
}

// WithMaxOpen sets the max number of open connections as is (database/sql semantics: <= 0 means no limit).
func WithMaxOpen(n int) func(*sqlx.DB) {
	return func(conn *sqlx.DB) { conn.SetMaxOpenConns(n) }
}

// WithMaxIdle sets the max number of idle connections as is (<= 0 means no idle connections).
func WithMaxIdle(n int) func(*sqlx.DB) {
	return func(conn *sqlx.DB) { conn.SetMaxIdleConns(n) }
}

// WithConnMaxLifetime sets the max connection lifetime as is (<= 0 means no limit).
func WithConnMaxLifetime(d time.Duration) func(*sqlx.DB) {
	return func(conn *sqlx.DB) { conn.SetConnMaxLifetime(d) }
}

// WithConnMaxIdleTime sets the max connection idle time as is (<= 0 means no limit).
func WithConnMaxIdleTime(d time.Duration) func(*sqlx.DB) {
	return func(conn *sqlx.DB) { conn.SetConnMaxIdleTime(d) }
}
