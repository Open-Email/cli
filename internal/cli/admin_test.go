package cli

import (
	"strings"
	"testing"
)

// `admin verify-login` is what an operator runs when a customer says "I can't
// send", so the two holds must not read alike: one is permanent (the sender's
// MTA bounces) and one is reversible (their MTA queues). A mode core adds later
// must still print, rather than vanishing into an empty cell.
func TestSendHoldLabel(t *testing.T) {
	disabled := sendHoldLabel("disabled")
	paused := sendHoldLabel("paused")

	if !strings.Contains(disabled, "permanent") || !strings.Contains(disabled, "550") {
		t.Fatalf("disabled does not read as a permanent stop: %q", disabled)
	}
	if !strings.Contains(paused, "451") || strings.Contains(paused, "permanent") {
		t.Fatalf("paused does not read as a reversible hold: %q", paused)
	}
	if disabled == paused {
		t.Fatal("the two holds render identically")
	}
	if got := sendHoldLabel("frozen_by_mars"); got != "frozen_by_mars" {
		t.Fatalf("unknown hold not passed through: %q", got)
	}
}

// `deliver check` is run by operators debugging an MTA, so the hint it prints
// when core refuses the gate has to point at a credential that actually works.
//
// Core refuses ONLY mailbox principals (src/endpoints/deliver/check.ts), so an
// account key runs the gate fine — scoped to its own domains, with foreign
// recipients collapsing to 404 rather than 403. The tempting shorthand "needs a
// system key" is therefore wrong in the expensive direction: it sends an
// operator after a credential they very likely cannot get, for a problem the
// key already in their hand solves. This asserts the CLAIM rather than the
// prose, so the wording stays free to change and the meaning does not.
func TestInsufficientScopeHint(t *testing.T) {
	hint := insufficientScopeHint()
	lower := strings.ToLower(hint)

	// It must name the credential that genuinely cannot run the gate...
	if !strings.Contains(lower, "mailbox") {
		t.Errorf("hint does not name the mailbox credential as the one that cannot: %q", hint)
	}
	// ...and the one that can, which is the actionable half.
	if !strings.Contains(lower, "account") {
		t.Errorf("hint does not offer an account key as a way forward: %q", hint)
	}
	// It must not claim a system key is REQUIRED. Offering it as one option is
	// fine; demanding it is the regression.
	for _, wrong := range []string{
		"needs a system key",
		"requires a system key",
		"a system key is required",
		"system key required",
		"only a system key",
		"must be a system key",
	} {
		if strings.Contains(lower, wrong) {
			t.Errorf("hint claims a system key is required (%q) — an account key runs this gate: %q", wrong, hint)
		}
	}
	// And it must say no verdict was reached, or the operator reads the refusal
	// as a recipient rejection — the exact conflation the branch exists to stop.
	if !strings.Contains(lower, "no recipient verdict") {
		t.Errorf("hint does not say the check produced no verdict: %q", hint)
	}
}
