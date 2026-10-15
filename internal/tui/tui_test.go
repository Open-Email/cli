package tui

import (
	"context"
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

func TestRootRoutesPaneScopedMsgToUnderlyingPane(t *testing.T) {
	m := newRoot(t.Context(), Options{Role: "account"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Base pane is a screenPane (Mailboxes). Push a child notePane on top.
	base, ok := m.stack[0].(*screenPane)
	if !ok {
		t.Fatalf("expected base *screenPane, got %T", m.stack[0])
	}
	base.loading = true
	base.seq = 1

	m.Update(pushPaneMsg{p: newNotePane("Child", []string{"body"})})
	if len(m.stack) != 2 {
		t.Fatalf("expected 2 panes on stack, got %d", len(m.stack))
	}

	// Deliver pageMsg for the underlying screenPane while child notePane is on top.
	m.Update(pageMsg{
		paneID:  base.id,
		seq:     1,
		rows:    []rowData{{cells: []string{"01M", "alice@example.test", "∞", "2026-01-01"}}},
		replace: true,
	})

	baseAfter := m.stack[0].(*screenPane)
	if baseAfter.loading {
		t.Fatalf("underlying screenPane remained stuck in loading=true when pageMsg arrived while child pane was on top")
	}
	if len(baseAfter.all) != 1 {
		t.Fatalf("expected 1 row in underlying screenPane, got %d", len(baseAfter.all))
	}
}

func TestMessagesPaneLiveEventRoutedUnderChildAndSkipsPIM(t *testing.T) {
	m := newRoot(t.Context(), Options{Role: "account"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	mp := newMessagesPane(t.Context(), &m.ui, coreapi.Mailbox{ID: "01M"})
	mp.setSize(80, 24)
	mp.loading = false
	m.stack = []pane{mp}

	// Push a previewPane on top of messagesPane.
	sub := "hello"
	prev := newPreviewPane(t.Context(), &m.ui, "01M", coreapi.MessageMeta{ID: "01X", Subject: &sub})
	m.Update(pushPaneMsg{p: prev})

	// Deliver a mail liveEvent targeted at mp while previewPane is on top.
	m.Update(liveEvent{paneID: mp.id, typ: "message.created"})
	mpAfter := m.stack[0].(*messagesPane)
	if !mpAfter.loading {
		t.Fatalf("expected underlying messagesPane to start refreshing on message.created liveEvent even while previewPane is on top")
	}

	// Deliver msgsPageMsg targeted at mp while previewPane is still on top.
	mpAfter.loading = true
	m.Update(msgsPageMsg{
		paneID:  mpAfter.id,
		seq:     mpAfter.seq,
		items:   []coreapi.MessageMeta{{ID: "01X", Subject: &sub}},
		replace: true,
	})
	mpAfter = m.stack[0].(*messagesPane)
	if mpAfter.loading || len(mpAfter.all) != 1 {
		t.Fatalf("expected underlying messagesPane to process msgsPageMsg while child pane is on top: loading=%v len=%d", mpAfter.loading, len(mpAfter.all))
	}

	// A pim.* event should NOT trigger a mail message list refresh.
	prevSeq := mpAfter.seq
	m.Update(liveEvent{paneID: mpAfter.id, typ: "pim.calendar.updated"})
	mpAfter = m.stack[0].(*messagesPane)
	if mpAfter.loading || mpAfter.seq != prevSeq {
		t.Fatalf("pim.* liveEvent should not trigger messagesPane refresh: loading=%v seq=%d (was %d)", mpAfter.loading, mpAfter.seq, prevSeq)
	}
}

func TestRulesScreenStateConcurrentAccess(t *testing.T) {
	st := &rulesScreenState{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			st.set(&coreapi.FilterRulesState{
				State: "active",
				Rules: []coreapi.FilterRule{{Name: "Rule 1"}},
			})
		}
	}()
	for i := 0; i < 200; i++ {
		_ = st.summaryLines(80)
	}
	<-done
}

func TestCursorClampedOnRowDeletion(t *testing.T) {
	s := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}},
	})
	s.setSize(80, 20)
	s.all = []rowData{
		{cells: []string{"a"}},
		{cells: []string{"b"}},
		{cells: []string{"c"}},
	}
	s.applyFilter()
	s.tbl.SetCursor(2) // last row ("c")

	// Simulate refetch after deleting the last row ("c"): 2 rows remain.
	s.all = []rowData{
		{cells: []string{"a"}},
		{cells: []string{"b"}},
	}
	s.applyFilter()
	if got := s.tbl.Cursor(); got != 1 {
		t.Fatalf("cursor after deleting last row = %d; want 1 (clamped to last remaining row, not 0)", got)
	}
}

func TestConfirmPaneIgnoresEnterKey(t *testing.T) {
	ran := false
	p := newConfirmPane(t.Context(), &Options{}, confirmSpec{
		title: "Delete thing",
		body:  "gone",
		verb:  "delete",
		submit: func(tCtx context.Context, c *coreapi.Client) (string, error) {
			ran = true
			return "deleted", nil
		},
	})
	p.setSize(80, 20)
	_, cmd := p.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || ran {
		t.Fatalf("enter must not trigger destructive confirmPane action (require explicit 'y')")
	}
}

func TestWideHeaderErrorNotSwallowed(t *testing.T) {
	s := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name:    strings.Repeat("VeryLongScreenTitle", 4),
		columns: []column{{title: "NAME", flex: true}},
	})
	s.setSize(40, 20)
	s.loading = false
	s.errMsg = "network timeout talking to core"
	v := s.view()
	if !strings.Contains(v, "network") {
		t.Fatalf("expected error message to remain visible even when header is wider than pane, got:\n%s", v)
	}
}

func TestDetailAndSourceTitles(t *testing.T) {
	dp := newDetailPane(t.Context(), &Options{}, resourceDesc{
		detail: func(any) []kv { return nil },
	}, rowData{
		cells: []string{"1", "Newsletter filter", "on"},
		item:  "r1",
	})
	if got := dp.title(); got != "Newsletter filter" {
		t.Fatalf("detailPane.title() on numbered row = %q; want %q", got, "Newsletter filter")
	}

	sp := newSourcePane(t.Context(), &Options{}, "01M", coreapi.MessageMeta{ID: "01MSG"})
	if got := sp.title(); got != "Source" {
		t.Fatalf("sourcePane.title() = %q; want %q", got, "Source")
	}
}

