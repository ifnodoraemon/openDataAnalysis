package handler

import "testing"

func TestSourceScopedFileTableNameAvoidsCrossSourceCollision(t *testing.T) {
	t.Parallel()

	first := sourceScopedFileTableName("sales.csv", "ds_alpha_12345678")
	second := sourceScopedFileTableName("sales.csv", "ds_beta_87654321")
	if first == second {
		t.Fatalf("expected source-scoped table names to differ, both %q", first)
	}
	if first != "sales__12345678" || second != "sales__87654321" {
		t.Fatalf("unexpected scoped table names: %q %q", first, second)
	}
}
