package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func i64(n int64) *int64 { return &n }

func baseAccount() coreapi.Account {
	return coreapi.Account{
		ID:   "ACC1",
		Name: "Acme",
	}
}

// The form must send ONLY what moved. A full-form submit is last-write-wins on
// every column, so it would revert whatever another operator changed while this
// form sat open.
func TestAccountPlanPatchSendsOnlyChanges(t *testing.T) {
	cur := baseAccount()
	p := planFromAccount(cur)
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patch) != 0 {
		t.Fatalf("round-tripping an untouched account must produce an empty patch, got %v", patch)
	}

	p.vanityHosts = true
	patch, err = accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patch) != 1 || patch["vanityHosts"] != true {
		t.Fatalf("want only vanityHosts, got %v", patch)
	}
}

// The three cap states are distinct and none of them may collapse into another:
// nil follows the account's plan, 0 is unlimited, and on a cap those are
// opposite answers rather than synonyms.
func TestAccountPlanPatchCapStates(t *testing.T) {
	cur := baseAccount()
	cur.SendMsgsPerDay = i64(500)

	// 500 -> follow the account's plan (blank)
	p := planFromAccount(cur)
	p.msgsPerDay = ""
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := patch["sendMsgsPerDay"]
	if !ok {
		t.Fatal("handing a cap back to the plan must be sent")
	}
	if ptr, _ := v.(*int64); ptr != nil {
		t.Fatalf("blank must mean null (follow the plan), got %v", *ptr)
	}

	// 500 -> unlimited (0), which is NOT the same as the default above
	p = planFromAccount(cur)
	p.msgsPerDay = "unlimited"
	patch, err = accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ptr, _ := patch["sendMsgsPerDay"].(*int64)
	if ptr == nil || *ptr != 0 {
		t.Fatalf("'unlimited' must mean 0, got %v", patch["sendMsgsPerDay"])
	}

	// An unlimited cap seeded back into the form must round-trip to no change.
	cur.SendMsgsPerDay = i64(0)
	patch, err = accountPlanPatch(cur, planFromAccount(cur))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patch) != 0 {
		t.Fatalf("an unlimited cap must round-trip clean, got %v", patch)
	}
}

// maxMailboxes has the caps' THREE states (core migration 0157): a number, 0
// for no limit, or nil to follow the account's plan. It used to have two, and
// refused 0 and 'unlimited' — which broke every plan edit on a Free or Pro
// account: the migration leaves them at 0, the form seeded "0", and then
// refused its own seed whatever field the operator had actually changed.
func TestAccountPlanPatchMailboxCapIsThreeState(t *testing.T) {
	cur := baseAccount()
	cur.MaxMailboxes = i64(50)
	p := planFromAccount(cur)
	if p.maxMailboxes != "50" {
		t.Fatalf("want the number seeded, got %q", p.maxMailboxes)
	}
	p.maxMailboxes = ""
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ptr, ok := patch["maxMailboxes"].(*int64)
	if !ok || ptr != nil {
		t.Fatalf("blank max mailboxes must mean null (follow the plan), got %v", patch["maxMailboxes"])
	}

	// No limit is 0 on the wire however it is spelled — never null, which
	// would hand the ceiling to the plan instead.
	for _, word := range []string{"unlimited", "none", "0"} {
		p.maxMailboxes = word
		patch, err := accountPlanPatch(cur, p)
		if err != nil {
			t.Fatalf("max mailboxes %q: unexpected error: %v", word, err)
		}
		if ptr, _ := patch["maxMailboxes"].(*int64); ptr == nil || *ptr != 0 {
			t.Fatalf("max mailboxes %q must send 0 (no limit), got %v", word, patch["maxMailboxes"])
		}
	}

	// A Free or Pro account's 0 seeds "unlimited" and round-trips clean, so
	// an edit to any OTHER field goes through and leaves the ceiling alone.
	cur.MaxMailboxes = i64(0)
	p = planFromAccount(cur)
	if p.maxMailboxes != "unlimited" {
		t.Fatalf("a 0 ceiling must seed 'unlimited', got %q", p.maxMailboxes)
	}
	p.msgsPerDay = "100"
	patch, err = accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("an edit on an account with no mailbox limit must not fail: %v", err)
	}
	if _, ok := patch["maxMailboxes"]; ok || len(patch) != 1 {
		t.Fatalf("want only sendMsgsPerDay, got %v", patch)
	}

	// And a column with no number (only a purged account's row) seeds blank,
	// so it round-trips too.
	cur.MaxMailboxes = nil
	if got := planFromAccount(cur).maxMailboxes; got != "" {
		t.Fatalf("a null ceiling must seed blank, got %q", got)
	}
	if patch, err := accountPlanPatch(cur, planFromAccount(cur)); err != nil || len(patch) != 0 {
		t.Fatalf("an untouched null ceiling must round-trip clean, got %v (%v)", patch, err)
	}
}

// parsePlanCap is the one vocabulary every cap in the plan form reads, and the
// new-account form's max mailboxes with it: blank or 'default' follows the
// account's plan (nil), 'unlimited' or 'none' is 0, a non-negative number is
// itself. A bad value names the field and says what blank means.
func TestParsePlanCap(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    *int64 // nil = JSON null = follow the account's plan
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "  ", want: nil},
		{in: "default", want: nil},
		{in: "DEFAULT", want: nil},
		{in: "unlimited", want: i64(0)},
		{in: "None", want: i64(0)},
		{in: "0", want: i64(0)}, // the wire form of no limit, spelled directly
		{in: "50", want: i64(50)},
		{in: " 50 ", want: i64(50)},
		{in: "-1", wantErr: true},
		{in: "lots", wantErr: true},
		{in: "1.5", wantErr: true},
	} {
		got, err := parsePlanCap("max mailboxes", tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePlanCap(%q): expected an error, got %q", tc.in, planCapText(got))
			} else if msg := err.Error(); !strings.Contains(msg, "max mailboxes") || !strings.Contains(msg, "account's plan") {
				t.Errorf("parsePlanCap(%q): error %q must name the field and say blank follows the account's plan", tc.in, msg)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePlanCap(%q): unexpected error %v", tc.in, err)
			continue
		}
		if !int64PtrEq(got, tc.want) {
			t.Errorf("parsePlanCap(%q) = %q; want %q", tc.in, planCapText(got), planCapText(tc.want))
		}
	}
}

// The sending select mirrors core's one `sendHold` field: seeded from it,
// re-sent only when it moves, and cleared with an explicit JSON null.
func TestAccountPlanSendingIsOrdered(t *testing.T) {
	disabled := "disabled"
	stopped := baseAccount()
	stopped.SendHold = &disabled
	if got := planFromAccount(stopped).sending; got != sendingStopped {
		t.Fatalf("a disabled hold must seed stopped, got %q", got)
	}
	patch, err := accountPlanPatch(stopped, planFromAccount(stopped))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := patch["sendHold"]; ok {
		t.Fatalf("an unchanged hold must not be resent, got %v", patch)
	}

	pausedHold := "paused"
	paused := baseAccount()
	paused.SendHold = &pausedHold
	p := planFromAccount(paused)
	p.sending = sendingEnabled
	patch, err = accountPlanPatch(paused, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := patch["sendHold"]; !ok || v != nil {
		t.Fatalf("resuming must send sendHold: null, got %v", patch)
	}
	p.sending = sendingStopped
	patch, err = accountPlanPatch(paused, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := patch["sendHold"]; !ok || v != "disabled" {
		t.Fatalf("escalating a hold must send sendHold: disabled, got %v", patch)
	}
}

func TestAccountPlanStorageAcceptsHumanSizes(t *testing.T) {
	cur := baseAccount()
	p := planFromAccount(cur)
	p.storage = "50G"
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ptr, _ := patch["storageLimitBytes"].(*int64)
	if ptr == nil || *ptr != 50<<30 {
		t.Fatalf("want 50 GiB in bytes, got %v", patch["storageLimitBytes"])
	}
	// And it renders back in the same vocabulary it accepts.
	cur.StorageLimitBytes = i64(50 << 30)
	if got := planFromAccount(cur).storage; got != "50.0 GB" {
		t.Fatalf("want a human size seeded back, got %q", got)
	}
}

func TestAccountPlanRejectsBadInput(t *testing.T) {
	cur := baseAccount()
	for _, tc := range []struct{ name, field, value string }{
		{"empty name", "name", "   "},
		{"negative cap", "msgs", "-1"},
		{"words in a cap", "msgs", "lots"},
		{"bad size", "storage", "50 bananas"},
		{"negative mailboxes", "maxmbx", "-3"},
		{"words in mailboxes", "maxmbx", "lots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planFromAccount(cur)
			switch tc.field {
			case "name":
				p.name = tc.value
			case "msgs":
				p.msgsPerDay = tc.value
			case "storage":
				p.storage = tc.value
			case "maxmbx":
				p.maxMailboxes = tc.value
			}
			if _, err := accountPlanPatch(cur, p); err == nil {
				t.Fatalf("want a validation error for %s=%q", tc.field, tc.value)
			}
		})
	}
}

// The flash names what moved, so an operator reads the edit rather than the
// fact that a request succeeded.
func TestPlanChangeSummary(t *testing.T) {
	got := planChangeSummary(map[string]any{
		"vanityHosts":    true,
		"sendMsgsPerDay": i64(500),
		"sendHold":       "disabled",
	})
	if got != "sending, vanity hostnames, messages/day" {
		t.Fatalf("want stops first, got %q", got)
	}
	// Both send fields in one patch must still read as one thing.
	got = planChangeSummary(map[string]any{"sendHold": "disabled"})
	if got != "sending" {
		t.Fatalf("want a single 'sending', got %q", got)
	}
}

// The create-velocity fields share the send caps' three states, are seeded from
// the account, and are sent only when they move.
func TestAccountPlanPatchCreateVelocity(t *testing.T) {
	cur := baseAccount()
	cur.MailboxCreatesPerDay = i64(5000)
	cur.AddressCreatesPerDay = i64(0)
	p := planFromAccount(cur)
	if p.mailboxCreates != "5000" || p.addressCreates != "unlimited" || p.domainCreates != "" {
		t.Fatalf("seeded %q / %q / %q; want 5000 / unlimited / blank", p.mailboxCreates, p.addressCreates, p.domainCreates)
	}
	if patch, err := accountPlanPatch(cur, p); err != nil || len(patch) != 0 {
		t.Fatalf("an untouched plan must round-trip clean, got %v (%v)", patch, err)
	}

	p.mailboxCreates = ""
	p.domainCreates = "20"
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := patch["mailboxCreatesPerDay"]; !ok || v.(*int64) != nil {
		t.Fatalf("blank must send null (follow the plan), got %v", patch)
	}
	if v, _ := patch["domainCreatesPerDay"].(*int64); v == nil || *v != 20 {
		t.Fatalf("want domainCreatesPerDay 20, got %v", patch["domainCreatesPerDay"])
	}
	if _, ok := patch["addressCreatesPerDay"]; ok || len(patch) != 2 {
		t.Fatalf("want only the two moved fields, got %v", patch)
	}
	if got := planChangeSummary(patch); got != "mailbox creates/day, domain creates/day" {
		t.Fatalf("summary = %q", got)
	}

	p.addressCreates = "-1"
	if _, err := accountPlanPatch(cur, p); err == nil || !strings.Contains(err.Error(), "address creates/day") {
		t.Fatalf("want an error naming address creates/day, got %v", err)
	}
}

// The accounts screen shows the mailbox ceiling in the same three states: 0 is
// no limit and reads "unlimited" (a bare "0" reads as a ceiling of nothing),
// and a column with no number — only a purged account's row — is a dash in the
// list and "not set" in the detail, never "unlimited".
func TestAccountsScreenShowsTheMailboxCapStates(t *testing.T) {
	for _, tc := range []struct {
		name, field, cell, detail string
	}{
		{"no limit", `"maxMailboxes":0`, "unlimited", "unlimited"},
		{"a number", `"maxMailboxes":25`, "25", "25"},
		{"no number", `"maxMailboxes":null`, "—", "not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"accounts":[{"id":"id1","name":"Acme",%s,"sendMsgsPerDay":0}]}`, tc.field)
			}))
			defer srv.Close()
			desc := accountsDesc()
			rows, _, err := desc.fetch(context.Background(), newTestClient(t, srv.URL), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].cells[2] != tc.cell {
				t.Fatalf("list = %+v, want max mailboxes %q", rows, tc.cell)
			}
			got := map[string]string{}
			for _, kv := range desc.detail(rows[0].item) {
				got[kv.k] = kv.v
			}
			if got["max mailboxes"] != tc.detail {
				t.Errorf("detail max mailboxes = %q, want %q", got["max mailboxes"], tc.detail)
			}
			if got["messages/day"] != "unlimited" || got["recipients/day"] != "not set" {
				t.Errorf("detail caps = %q / %q, want unlimited / not set", got["messages/day"], got["recipients/day"])
			}
		})
	}
}
