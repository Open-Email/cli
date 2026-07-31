package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func rulesMbx() coreapi.Mailbox {
	addr := "alice@x.test"
	return coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr}
}

const twoRulesJSON = `{"rules":[
	{"name":"Acme","match":"all","conditions":[{"field":"from","op":"contains","value":"@acme.com"}],"actions":[{"type":"fileInto","label":"Work"}],"stop":false},
	{"name":"Big","match":"any","conditions":[{"field":"size","op":"over","value":5242880}],"actions":[{"type":"discard"}],"enabled":false,"stop":true}
],"status":"active","activeScript":"openemail.rules","script":"# sieve","updatedAt":100}`

// The mailboxes screen drills into filters with F.
func TestMailboxFiltersActionOpensRules(t *testing.T) {
	var found *action
	desc := mailboxesDesc()
	for i := range desc.actions {
		if desc.actions[i].key == "F" {
			found = &desc.actions[i]
		}
	}
	if found == nil || !found.needsRow || found.run == nil {
		t.Fatal("mailboxes should have a row-bound F filters action")
	}
	p := found.run(context.Background(), &Options{}, rulesMbx())
	if p == nil || !contains(p.title(), "Filter rules") {
		t.Fatalf("F should open the rules screen, got %v", p)
	}
}

func TestRulesDescRows(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(twoRulesJSON))
	})
	defer done()
	d := rulesDesc(rulesMbx())
	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" || len(rows) != 2 {
		t.Fatalf("want 2 unpaginated rows, got %d (next %q)", len(rows), next)
	}
	// Position, name, state, match, and the summarized condition/action cells.
	if rows[0].cells[0] != "1" || rows[0].cells[1] != "Acme" || rows[0].cells[2] != "on" {
		t.Fatalf("row0 head wrong: %v", rows[0].cells)
	}
	if rows[0].cells[4] != "from contains @acme.com" || rows[0].cells[5] != "file into Work" {
		t.Fatalf("row0 summary wrong: %v", rows[0].cells)
	}
	// A disabled rule reads "off"; a JSON-number size renders as bytes.
	// fmtBytes here is the tui package's (base-1024, SI-style labels).
	if rows[1].cells[2] != "off" || rows[1].cells[4] != "size over 5.0 MB" || rows[1].cells[6] != "yes" {
		t.Fatalf("row1 wrong: %v", rows[1].cells)
	}
}

// The summary answers whether the rules actually filter mail — the three
// states must be distinguishable.
func TestRulesSummaryStates(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"active", twoRulesJSON, "ACTIVE"},
		{"displaced", `{"rules":[],"status":"inactive","activeScript":"mine","script":"","updatedAt":1}`, "NOT ACTIVE"},
		{"unfiltered", `{"rules":[],"status":"inactive","activeScript":null,"script":"","updatedAt":1}`, "no active filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, done := credClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(tc.body))
			})
			defer done()
			d := rulesDesc(rulesMbx())
			if lines := d.summary(120); len(lines) != 0 {
				t.Fatalf("summary before a fetch should be empty, got %v", lines)
			}
			if _, _, err := d.fetch(context.Background(), c, ""); err != nil {
				t.Fatalf("fetch: %v", err)
			}
			lines := d.summary(120)
			if len(lines) != 1 || !contains(lines[0], tc.want) {
				t.Fatalf("summary = %v, want one line containing %q", lines, tc.want)
			}
		})
	}
}

// A mailbox with no rules is an empty screen with a hint, never an error.
func TestRulesDescNotFoundIsEmpty(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	})
	defer done()
	d := rulesDesc(rulesMbx())
	rows, _, err := d.fetch(context.Background(), c, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("404 should be an empty listing, got %d rows / %v", len(rows), err)
	}
	if lines := d.summary(120); len(lines) != 1 || !contains(lines[0], "no filter rules yet") {
		t.Fatalf("summary = %v, want the getting-started hint", lines)
	}
}

// Toggle re-reads the document, flips only the addressed rule, and PUTs the
// whole list back.
func TestRulesToggleAction(t *testing.T) {
	var put coreapi.RulesDocument
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &put)
			w.Write([]byte(`{"rules":[],"status":"active","created":false,"script":""}`))
			return
		}
		w.Write([]byte(twoRulesJSON))
	})
	defer done()

	d := rulesDesc(rulesMbx())
	var toggle *action
	for i := range d.actions {
		if d.actions[i].key == "t" {
			toggle = &d.actions[i]
		}
	}
	if toggle == nil || toggle.do == nil {
		t.Fatal("rules screen needs a direct-run t action")
	}
	// Rule 1 is enabled → switching it off.
	flash, err := toggle.do(context.Background(), c, ruleRow{index: 0})
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if !contains(flash, "disabled") {
		t.Errorf("flash = %q, want it to say disabled", flash)
	}
	if len(put.Rules) != 2 {
		t.Fatalf("PUT should carry the whole document, got %d rules", len(put.Rules))
	}
	if put.Rules[0].IsEnabled() {
		t.Error("rule 1 should have been switched off")
	}
	if put.Rules[1].IsEnabled() {
		t.Error("rule 2 was already off and must stay off")
	}
}

// Reordering swaps neighbours; at an edge it is a no-op rather than an error.
func TestRulesReorder(t *testing.T) {
	var put coreapi.RulesDocument
	putCalls := 0
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalls++
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &put)
			w.Write([]byte(`{"rules":[],"status":"active","created":false,"script":""}`))
			return
		}
		w.Write([]byte(twoRulesJSON))
	})
	defer done()

	if _, err := reorderRule(context.Background(), c, "01M", ruleRow{index: 1}, -1); err != nil {
		t.Fatalf("move up: %v", err)
	}
	if len(put.Rules) != 2 || put.Rules[0].Name != "Big" || put.Rules[1].Name != "Acme" {
		t.Fatalf("rules not swapped: %+v", put.Rules)
	}
	// Moving the first rule up has nowhere to go: no write, no error.
	before := putCalls
	flash, err := reorderRule(context.Background(), c, "01M", ruleRow{index: 0}, -1)
	if err != nil || flash != "" {
		t.Fatalf("edge move should be a silent no-op, got %q / %v", flash, err)
	}
	if putCalls != before {
		t.Error("edge move must not write")
	}
}

// A stale row index (the document shrank under the screen) is refused with a
// refresh hint rather than corrupting the document.
func TestRulesStaleIndexRefused(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Error("a stale index must not write")
		}
		w.Write([]byte(`{"rules":[],"status":"active","activeScript":null,"script":"","updatedAt":1}`))
	})
	defer done()
	if _, err := reorderRule(context.Background(), c, "01M", ruleRow{index: 3}, -1); err == nil {
		t.Fatal("a stale index should be refused")
	}
}
