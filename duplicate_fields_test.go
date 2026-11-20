package sqlkit

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

func TestDuplicateFields_PqAndPgx(t *testing.T) {
	const detail = "Key (tenant_id, external_id)=(1, a) already exists."

	tests := []struct {
		name   string
		err    error
		want   []string
		wantOK bool
	}{
		{"pq", &pq.Error{Code: "23505", Constraint: "campaign_external_id_key", Detail: detail}, []string{"tenant_id", "external_id"}, true},
		{"pgx", &pgconn.PgError{Code: "23505", Detail: detail}, []string{"tenant_id", "external_id"}, true},
		{"wrapped pgx", fmt.Errorf("x: %w", &pgconn.PgError{Code: "23505", Detail: detail}), []string{"tenant_id", "external_id"}, true},
		{"pq not unique", &pq.Error{Code: "23503", Detail: detail}, nil, false},
		{"pgx not unique", &pgconn.PgError{Code: "23503", Detail: detail}, nil, false},
		{"unparsable detail", &pgconn.PgError{Code: "23505", Detail: "garbage"}, nil, false},
		{"other error", errors.New("x"), nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DuplicateFields(tt.err)
			if ok != tt.wantOK || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
