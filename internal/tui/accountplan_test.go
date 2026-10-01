package tui

import (
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
// nil is the platform default, 0 is unlimited, and on a cap those are opposite
// answers rather than synonyms.
func TestAccountPlanPatchCapStates(t *testing.T) {
	cur := baseAccount()
	cur.SendMsgsPerDay = i64(500)

	// 500 -> platform default (blank)
	p := planFromAccount(cur)
	p.msgsPerDay = ""
	patch, err := accountPlanPatch(cur, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := patch["sendMsgsPerDay"]
	if !ok {
		t.Fatal("clearing a cap to the platform default must be sent")
	}
	if ptr, _ := v.(*int64); ptr != nil {
		t.Fatalf("blank must mean null (platform default), got %v", *ptr)
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

// maxMailboxes has TWO states, not three: a positive number, or nil for the
// platform default (core reads it as MAX_MAILBOXES_DEFAULT). There is no
// unlimited value, so the word is refused rather than sent as null.
func TestAccountPlanPatchMailboxCapIsTwoState(t *testing.T) {
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
		t.Fatalf("blank max mailboxes must mean the platform default (null), got %v", patch["maxMailboxes"])
	}

	for _, bad := range []string{"unlimited", "0"} {
		p.maxMailboxes = bad
		if _, err := accountPlanPatch(cur, p); err == nil {
			t.Fatalf("max mailboxes %q must be refused: core has no unlimited value and 0 is a 400", bad)
		}
	}

	// And an account on the platform default seeds blank, so it round-trips.
	cur.MaxMailboxes = nil
	if got := planFromAccount(cur).maxMailboxes; got != "" {
		t.Fatalf("the platform default must seed blank, got %q", got)
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
		t.Fatalf("blank must send null (platform default), got %v", patch)
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
