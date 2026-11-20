package sqlkit

import (
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

// newMockDB returns sqlx.DB over sqlmock; both are closed on cleanup.
func newMockDB(t *testing.T) (*sqlx.DB, sqlmock.Sqlmock) {
	t.Helper()

	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}

	db := sqlx.NewDb(raw, "sqlmock")
	t.Cleanup(func() { _ = db.Close() })

	return db, mock
}

// waitMet waits until all sqlmock expectations are fulfilled (needed for the driver's async rollback on ctx cancel).
func waitMet(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := mock.ExpectationsWereMet()
		if err == nil {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("expectations not met: %v", err)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// newDSNMock registers a sqlmock for dsn (pings are monitored). The throwaway *sql.DB is closed on cleanup.
func newDSNMock(t *testing.T, dsn string) sqlmock.Sqlmock {
	t.Helper()

	raw, mock, err := sqlmock.NewWithDSN(dsn, sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = raw.Close() })

	return mock
}

// writeFile writes content to path or fails the test.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
