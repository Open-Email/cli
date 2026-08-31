package cli

import "testing"

// The send-allowance cap has THREE states where a quota has two, and the flag
// parser is the only place the third one is expressible. Getting it wrong is
// not a cosmetic bug: reading "inherit the platform default" as "unlimited"
// reports a bounded mailbox as unbounded, and does it in the direction that
// removes a limit rather than adding one.
func TestParseSendCapFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    *int64 // nil = clear the override
		wantErr bool
	}{
		{in: "default", want: nil},
		{in: "", want: nil}, // an empty --flag= is the same ask
		{in: "DEFAULT", want: nil},
		{in: "unlimited", want: ptr(0)},
		{in: "none", want: ptr(0)},
		{in: "0", want: ptr(0)}, // the wire form of unlimited, spelled directly
		{in: "250", want: ptr(250)},
		{in: " 250 ", want: ptr(250)},
		{in: "-1", wantErr: true},
		{in: "many", wantErr: true},
		{in: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseSendCapFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSendCapFlag(%q): expected an error, got %v", tc.in, deref64(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSendCapFlag(%q): unexpected error %v", tc.in, err)
			continue
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseSendCapFlag(%q) = %d; want nil (clear the override)", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseSendCapFlag(%q) = nil; want %d", tc.in, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("parseSendCapFlag(%q) = %d; want %d", tc.in, *got, *tc.want)
		}
	}
}

// The rendering side of the same three states. `nil` must never read as
// "unlimited" — that is the misreading the pointer type exists to prevent.
func TestFmtSendCap(t *testing.T) {
	if got := fmtSendCap(nil); got != "platform default" {
		t.Errorf("fmtSendCap(nil) = %q; want %q", got, "platform default")
	}
	if got := fmtSendCap(ptr(0)); got != "unlimited" {
		t.Errorf("fmtSendCap(0) = %q; want %q", got, "unlimited")
	}
	if got := fmtSendCap(ptr(250)); got != "250" {
		t.Errorf("fmtSendCap(250) = %q; want %q", got, "250")
	}
}

// send-usage reports limits ALREADY resolved to what is in force, so null there
// means a different thing than it does on a mailbox row: not "inherit", but
// "nothing is enforcing this axis".
func TestFmtSendLimit(t *testing.T) {
	if got := fmtSendLimit(nil); got != "not enforced" {
		t.Errorf("fmtSendLimit(nil) = %q; want %q", got, "not enforced")
	}
	if got := fmtSendLimit(ptr(500)); got != "500" {
		t.Errorf("fmtSendLimit(500) = %q; want %q", got, "500")
	}
}

func TestFmtSendState(t *testing.T) {
	// The disabled string must be unmistakable in a table an operator scans
	// during an incident; the enabled one must not shout.
	if got := fmtSendState(nil); got != "enabled" {
		t.Errorf("fmtSendState(live) = %q; want %q", got, "enabled")
	}
	if got := fmtSendState(strPtr("disabled")); got == "enabled" || got == "" {
		t.Errorf("fmtSendState(disabled) = %q; want a distinct disabled state", got)
	}
	// A HOLD is its own state, not a synonym for the freeze: one bounces queued
	// mail and the other keeps it, so a reader must be able to tell them apart.
	paused := fmtSendState(strPtr("paused"))
	if paused == "enabled" || paused == fmtSendState(strPtr("disabled")) {
		t.Errorf("fmtSendState(paused) = %q; want a state distinct from both enabled and disabled", paused)
	}
}

func ptr(n int64) *int64 { return &n }

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
