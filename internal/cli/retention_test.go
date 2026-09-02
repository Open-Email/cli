package cli

import (
	"strings"
	"testing"
)

// A window is a positive whole number of days, refused as a usage error
// BEFORE any credential is consulted — a typo must not become a round trip
// against the first automation on the platform that destroys live mail.
func TestRetentionSetRefusesNonNumbers(t *testing.T) {
	for _, scope := range []retentionScope{retentionMailbox, retentionAccount} {
		// A leading dash is cobra's to refuse (as an unknown flag) before this
		// code runs, so "-5" is not in the list — the parser is pinned on "0".
		for _, bad := range []string{"lots", "0", "2.5", ""} {
			a := &app{out: newPrinter(false, true)}
			cmd := newRetentionSetCmd(a, scope)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs([]string{bad})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "number of days") {
				t.Fatalf("%v set %q: err = %v; want a usage error naming a number of days", scope, bad, err)
			}
		}
	}
}

// `--days` is core's preview ladder: comma-separated positive integers, at
// most eight — the same bound core applies, refused here without a round trip.
func TestParsePreviewDays(t *testing.T) {
	got, err := parsePreviewDays(" 30, 90,365 ")
	if err != nil || len(got) != 3 || got[0] != 30 || got[1] != 90 || got[2] != 365 {
		t.Fatalf("ladder = %v, %v", got, err)
	}
	if got, err := parsePreviewDays(""); err != nil || got != nil {
		t.Fatalf("empty = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{"30,x", "0", "30,,90", "1,2,3,4,5,6,7,8,9"} {
		if _, err := parsePreviewDays(bad); err == nil {
			t.Fatalf("%q accepted; want a usage error", bad)
		}
	}
}
