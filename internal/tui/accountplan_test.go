package tui

import (
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

// maxMailboxes has TWO states, not three — nil means unlimited, never "platform
// default". Reusing the cap vocabulary here would invert the meaning on the one
// knob where being wrong creates mailboxes.
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
		t.Fatalf("blank max mailboxes must mean unlimited (null), got %v", patch["maxMailboxes"])
	}

	// And an unlimited account seeds blank, so it round-trips.
	cur.MaxMailboxes = nil
	if got := planFromAccount(cur).maxMailboxes; got != "" {
		t.Fatalf("unlimited must seed blank, got %q", got)
	}
}

// The sending select collapses an ordered pair. Core resolves a row carrying
// both toward the freeze, so the form must show "stopped" for it — and choosing
// stopped must clear the now-meaningless hold rather than leave a state no
// screen renders honestly.
func TestAccountPlanSendingIsOrdered(t *testing.T) {
	both := baseAccount()
	both.SendDisabled, both.SendPaused = true, true
	if got := planFromAccount(both).sending; got != sendingStopped {
		t.Fatalf("disabled must outrank paused, got %q", got)
	}
	patch, err := accountPlanPatch(both, planFromAccount(both))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := patch["sendPaused"]; !ok || v != false {
		t.Fatalf("choosing stopped must clear the redundant hold, got %v", patch)
	}
	if _, ok := patch["sendDisabled"]; ok {
		t.Fatalf("sendDisabled was already true and must not be resent, got %v", patch)
	}

	paused := baseAccount()
	paused.SendPaused = true
	p := planFromAccount(paused)
	p.sending = sendingEnabled
	patch, err = accountPlanPatch(paused, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := patch["sendPaused"]; !ok || v != false {
		t.Fatalf("resuming must clear the hold, got %v", patch)
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
		"sendDisabled":   true,
	})
	if got != "sending, vanity hostnames, messages/day" {
		t.Fatalf("want stops first, got %q", got)
	}
	// Both send fields in one patch must still read as one thing.
	got = planChangeSummary(map[string]any{"sendDisabled": true, "sendPaused": false})
	if got != "sending" {
		t.Fatalf("want a single 'sending', got %q", got)
	}
}
