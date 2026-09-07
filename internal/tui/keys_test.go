package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// The keys screen says what each key REACHES (core migration 0078) — the three
// wire cases rendered in the SCOPE column, and in full on the detail view.
// Without this column a one-domain delegate and the account-wide key were the
// same row, which is the misreading a scope must never invite.
func TestKeysScreenShowsTheDomainScope(t *testing.T) {
	c, closeSrv := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiKeys": []map[string]any{
				{"id": "k_wide", "name": "wide", "role": "account", "createdAt": 1},
				{"id": "k_scoped", "name": "scoped", "role": "account", "createdAt": 1, "domains": []string{"a.test", "b.test", "c.test"}},
				// A stored scope core could not read: the key refuses to
				// authenticate, and the listing says so with `[]`.
				{"id": "k_dead", "name": "dead", "role": "account", "createdAt": 1, "domains": []string{}},
			},
		})
	})
	defer closeSrv()

	desc := keysDesc()
	scopeCol := -1
	for i, col := range desc.columns {
		if col.title == "SCOPE" {
			scopeCol = i
		}
	}
	if scopeCol < 0 {
		t.Fatal("keys screen has no SCOPE column")
	}
	rows, _, err := desc.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for i, want := range []string{"all", "a.test, b.test +1 more", "none"} {
		if got := rows[i].cells[scopeCol]; got != want {
			t.Errorf("row %d scope = %q, want %q", i, got, want)
		}
		if len(rows[i].cells) != len(desc.columns) {
			t.Errorf("row %d has %d cells for %d columns", i, len(rows[i].cells), len(desc.columns))
		}
	}

	// The detail line spells the whole scope out — the cell truncates.
	detail := desc.detail(rows[1].item)
	found := false
	for _, line := range detail {
		if line.k == "scope" {
			found = true
			if line.v != "a.test, b.test, c.test" {
				t.Fatalf("detail scope = %q", line.v)
			}
		}
	}
	if !found {
		t.Fatal("detail view has no scope line")
	}
}
