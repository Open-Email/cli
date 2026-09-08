package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
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

// The semantic route takes q, label, limit, cursor, since/before and snippet —
// and nothing else. A CLI that accepted --from here and dropped it would answer
// a DIFFERENT question than the one asked, which on a search is
// indistinguishable from a wrong answer. So the refusals are the behaviour
// worth pinning, and each one has to name a way forward.
func TestSemanticModeRefusesWhatItCannotHonour(t *testing.T) {
	// A stand-in for the real command's flag set: runSemanticSearch reads flags
	// through cmd.Flags().Changed, so what it needs is a command whose flags
	// have the same names and can be marked changed.
	newCmd := func(changed ...string) *cobra.Command {
		cmd := &cobra.Command{Use: "search"}
		for _, name := range []string{
			"from", "to", "cc", "subject", "body", "min-size", "max-size",
			"has-attachment", "unread", "flagged", "has-keyword", "not-keyword",
			"sort", "position", "total", "mode",
		} {
			cmd.Flags().String(name, "", "")
		}
		for _, name := range changed {
			if err := cmd.Flags().Set(name, "x"); err != nil {
				t.Fatalf("set %s: %v", name, err)
			}
		}
		return cmd
	}
	a := &app{out: &Printer{out: io.Discard, err: io.Discard}}

	for _, tc := range []struct {
		name    string
		changed []string
		mode    string
		query   string
		all     bool
		group   bool
		want    string
	}{
		{
			name:    "a structured filter is refused by name, with the alternative",
			changed: []string{"from"},
			mode:    "hybrid", query: "anything",
			want: "--from cannot be combined with --mode",
		},
		{
			name:    "several are listed together rather than one at a time",
			changed: []string{"from", "unread"},
			mode:    "hybrid", query: "anything",
			want: "--from, --unread",
		},
		{
			// Not a longer answer — the same short answer with a misleading
			// shape, since the route is bounded at 100 candidates.
			name: "--all is refused because there is no every-page to fetch",
			mode: "hybrid", query: "anything", all: true,
			want: "100 most relevant",
		},
		{
			name:  "--group-thread is refused: this route ranks messages",
			mode:  "hybrid",
			query: "anything", group: true,
			want: "--group-thread cannot be combined",
		},
		{
			name: "an empty query is refused with an example",
			mode: "hybrid",
			want: "--mode needs a query",
		},
		{
			name: "an unknown mode names the two that exist",
			mode: "sideways", query: "anything",
			want: "must be 'hybrid' or 'semantic'",
		},
		{
			// "lexical" is what someone types reaching for the default, and
			// sending it to the semantic route would be silently wrong.
			name: "--mode lexical points back at the plain search",
			mode: "lexical", query: "anything",
			want: "drop --mode entirely",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCmd(tc.changed...)
			err := runSemanticSearch(cmd, a, nil, "MBX", tc.query, "", 0, "", tc.mode, tc.all, tc.group, &searchFlags{})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

// An incomplete backfill is not an error and nothing in the results reveals
// it — they are simply the best of what has been embedded so far, which on a
// half-indexed mailbox can be a confidently-ranked list of the wrong messages.
func TestSemanticCoverageWarnsOnlyWhenIncomplete(t *testing.T) {
	var buf bytes.Buffer
	a := &app{out: &Printer{out: io.Discard, err: &buf}}

	printSemanticCoverage(a, &coreapi.SemanticSearchResult{Coverage: coreapi.SemanticCoverage{Complete: true}})
	if buf.Len() != 0 {
		t.Fatalf("a complete index should say nothing, got %q", buf.String())
	}

	printSemanticCoverage(a, &coreapi.SemanticSearchResult{Coverage: coreapi.SemanticCoverage{Complete: false}})
	if !strings.Contains(buf.String(), "still running") {
		t.Fatalf("an incomplete index must warn, got %q", buf.String())
	}
}
