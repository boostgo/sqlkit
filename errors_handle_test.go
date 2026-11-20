package sqlkit

import (
	"errors"
	"testing"

	"github.com/lib/pq"
)

func TestDuplicate(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantDuplicate bool
		wantField     string
	}{
		{
			name: "unique_violation returns duplicate field",
			err: &pq.Error{
				Code:       "23505",
				Constraint: "partners_name_key",
				Detail:     "Key (name)=(acme) already exists.",
			},
			wantDuplicate: true,
			wantField:     "name",
		},
		{
			name: "foreign_key_violation is not treated as duplicate",
			err: &pq.Error{
				Code:       "23503",
				Constraint: "dict_api_sources_wave_id_fkey",
				Detail:     "Key (wave_id)=(42) is not present in table \"dict_deploy_waves\".",
			},
			wantDuplicate: false,
			wantField:     "",
		},
		{
			name: "not_null_violation is not treated as duplicate",
			err: &pq.Error{
				Code: "23502",
			},
			wantDuplicate: false,
			wantField:     "",
		},
		{
			name:          "non-pq error is not treated as duplicate",
			err:           errors.New("some other error"),
			wantDuplicate: false,
			wantField:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDuplicate, gotField := Duplicate(tt.err)
			if gotDuplicate != tt.wantDuplicate {
				t.Errorf("Duplicate() duplicate = %v, want %v", gotDuplicate, tt.wantDuplicate)
			}
			if gotField != tt.wantField {
				t.Errorf("Duplicate() field = %q, want %q", gotField, tt.wantField)
			}
		})
	}
}
