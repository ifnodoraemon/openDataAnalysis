package idgen

import (
	"regexp"
	"testing"
)

func TestNewProducesWideHexIdentifiers(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		id := New()
		if !pattern.MatchString(id) {
			t.Fatalf("identifier %q must be 16 lowercase hex characters", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("identifier %q was produced twice in 256 draws", id)
		}
		seen[id] = struct{}{}
	}
}

func TestPrefixedKeepsTheNamespace(t *testing.T) {
	t.Parallel()

	id := Prefixed("r_")
	if len(id) != 18 || id[:2] != "r_" {
		t.Fatalf("expected a namespaced identifier, got %q", id)
	}
}
