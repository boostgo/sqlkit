package duplicate

import (
	"reflect"
	"testing"

	"github.com/lib/pq"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestParseDetail_CompositeKey(t *testing.T) {
	tests := []struct {
		name       string
		detail     string
		wantFields []string
		wantValue  string
		wantOK     bool
	}{
		{"single key, constraint name misleads", "Key (external_id)=(abc) already exists.", []string{"external_id"}, "abc", true},
		{"composite", "Key (tenant_id, external_id)=(1, abc) already exists.", []string{"tenant_id", "external_id"}, "1, abc", true},
		{"value with comma", "Key (name)=(Doe, John) already exists.", []string{"name"}, "Doe, John", true},
		{"value with parentheses", "Key (name)=(a (b)) already exists.", []string{"name"}, "a (b)", true},
		{"index expression", "Key (lower(name::text))=(acme) already exists.", []string{"lower(name::text)"}, "acme", true},
		{"garbage", "something else", nil, "", false},
		{"empty", "", nil, "", false},
		{"no value part", "Key (a) already exists.", nil, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, value, ok := ParseDetail(tt.detail)
			if ok != tt.wantOK || value != tt.wantValue || !reflect.DeepEqual(fields, tt.wantFields) {
				t.Fatalf("got %v %q %v; want %v %q %v", fields, value, ok, tt.wantFields, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func BenchmarkDuplicateHandle_PF2(b *testing.B) {
	err := &pq.Error{Code: "23505", Constraint: "partners_name_key", Detail: "Key (name)=(acme) already exists."}

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = Handle(err)
	}
}
