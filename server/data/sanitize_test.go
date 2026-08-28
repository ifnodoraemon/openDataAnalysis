package data

import "testing"

func TestValidateSQLIdent(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", "users", false},
		{"valid_with_underscore", "user_name", false},
		{"valid_with_numbers", "table1", false},
		{"empty", "", true},
		{"too_long", string(make([]byte, 129)), true},
		{"semicolon", "users; DROP TABLE foo", true},
		{"quote", `users"`, true},
		{"single_quote", "users'", true},
		{"space", "user name", true},
		{"dash", "user-name", true},
		{"dot", "schema.table", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.input == string(make([]byte, 129)) {
				for i := range tt.input {
					tt.input = tt.input[:i] + "a" + tt.input[i+1:]
				}
			}
			err := ValidateSQLIdent(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSQLIdent(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
