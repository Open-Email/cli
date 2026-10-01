package cli

import (
	"strings"
	"testing"
)

// --max-mailboxes has two states where the send caps have three. Null is the
// platform default (core reads it as MAX_MAILBOXES_DEFAULT) and there is NO
// unlimited value: core's column is `.positive()`, so 0 is a 400. The word
// 'unlimited' used to map to null here, which quietly set the platform default
// on an operator who asked for no cap; it is refused now, by name.
func TestParseMaxMailboxesFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    *int64 // nil = JSON null = the platform default
		wantErr bool
	}{
		{in: "default", want: nil},
		{in: "DEFAULT", want: nil},
		{in: "", want: nil}, // an empty --flag= is the same ask
		{in: "unlimited", wantErr: true},
		{in: "none", wantErr: true},
		{in: "5", want: ptr(5)},
		{in: " 5 ", want: ptr(5)},
		// 0 is NOT unlimited here — core rejects a non-positive cap, so
		// accepting it would produce a confident 400 at the far end.
		{in: "0", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "lots", wantErr: true},
		{in: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseMaxMailboxesFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseMaxMailboxesFlag(%q): expected an error, got %v", tc.in, deref64(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMaxMailboxesFlag(%q): unexpected error %v", tc.in, err)
			continue
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseMaxMailboxesFlag(%q) = %d; want nil (platform default)", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseMaxMailboxesFlag(%q) = nil; want %d", tc.in, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("parseMaxMailboxesFlag(%q) = %d; want %d", tc.in, *got, *tc.want)
		}
	}
}

// The account freeze is the widest abuse control the CLI exposes, so its
// rendering must be unmissable when disabled and quiet when not — and it must say
// the SCOPE, since the reason to reach for it over the per-mailbox freeze is
// that it also covers mailboxes the tenant has not created yet.
// nil is a finding about the 90-day retention window, not "never": the word
// an offboarding decision reads must not overclaim.
func TestFmtLastClient(t *testing.T) {
	if got := fmtLastClient(nil); got != "none in 90d" {
		t.Fatalf("nil = %q", got)
	}
	when := int64(1735689600)
	if got := fmtLastClient(&when); got != fmtEpoch(when) {
		t.Fatalf("set = %q", got)
	}
	// The identity spelling has to admit the second reading of an absence —
	// core omits the field for a grant holder — because the CLI cannot tell.
	if got := fmtLastClientIdentity(nil); !strings.Contains(got, "shared with you") || !strings.Contains(got, "90d") {
		t.Fatalf("identity nil = %q, should name both readings", got)
	}
}

func TestFmtAccountSendState(t *testing.T) {
	if got := fmtAccountSendState(nil); got != "enabled" {
		t.Errorf("fmtAccountSendState(live) = %q; want %q", got, "enabled")
	}
	disabled := fmtAccountSendState(strPtr("disabled"))
	if !strings.Contains(disabled, "DISABLED") {
		t.Errorf("fmtAccountSendState(disabled) = %q; want it to shout DISABLED", disabled)
	}
	for _, want := range []string{"every mailbox", "every domain", "relay"} {
		if !strings.Contains(disabled, want) {
			t.Errorf("fmtAccountSendState(disabled) = %q; want it to mention %q", disabled, want)
		}
	}

	// The HOLD must read as a DIFFERENT state, and must say what happens to the
	// queued mail: an operator choosing between the two is choosing between
	// bouncing a tenant's mail and holding it, which is the whole distinction.
	paused := fmtAccountSendState(strPtr("paused"))
	if !strings.Contains(paused, "PAUSED") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to shout PAUSED", paused)
	}
	if strings.Contains(paused, "DISABLED") {
		t.Errorf("fmtAccountSendState(paused) = %q; must not read as a freeze", paused)
	}
	if !strings.Contains(paused, "held") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to say the mail is held", paused)
	}

	// Both set resolves toward the FREEZE, matching what core actually does — so
	// the CLI can never describe behavior the tenant is not getting.
	if got := fmtAccountSendState(strPtr("disabled")); !strings.Contains(got, "DISABLED") {
		t.Errorf("fmtAccountSendState(both) = %q; want the permanent answer to win", got)
	}
}

// The hold verbs moved to `admin hold`/`admin release`; exactly one mode is
// required there, and the usage error fires BEFORE authentication, whatever
// credentials the caller holds.
func TestAdminHoldRequiresExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{
		{"account", "ACC1", "--pause", "--stop"},
		{"account", "ACC1"},
	} {
		cmd := newAdminHoldCmd(&app{out: newPrinter(false, true)})
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%v: error = %v; want the exactly-one usage error", args, err)
		}
	}
}
