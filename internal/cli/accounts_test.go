package cli

import (
	"strings"
	"testing"
)

// --max-mailboxes and --send-*-per-day look like the same kind of flag and have
// OPPOSITE null semantics, which is exactly the trap this parser exists to
// avoid. On a send cap, null means "inherit the platform number" and 0 spells
// unlimited. Here null IS unlimited, and 0 is refused by core (the column is
// `.positive()`), so reusing parseSendCapFlag would turn `--max-mailboxes
// unlimited` into a 400 while the identical word works everywhere else.
func TestParseMaxMailboxesFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    *int64 // nil = JSON null = unlimited
		wantErr bool
	}{
		{in: "unlimited", want: nil},
		{in: "none", want: nil},
		{in: "", want: nil}, // an empty --flag= is the same ask
		{in: "UNLIMITED", want: nil},
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
			t.Errorf("parseMaxMailboxesFlag(%q) = %d; want nil (unlimited)", tc.in, *got)
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
