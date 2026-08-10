package tui

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

// S on an open message opens its raw source — including when the decoded
// content never loaded, which is exactly when the source is worth reading.
func TestPreviewSourceKeyPushesSourcePane(t *testing.T) {
	sub := "hello"
	p := newPreviewPane(t.Context(), &Options{}, "01M", coreapi.MessageMeta{ID: "01X", Subject: &sub})
	p.setSize(80, 24)
	_, cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	if cmd == nil {
		t.Fatal("S produced no command")
	}
	push, ok := cmd().(pushPaneMsg)
	if !ok {
		t.Fatalf("S produced %T, want pushPaneMsg", cmd())
	}
	if _, ok := push.p.(*sourcePane); !ok {
		t.Fatalf("S pushed %T, want *sourcePane", push.p)
	}
}

func TestSourceLoadingViewHasNoSpinnerError(t *testing.T) {
	p := newSourcePane(t.Context(), &Options{}, "01M", coreapi.MessageMeta{ID: "01X"})
	p.setSize(80, 24)
	p.loading = true
	if v := p.view(); strings.Contains(v, "(error)") {
		t.Fatalf("loading source renders a broken spinner: %q", v)
	}
}

// A message body is attacker-supplied bytes. Nothing that can move the cursor,
// clear the screen, or repaint the UI may reach the terminal.
func TestSanitizeSourceNeutralizesControlBytes(t *testing.T) {
	lines := sanitizeSource("Subject: \x1b[2Jgotcha\r\nX-Bell: \x07\x00\x7f\r\n\r\nbody")
	joined := strings.Join(lines, "\n")
	for _, bad := range []string{"\x1b", "\x07", "\x00", "\x7f", "", "\r"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("control byte %q survived sanitization: %q", bad, joined)
		}
	}
	if !strings.Contains(joined, "␛[2Jgotcha") {
		t.Fatalf("ESC should stay VISIBLE as a control picture, not vanish: %q", joined)
	}
	if len(lines) != 4 || lines[3] != "body" {
		t.Fatalf("CRLF should split into 4 lines with the body last, got %q", lines)
	}
}

func TestSanitizeSourceInvalidUTF8AndTabs(t *testing.T) {
	// A byte cap can cut a rune in half, and an 8-bit body is not UTF-8 at all.
	got := sanitizeSource("From: \xff\xfe bad")
	if !strings.ContainsRune(got[0], '�') {
		t.Fatalf("invalid UTF-8 not replaced: %q", got[0])
	}
	// ContainsRune would report the replacement char here (invalid bytes decode
	// to it), so the raw bytes have to be looked for as bytes.
	if strings.IndexByte(got[0], 0xff) >= 0 || strings.IndexByte(got[0], 0xfe) >= 0 {
		t.Fatalf("raw invalid bytes survived: %q", got[0])
	}
	tabbed := sanitizeSource("ab\tc")
	if tabbed[0] != "ab      c" {
		t.Fatalf("tab should expand to the next 8-column stop, got %q", tabbed[0])
	}
}

// Wrapping a folded header must not lose the leading whitespace that makes it a
// continuation, and no produced row may exceed the width it was given.
func TestWrapSourcePreservesBytesAndWidth(t *testing.T) {
	line := "\tboundary=\"" + strings.Repeat("a", 60) + "\""
	rows := wrapSource(line, 20)
	if len(rows) < 2 {
		t.Fatalf("long line did not wrap: %v", rows)
	}
	if !strings.HasPrefix(rows[0], "\t") {
		t.Fatalf("leading whitespace lost: %q", rows[0])
	}
	var rebuilt strings.Builder
	for i, r := range rows {
		if i > 0 {
			if !strings.HasPrefix(r, contMark) {
				t.Fatalf("continuation row %d lacks the marker: %q", i, r)
			}
			r = strings.TrimPrefix(r, contMark)
		}
		if w := width(rows[i]); w > 20 {
			t.Fatalf("row %d is %d columns wide, over the 20 given: %q", i, w, rows[i])
		}
		rebuilt.WriteString(r)
	}
	if rebuilt.String() != line {
		t.Fatalf("wrapping changed the content:\n got %q\nwant %q", rebuilt.String(), line)
	}
	// A line that fits is returned untouched, marker-free.
	if rows := wrapSource("Date: today", 40); len(rows) != 1 || rows[0] != "Date: today" {
		t.Fatalf("short line was altered: %v", rows)
	}
}

// A result that lands after the user navigated away belongs to another pane.
func TestSourceDropsForeignPaneResults(t *testing.T) {
	p := newSourcePane(t.Context(), &Options{}, "01M", coreapi.MessageMeta{ID: "01X"})
	p.setSize(80, 24)
	p.loading = true
	np, _ := p.update(sourceMsg{paneID: p.id + 1, text: "stray"})
	p = np.(*sourcePane)
	if !p.loading || p.lines != nil {
		t.Fatalf("foreign result applied: loading=%v lines=%v", p.loading, p.lines)
	}
	np, _ = p.update(sourceMsg{paneID: p.id, text: "Subject: hi\n\nbody", n: 17})
	p = np.(*sourcePane)
	if p.loading || len(p.lines) != 3 {
		t.Fatalf("own result not applied: loading=%v lines=%v", p.loading, p.lines)
	}
	if v := p.vp.View(); !strings.Contains(v, "Subject: hi") {
		t.Fatalf("source not rendered: %q", v)
	}
}
