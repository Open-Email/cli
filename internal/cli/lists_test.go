package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// --expires speaks the vocabulary the rest of the CLI already does (an age as
// credentials take it, an absolute as the PIM commands take it) and refuses a
// past instant locally, naming what it parsed, because core's own refusal is a
// round trip and a bare invalid_expiry.
func TestParseExpiresFlag(t *testing.T) {
	if got, err := parseExpiresFlag(""); err != nil || got != nil {
		t.Fatalf("empty must mean never (nil, nil), got %v, %v", got, err)
	}
	now := time.Now().Unix()
	for _, tc := range []struct {
		in   string
		from time.Duration
		to   time.Duration
	}{
		{"30d", 30*24*time.Hour - time.Minute, 30*24*time.Hour + time.Minute},
		{"12h", 12*time.Hour - time.Minute, 12*time.Hour + time.Minute},
	} {
		got, err := parseExpiresFlag(tc.in)
		if err != nil || got == nil {
			t.Fatalf("parseExpiresFlag(%q): %v, %v", tc.in, got, err)
		}
		if *got < now+int64(tc.from.Seconds()) || *got > now+int64(tc.to.Seconds()) {
			t.Fatalf("parseExpiresFlag(%q) = %d, want about now+%s", tc.in, *got, tc.to)
		}
	}
	future := now + 86400
	if got, err := parseExpiresFlag(strings.TrimSpace(time.Unix(future, 0).UTC().Format(time.RFC3339))); err != nil || got == nil || *got != future {
		t.Fatalf("RFC3339 round trip: %v, %v", got, err)
	}
	for _, bad := range []string{"2001-01-01", "0", "yesterday", "-3d", "0d"} {
		_, err := parseExpiresFlag(bad)
		if err == nil {
			t.Fatalf("parseExpiresFlag(%q) should have failed", bad)
		}
		if !strings.Contains(err.Error(), "--expires") {
			t.Fatalf("parseExpiresFlag(%q) error should name the flag, got %v", bad, err)
		}
	}
}

// One row shape for both entry listings. The three new columns are the ones
// that make a listing actionable: an entry with an expiry must not display as
// permanent, and an entry that has never decided anything is the one to prune.
func TestEntryCells(t *testing.T) {
	exp := int64(1893456000)
	hit := int64(1757000000)
	note := "vendor allow"
	got := entryCells(coreapi.AddressListEntry{
		Pattern: "*@acme.example", CreatedAt: 1735689600, Note: &note,
		ExpiresAt: &exp, HitCount: 1204, LastHitAt: &hit,
	}, 40)
	want := entryHeaders("PATTERN")
	if len(got) != len(want) {
		t.Fatalf("cells/headers disagree: %d cells for %d headers", len(got), len(want))
	}
	if got[2] != fmtEpoch(exp) {
		t.Fatalf("EXPIRES = %q, want the formatted expiry", got[2])
	}
	if got[3] != "1,204" {
		t.Fatalf("HITS = %q, want thousands-separated 1,204", got[3])
	}
	if got[4] != fmtEpoch(hit) {
		t.Fatalf("LAST HIT = %q", got[4])
	}
	if got[5] != note {
		t.Fatalf("NOTE = %q", got[5])
	}

	bare := entryCells(coreapi.AddressListEntry{Pattern: "x@y.example", CreatedAt: 1}, 40)
	if bare[2] != "—" || bare[4] != "—" || bare[3] != "0" {
		t.Fatalf("a permanent, never-matched entry should read — / 0 / —, got %v", bare[2:5])
	}
}
