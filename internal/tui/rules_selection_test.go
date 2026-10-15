package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

func TestRulesReorderKeepsSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		names      []string
		filter     string
		cursor     int
		key        rune
		wantIndex  int
		wantWrites int
		failWrite  bool
		navigate   bool
	}{
		{name: "down past hidden rule", filter: "Work", key: 'J', wantIndex: 1, wantWrites: 1},
		{name: "up past hidden rule", filter: "Work", cursor: 1, key: 'K', wantIndex: 1, wantWrites: 1},
		{name: "down without filter", key: 'J', wantIndex: 1, wantWrites: 1},
		{name: "up to first position", cursor: 1, key: 'K', wantIndex: 0, wantWrites: 1},
		{name: "cursor moved while pending", key: 'J', wantIndex: 1, wantWrites: 1, navigate: true},
		{name: "duplicate names", names: []string{"Work", "Personal", "Work"}, filter: "Work", key: 'J', wantIndex: 1, wantWrites: 1},
		{name: "unnamed rules", names: []string{"", "", ""}, key: 'J', wantIndex: 1, wantWrites: 1},
		{name: "already first", filter: "Work", key: 'K', wantIndex: 0},
		{name: "already last", filter: "Work", cursor: 1, key: 'J', wantIndex: 2},
		{name: "failed move", filter: "Work", key: 'J', wantIndex: 0, wantWrites: 1, failWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := tc.names
			if names == nil {
				names = []string{"Work A", "Personal", "Work C"}
			}
			var mu sync.Mutex
			rules := make([]coreapi.FilterRule, len(names))
			for i, name := range names {
				rules[i] = coreapi.FilterRule{
					Name: name, Match: "all",
					Conditions: []coreapi.RuleCondition{{Field: "subject", Op: "contains", Value: "hello"}},
					Actions:    []coreapi.RuleAction{{Type: "fileInto", Label: fmt.Sprintf("Folder %d", i)}},
				}
			}
			writes := 0
			client, closeServer := credClient(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPut {
					writes++
					if tc.failWrite {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"error":"conflict"}`))
						return
					}
					var doc coreapi.RulesDocument
					if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
						t.Errorf("decode rules: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					rules = doc.Rules
				}
				_ = json.NewEncoder(w).Encode(coreapi.FilterRulesState{Rules: rules, State: "active"})
			})
			defer closeServer()
			s := newScreenPane(t.Context(), &Options{Client: client}, rulesDesc(rulesMbx()))
			s.setSize(100, 30)
			s.update(s.fetchCmd("", true)())
			s.filter.SetValue(tc.filter)
			s.applyFilter()
			s.tbl.SetCursor(tc.cursor)
			original := s.visible[s.tbl.Cursor()].item.(ruleRow)

			_, move := s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
			if move == nil {
				t.Fatal("reorder command missing")
			}
			if tc.navigate {
				s.tbl.SetCursor(len(s.visible) - 1)
			}
			_, refresh := s.update(move())
			for _, msg := range collectMsgs(refresh) {
				if page, ok := msg.(pageMsg); ok {
					s.update(page)
				}
			}
			if (s.errMsg != "") != tc.failWrite {
				t.Fatalf("error = %q, want failure %v", s.errMsg, tc.failWrite)
			}
			selected := s.visible[s.tbl.Cursor()].item.(ruleRow)
			if selected.index != tc.wantIndex || selected.rule.Actions[0].Label != original.rule.Actions[0].Label {
				t.Fatalf("selected %q at index %d; want original rule %q at index %d", selected.rule.Name, selected.index, original.rule.Name, tc.wantIndex)
			}
			mu.Lock()
			gotWrites := writes
			mu.Unlock()
			if gotWrites != tc.wantWrites {
				t.Fatalf("writes = %d, want %d", gotWrites, tc.wantWrites)
			}

			// Restoring the reordered rule must not pin it on later refreshes.
			s.tbl.SetCursor((s.tbl.Cursor() + 1) % len(s.visible))
			selected = s.visible[s.tbl.Cursor()].item.(ruleRow)
			_, refresh = s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			for _, msg := range collectMsgs(refresh) {
				if page, ok := msg.(pageMsg); ok {
					s.update(page)
				}
			}
			if got := s.visible[s.tbl.Cursor()].item.(ruleRow); got.index != selected.index {
				t.Fatalf("later refresh selected index %d, want %d", got.index, selected.index)
			}
		})
	}
}

// A second J before the first move's refresh lands carried the stale row
// index, so it swapped whatever rule now sat there: A,J,J left A where it
// started and selected B, while the flash said "rule moved". Direct-run
// actions now wait for the previous one and its refresh.
func TestRulesDirectActionRefusedWhileInFlight(t *testing.T) {
	var mu sync.Mutex
	rules := []coreapi.FilterRule{}
	for _, n := range []string{"A", "B", "C"} {
		rules = append(rules, coreapi.FilterRule{Name: n, Match: "all",
			Conditions: []coreapi.RuleCondition{{Field: "subject", Op: "contains", Value: "x"}},
			Actions:    []coreapi.RuleAction{{Type: "discard"}}})
	}
	client, closeFn := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			var doc coreapi.RulesDocument
			_ = json.NewDecoder(r.Body).Decode(&doc)
			rules = doc.Rules
		}
		_ = json.NewEncoder(w).Encode(coreapi.FilterRulesState{Rules: rules, State: "active"})
	})
	defer closeFn()
	order := func() string {
		mu.Lock()
		defer mu.Unlock()
		var names []string
		for _, r := range rules {
			names = append(names, r.Name)
		}
		return fmt.Sprint(names)
	}

	s := newScreenPane(t.Context(), &Options{Client: client}, rulesDesc(rulesMbx()))
	s.setSize(100, 30)
	s.update(s.fetchCmd("", true)())
	J := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}}

	_, j1 := s.update(J)
	if _, j2 := s.update(J); j2 != nil {
		t.Fatal("a second J while the first move is in flight must be refused")
	}
	_, refresh := s.update(j1())
	if _, j3 := s.update(J); j3 != nil {
		t.Fatal("a J while the refresh after a move is in flight must be refused")
	}
	for _, msg := range collectMsgs(refresh) {
		if pg, ok := msg.(pageMsg); ok {
			s.update(pg)
		}
	}
	if got := order(); got != "[B A C]" {
		t.Fatalf("server order after one accepted J = %s; want [B A C]", got)
	}
	if got := s.visible[s.tbl.Cursor()].item.(ruleRow).rule.Name; got != "A" {
		t.Fatalf("selected %q after the move; want A", got)
	}

	_, j4 := s.update(J)
	if j4 == nil {
		t.Fatal("J after the refresh landed must run")
	}
	s.update(j4())
	if got := order(); got != "[B C A]" {
		t.Fatalf("server order after the second J = %s; want [B C A]", got)
	}
}
