package cli

import (
	"strings"
	"testing"
)

func TestNewDeliveryIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newDeliveryID()
		if len(id) != 26 {
			t.Fatalf("ULID length: got %d (%q)", len(id), id)
		}
		for _, r := range id {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("non-Crockford char %q in %q", r, id)
			}
		}
		// Must never collide with core's reserved delivery-id prefixes or contain '#'.
		if strings.HasPrefix(id, "sent:") || strings.HasPrefix(id, "bounce:") || strings.Contains(id, "#") {
			t.Fatalf("reserved/invalid delivery id: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ULID at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}
