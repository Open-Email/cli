package tui

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

func TestLayoutColumns(t *testing.T) {
	cols := []column{
		{title: "A", width: 10},
		{title: "B", flex: true},
		{title: "C", width: 6},
		{title: "D", flex: true},
	}
	out := layoutColumns(cols, 60)
	// fixed 16 + padding 8 = 24; remaining 36 over 2 flex = 18 each
	if out[1].Width != 18 || out[3].Width != 18 {
		t.Fatalf("flex widths = %d,%d; want 18,18", out[1].Width, out[3].Width)
	}
	if out[0].Width != 10 || out[2].Width != 6 {
		t.Fatalf("fixed widths changed: %d,%d", out[0].Width, out[2].Width)
	}
	// Starved layout: flex never collapses below the floor.
	out = layoutColumns(cols, 20)
	if out[1].Width < 10 {
		t.Fatalf("flex width %d below floor", out[1].Width)
	}
}

func TestScreenFilter(t *testing.T) {
	s := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}, {title: "KIND", width: 8}},
	})
	s.setSize(80, 20)
	s.all = []rowData{
		{cells: []string{"alpha.example.com", "mailbox"}},
		{cells: []string{"beta.example.com", "webhook"}},
		{cells: []string{"gamma.other.net", "mailbox"}},
	}
	s.applyFilter()
	if len(s.visible) != 3 {
		t.Fatalf("unfiltered visible = %d", len(s.visible))
	}
	s.filter.SetValue("EXAMPLE")
	s.applyFilter()
	if len(s.visible) != 2 {
		t.Fatalf("filter 'EXAMPLE' visible = %d; want 2 (case-insensitive)", len(s.visible))
	}
	s.filter.SetValue("webhook")
	s.applyFilter()
	if len(s.visible) != 1 || s.visible[0].cells[0] != "beta.example.com" {
		t.Fatalf("filter should match any cell, got %v", s.visible)
	}
	s.filter.SetValue("")
	s.applyFilter()
	if len(s.visible) != 3 {
		t.Fatalf("clearing filter should restore all rows")
	}
}

func TestScreenStalePageDropped(t *testing.T) {
	s := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}},
	})
	s.setSize(80, 20)
	s.seq = 2 // a newer request is in flight
	p, _ := s.update(pageMsg{paneID: s.id, seq: 1, rows: []rowData{{cells: []string{"stale"}}}, replace: true})
	s = p.(*screenPane)
	if len(s.all) != 0 {
		t.Fatalf("stale page applied: %v", s.all)
	}
	p, _ = s.update(pageMsg{paneID: s.id, seq: 2, rows: []rowData{{cells: []string{"fresh"}}}, replace: true})
	s = p.(*screenPane)
	if len(s.all) != 1 || s.all[0].cells[0] != "fresh" {
		t.Fatalf("current page not applied: %v", s.all)
	}
}

func TestWrapPlain(t *testing.T) {
	// wrapLine is the unclamped kernel.
	if got := wrapLine("aa bb cc dd", 5); got != "aa bb\ncc dd" {
		t.Fatalf("wrapLine = %q", got)
	}
	// Unbreakable runs hard-split rather than overflow.
	for _, line := range strings.Split(wrapLine("abcdefghij", 4), "\n") {
		if len(line) > 4 {
			t.Fatalf("line %q exceeds width", line)
		}
	}
	// wrapPlain preserves existing newlines and clamps tiny widths to 8.
	if got := wrapPlain("a\n\nb", 10); got != "a\n\nb" {
		t.Fatalf("blank lines not preserved: %q", got)
	}
	for _, line := range strings.Split(wrapPlain("one two three four five", 2), "\n") {
		if len(line) > 8 {
			t.Fatalf("clamped wrap produced %q", line)
		}
	}
}

func TestFmtMsgFlags(t *testing.T) {
	if got := fmtMsgFlags(nil); got != "N" {
		t.Fatalf("no flags = %q; want N (unseen)", got)
	}
	if got := fmtMsgFlags([]string{"seen"}); got != "" {
		t.Fatalf("seen = %q; want empty", got)
	}
	if got := fmtMsgFlags([]string{"flagged", "answered"}); got != "NAF" {
		t.Fatalf("unseen+answered+flagged = %q; want NAF (canonical order)", got)
	}
}

func TestSidebarClamp(t *testing.T) {
	s := sidebar{items: []sideItem{{key: "a"}, {key: "b"}}}
	s.move(-1)
	if s.cursor != 0 {
		t.Fatalf("cursor underflow: %d", s.cursor)
	}
	s.move(5)
	if s.cursor != 1 {
		t.Fatalf("cursor overflow: %d", s.cursor)
	}
}

// The root model must route esc as pop and land focus on the sidebar at the
// stack bottom, never quitting by accident.
func TestRootEscAtBottomFocusesSidebar(t *testing.T) {
	m := newRoot(t.Context(), Options{Role: "account"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.focus != focusContent {
		t.Fatalf("initial focus should be content")
	}
	// esc at the stack bottom → popPaneMsg → focus sidebar
	_, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("esc should produce a command")
	}
	m.Update(cmd())
	if m.focus != focusSidebar {
		t.Fatalf("esc at bottom should focus the sidebar")
	}
}

// A zero-value bubbles spinner renders the literal "(error)" — every pane that
// shows a loading spinner must have assigned a real one (regression: the
// preview pane constructed a spinner and forgot to store it, so message
// previews titled themselves "subject (error)" while the body loaded).
func TestPreviewLoadingViewHasNoSpinnerError(t *testing.T) {
	sub := "hello"
	p := newPreviewPane(t.Context(), &Options{}, "01M", coreapi.MessageMeta{ID: "01X", Subject: &sub})
	p.setSize(80, 24)
	p.loading = true
	if v := p.view(); strings.Contains(v, "(error)") {
		t.Fatalf("loading preview renders a broken spinner: %q", v)
	}
}
