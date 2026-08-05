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
// rendering must be unmissable when frozen and quiet when not — and it must say
// the SCOPE, since the reason to reach for it over the per-mailbox freeze is
// that it also covers mailboxes the tenant has not created yet.
func TestFmtAccountSendState(t *testing.T) {
	if got := fmtAccountSendState(false, false); got != "enabled" {
		t.Errorf("fmtAccountSendState(live) = %q; want %q", got, "enabled")
	}
	frozen := fmtAccountSendState(true, false)
	if !strings.Contains(frozen, "FROZEN") {
		t.Errorf("fmtAccountSendState(frozen) = %q; want it to shout FROZEN", frozen)
	}
	for _, want := range []string{"every mailbox", "every domain", "relay"} {
		if !strings.Contains(frozen, want) {
			t.Errorf("fmtAccountSendState(frozen) = %q; want it to mention %q", frozen, want)
		}
	}

	// The HOLD must read as a DIFFERENT state, and must say what happens to the
	// queued mail: an operator choosing between the two is choosing between
	// bouncing a tenant's mail and holding it, which is the whole distinction.
	paused := fmtAccountSendState(false, true)
	if !strings.Contains(paused, "PAUSED") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to shout PAUSED", paused)
	}
	if strings.Contains(paused, "FROZEN") {
		t.Errorf("fmtAccountSendState(paused) = %q; must not read as a freeze", paused)
	}
	if !strings.Contains(paused, "held") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to say the mail is held", paused)
	}

	// Both set resolves toward the FREEZE, matching what core actually does — so
	// the CLI can never describe behavior the tenant is not getting.
	if got := fmtAccountSendState(true, true); !strings.Contains(got, "FROZEN") {
		t.Errorf("fmtAccountSendState(both) = %q; want the permanent answer to win", got)
	}
}

// --pause and --resume are separate booleans for the same reason --freeze and
// --unfreeze are, and passing both must be a usage error rather than
// last-one-wins. Validated BEFORE authentication: contradictory flags are a
// usage error whatever credentials the caller holds.
func TestAccountUpdateRejectsBothPauseFlags(t *testing.T) {
	a := &app{out: newPrinter(false, true)}
	cmd := newAccountUpdateCmd(a)
	cmd.SetArgs([]string{"ACC1", "--pause", "--resume"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --pause --resume to be refused")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %v; want it to say the flags are mutually exclusive", err)
	}
}

// --freeze and --unfreeze are separate booleans rather than one
// --send-disabled=true|false precisely so a mistake cannot silently mean the
// opposite; passing both must therefore be an error, not a last-one-wins.
func TestAccountUpdateRejectsBothFreezeFlags(t *testing.T) {
	a := &app{out: newPrinter(false, true)}
	cmd := newAccountUpdateCmd(a)
	if err := cmd.Flags().Set("freeze", "true"); err != nil {
		t.Fatalf("set --freeze: %v", err)
	}
	if err := cmd.Flags().Set("unfreeze", "true"); err != nil {
		t.Fatalf("set --unfreeze: %v", err)
	}
	err := cmd.RunE(cmd, []string{"ACC_X"})
	if err == nil {
		t.Fatal("expected --freeze with --unfreeze to be refused")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q; want it to name the conflict", err.Error())
	}
}
