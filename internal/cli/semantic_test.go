package cli

import (
	"strings"
	"testing"
	"time"
)

// The floor is the one semantic flag with a parser, and the parser is where
// the two states core distinguishes can be lost. "all" must be 0 and never
// nil: core reads nil as "semantic search is off" and 0 as "indexed from the
// beginning of time", so folding them would ask for the opposite of what was
// typed on the one flag whose entire purpose is reaching further back.
func TestParseSemanticFloorFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want int64
	}{
		{"all is zero", "all", 0},
		{"everything is the same word", "everything", 0},
		{"an explicit zero stays zero", "0", 0},
		{"a bare date is midnight UTC", "2025-01-01", 1735689600},
		{"unix seconds pass through", "1735689600", 1735689600},
		{"case and space do not matter", "  ALL  ", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSemanticFloorFlag(tc.in)
			if err != nil {
				t.Fatalf("parseSemanticFloorFlag(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseSemanticFloorFlag(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}

	// The relative forms are relative to NOW, so they are asserted as a window
	// rather than a value — wide enough that a slow machine cannot fail it,
	// narrow enough that the wrong unit does.
	for _, tc := range []struct {
		in   string
		back time.Duration
	}{
		{"400d", 400 * 24 * time.Hour},
		{"18m", 18 * 30 * 24 * time.Hour},
		{"2y", 2 * 365 * 24 * time.Hour},
	} {
		got, err := parseSemanticFloorFlag(tc.in)
		if err != nil {
			t.Fatalf("parseSemanticFloorFlag(%q): %v", tc.in, err)
		}
		want := time.Now().Add(-tc.back).Unix()
		if got < want-60 || got > want+60 {
			t.Fatalf("parseSemanticFloorFlag(%q) = %d, want within a minute of %d", tc.in, got, want)
		}
	}

	// A rejection has to name the vocabulary, because the flag accepts four
	// shapes and a bare "invalid" leaves the user guessing which one they got
	// wrong.
	for _, bad := range []string{"-1", "yesterday", "12w", "2025-13-01", ""} {
		_, err := parseSemanticFloorFlag(bad)
		if err == nil {
			t.Fatalf("parseSemanticFloorFlag(%q) should have failed", bad)
		}
		if !strings.Contains(err.Error(), "--semantic-floor") {
			t.Fatalf("parseSemanticFloorFlag(%q) error should name the flag, got %v", bad, err)
		}
	}
}

// The mailbox printer shows the floor beside the switch, because the switch
// alone does not say how much of the mailbox a meaning-based search reaches.
func TestFmtSemanticFloor(t *testing.T) {
	if got := fmtSemanticFloor(nil); got != "on" {
		t.Fatalf("nil floor = %q, want %q", got, "on")
	}
	zero := int64(0)
	if got := fmtSemanticFloor(&zero); got != "on, all mail indexed" {
		t.Fatalf("zero floor = %q", got)
	}
	when := int64(1735689600)
	if got := fmtSemanticFloor(&when); !strings.HasPrefix(got, "on, indexed from ") {
		t.Fatalf("dated floor = %q", got)
	}
}

// --semantic is explicit true|false on both commands, and deliberately not a
// bare boolean flag: on the mailbox the false direction DELETES the
// embeddings, so it must not be the direction you reach by omission, and on
// the wire absent means "leave alone" — a third state a bare flag cannot say.
func TestSemanticFlagIsExplicitTriState(t *testing.T) {
	for _, yes := range []string{"true", "yes", "on", "1"} {
		v, err := parseBoolFlag("--semantic", yes)
		if err != nil || !v {
			t.Fatalf("parseBoolFlag(%q) = %v, %v", yes, v, err)
		}
	}
	for _, no := range []string{"false", "no", "off", "0"} {
		v, err := parseBoolFlag("--semantic", no)
		if err != nil || v {
			t.Fatalf("parseBoolFlag(%q) = %v, %v", no, v, err)
		}
	}
	if _, err := parseBoolFlag("--semantic", ""); err == nil {
		t.Fatal("an empty --semantic should be refused, not read as false")
	}
}
