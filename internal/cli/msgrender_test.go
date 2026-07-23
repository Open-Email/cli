package cli

import "testing"

// TestNormalizeFlagsLowercasesAndValidates guards that a correctly-spelled but
// miscased flag (e.g. "Seen") is accepted AND lowercased in place, so the wire
// value matches core's case-sensitive flag enum rather than passing the local
// check then failing server-side with 400 invalid_flag.
func TestNormalizeFlagsLowercasesAndValidates(t *testing.T) {
	in := []string{"Seen", "ANSWERED", "flagged"}
	if err := normalizeFlags(in); err != nil {
		t.Fatalf("normalizeFlags: %v", err)
	}
	want := []string{"seen", "answered", "flagged"}
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("in[%d] = %q, want %q (must lowercase in place)", i, in[i], want[i])
		}
	}
	if err := normalizeFlags([]string{"bogus"}); err == nil {
		t.Fatal("want an error for an unknown flag")
	}
}
