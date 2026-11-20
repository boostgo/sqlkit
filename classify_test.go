package sqlkit

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

func TestClassify_PqAndPgx(t *testing.T) {
	cases := []struct {
		code string
		want error
	}{
		{"23505", ErrDuplicate},
		{"23503", ErrForeignKeyViolation},
		{"23502", ErrNotNull},
	}

	for _, c := range cases {
		pqErr := &pq.Error{Code: pq.ErrorCode(c.code)}
		pgErr := &pgconn.PgError{Code: c.code}

		for name, orig := range map[string]error{"pq": pqErr, "pgx": pgErr} {
			got := Classify(fmt.Errorf("wrap: %w", orig))
			if !errors.Is(got, c.want) || !errors.Is(got, orig) {
				t.Errorf("%s %s: %v", name, c.code, got)
			}

			if again := Classify(got); again.Error() != got.Error() {
				t.Errorf("%s %s: double wrap: %v", name, c.code, again)
			}
		}
	}

	other := errors.New("network down")
	if got := Classify(other); got != other { //nolint:errorlint // identity is the point
		t.Fatalf("foreign error must be returned as is, got %v", got)
	}

	if got := Classify(&pgconn.PgError{Code: "40001"}); got == nil {
		t.Fatal("other pg code must not be nil")
	}

	if Classify(nil) != nil {
		t.Fatal("nil -> nil")
	}

	if !IsDuplicate(&pgconn.PgError{Code: "23505"}) || IsDuplicate(other) {
		t.Fatal("IsDuplicate")
	}

	if !IsForeignKey(&pq.Error{Code: "23503"}) || !IsNotNull(&pq.Error{Code: "23502"}) {
		t.Fatal("IsForeignKey/IsNotNull")
	}

	var pqTarget *pq.Error
	if !errors.As(Classify(&pq.Error{Code: "23505"}), &pqTarget) {
		t.Fatal("original error must stay in the chain")
	}
}

func TestIsNoRows_Wrapped(t *testing.T) {
	if !IsNoRows(fmt.Errorf("x: %w", sql.ErrNoRows)) || IsNoRows(errors.New("x")) {
		t.Fatal("IsNoRows")
	}
}

func TestHandleError_Unchanged(t *testing.T) {
	//nolint:staticcheck // SA1019: behavior of the deprecated function must stay
	if HandleError(errors.New("network")) != nil {
		t.Fatal("HandleError must still return nil for non-pq errors")
	}
}
