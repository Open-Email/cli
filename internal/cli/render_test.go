package cli

import (
	"bytes"
	"strings"
	"testing"
)

// colStarts returns the visible column-start offset of each field in a rendered
// row, ignoring color escapes. Two rows align iff their offset slices match.
func colStarts(line string) []int {
	visible := []rune(ansiSeq.ReplaceAllString(line, ""))
	var starts []int
	inField := false
	for i := 0; i < len(visible); i++ {
		if visible[i] != ' ' {
			if !inField {
				starts = append(starts, i)
				inField = true
			}
			continue
		}
		// A lone internal space (e.g. inside a date) stays in-field; the
		// two-space gap ends a column.
		if i+1 < len(visible) && visible[i+1] == ' ' {
			inField = false
		}
	}
	return starts
}

func TestPrintTableAlignsWithColor(t *testing.T) {
	var buf bytes.Buffer
	// Force color on: this is the case text/tabwriter got wrong — the dimmed
	// header's invisible escape bytes were counted as width.
	p := &Printer{color: true, out: &buf, err: &buf}

	headers := []string{"ID", "FROM", "SUBJECT", "DATE", "FLAGS", "LABELS"}
	rows := [][]string{
		{"01KXRS3SHN1N35G4YETVADSN0R", "dejan.strbac@gmail.com", "t3est", "2026-07-17 15:33", "—", "INBOX"},
		{"01KXRRSN93GTW0T2SY0DTMT54F", "a@b.co", "hi", "2026-07-16 09:00", "\\Seen", "WORK"},
	}
	printTable(&buf, p, headers, rows)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("expected %d lines, got %d", len(rows)+1, len(lines))
	}
	// The header must actually be colored (regression guard: color path active).
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("header not colored: %q", lines[0])
	}
	want := colStarts(lines[0])
	if len(want) != len(headers) {
		t.Fatalf("header column count: got %d want %d (%q)", len(want), len(headers), lines[0])
	}
	for _, l := range lines[1:] {
		if got := colStarts(l); !equalInts(got, want) {
			t.Fatalf("column misalignment:\n header cols %v\n row    cols %v\n line %q", want, got, l)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSanitizeCellStripsEscapesAndControls(t *testing.T) {
	cases := map[string]string{
		"plain":                "plain",
		"\x1b[2Jclear":         "clear",   // CSI (non-SGR) escape removed
		"\x1b]0;title\x07oops": "oops",    // OSC (title) sequence removed
		"a\x1b[31mb\x1b[0mc":   "abc",     // SGR stripped from cell data too
		"tab\there":            "tabhere", // C0 control removed
		"line\nbreak":          "linebreak",
		"\x1bMreverse":         "reverse", // ESC-Fe removed
		"ünïcöde":              "ünïcöde", // multibyte runes preserved
	}
	for in, want := range cases {
		if got := sanitizeCell(in); got != want {
			t.Errorf("sanitizeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintTableSanitizesUntrustedCells(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{color: false, out: &buf, err: &buf}
	// A hostile decoded Subject from inbound mail: clear-screen + cursor-home.
	printTable(&buf, p, []string{"ID", "SUBJECT"}, [][]string{
		{"01ABC", "\x1b[2J\x1b[Hpwned"},
	})
	out := buf.String()
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("escape sequence leaked to terminal output: %q", out)
	}
	if !strings.Contains(out, "pwned") {
		t.Fatalf("visible text lost during sanitization: %q", out)
	}
}

func TestVisibleWidthIgnoresColorAndCountsRunes(t *testing.T) {
	cases := map[string]int{
		"ID":               2,
		"\x1b[2mID\x1b[0m": 2,
		"—":                1, // em dash is one rune, not three bytes
		"":                 0,
	}
	for in, want := range cases {
		if got := visibleWidth(in); got != want {
			t.Errorf("visibleWidth(%q) = %d, want %d", in, got, want)
		}
	}
}
