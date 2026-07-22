package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/openemail/openemail-cli/internal/coreapi"
)

const pageLimit = 50

// column is one table column; flex columns share the leftover width.
type column struct {
	title string
	width int
	flex  bool
}

// kv is one detail line. k=="" renders v as a section header; both empty is a
// spacer line.
type kv struct{ k, v string }

// rowData pairs rendered cells with the source item for detail/drill-down.
type rowData struct {
	cells []string
	item  any
}

// resourceDesc declares one listing screen. fetch returns a page; detail
// renders a selected item; extra (optional) asynchronously enriches the detail
// pane (domain traffic, group members); open (optional) replaces the
// enter-detail behavior with a drill-down pane (mailboxes → messages); actions
// (optional) are the screen's mutations, each opening a form or confirm pane.
type resourceDesc struct {
	key     string
	name    string
	columns []column
	fetch   func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error)
	detail  func(item any) []kv
	extra   func(ctx context.Context, c *coreapi.Client, item any) ([]kv, error)
	open    func(ctx context.Context, ui *Options, item any) pane
	actions []action
}

// action is one keyed mutation on a listing. needsRow actions receive the
// selected item. Exactly one of run/do is set: run returns a form/confirm
// pane to push (nil = no-op); do executes directly — no modal — and the
// listing refreshes with the returned flash (restore, member remove-less ops).
type action struct {
	key      string
	label    string // status-bar help, e.g. "n new"
	needsRow bool
	run      func(ctx context.Context, ui *Options, item any) pane
	do       func(ctx context.Context, c *coreapi.Client, item any) (flash string, err error)
}

// actDoneMsg is a completed direct-run action.
type actDoneMsg struct {
	paneID int64
	flash  string
	err    error
}

// pageMsg is one fetched page, tagged to its pane instance and request.
type pageMsg struct {
	paneID  int64
	seq     int
	rows    []rowData
	next    string
	replace bool
	err     error
}

// screenPane is the generic resource listing: header line, optional filter
// input, table, cursor-paginated load-more.
type screenPane struct {
	ctx  context.Context
	ui   *Options
	desc resourceDesc
	id   int64
	seq  int

	tbl       table.Model
	filter    textinput.Model
	filtering bool // filter input focused (typing)
	spin      spinner.Model
	loading   bool // initial/replace fetch in flight
	more      bool // load-more fetch in flight
	errMsg    string
	flash     string // success notice from a completed form/confirm

	all     []rowData
	visible []rowData
	next    string

	w, h int
}

func newScreenPane(ctx context.Context, ui *Options, desc resourceDesc) *screenPane {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter"
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	t := newTable()
	return &screenPane{
		ctx:    ctx,
		ui:     ui,
		desc:   desc,
		id:     nextPaneID(),
		tbl:    t,
		filter: ti,
		spin:   sp,
	}
}

func (s *screenPane) init() tea.Cmd {
	s.loading = true
	return tea.Batch(s.spin.Tick, s.fetchCmd("", true))
}

func (s *screenPane) fetchCmd(cursor string, replace bool) tea.Cmd {
	s.seq++
	seq := s.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		rows, next, err := s.desc.fetch(ctx, s.ui.Client, cursor)
		return pageMsg{paneID: s.id, seq: seq, rows: rows, next: next, replace: replace, err: err}
	}
}

func (s *screenPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.loading && !s.more {
			return s, nil // stop the tick loop while idle
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case refreshMsg:
		s.flash = msg.flash
		s.loading = true
		s.next = ""
		return s, tea.Batch(s.spin.Tick, s.fetchCmd("", true))

	case actDoneMsg:
		if msg.paneID != s.id {
			return s, nil
		}
		if msg.err != nil {
			s.errMsg = msg.err.Error()
			return s, nil
		}
		s.errMsg = ""
		s.flash = msg.flash
		s.loading = true
		s.next = ""
		return s, tea.Batch(s.spin.Tick, s.fetchCmd("", true))

	case pageMsg:
		if msg.paneID != s.id || msg.seq != s.seq {
			return s, nil // a stale request; a newer one is in flight
		}
		s.loading, s.more = false, false
		if msg.err != nil {
			s.errMsg = msg.err.Error()
			return s, nil
		}
		s.errMsg = ""
		if msg.replace {
			s.all = msg.rows
		} else {
			s.all = append(s.all, msg.rows...)
		}
		s.next = msg.next
		s.applyFilter()
		return s, nil

	case tea.KeyMsg:
		return s.updateKey(msg)
	}
	return s, nil
}

func (s *screenPane) updateKey(msg tea.KeyMsg) (pane, tea.Cmd) {
	if s.filtering {
		switch msg.String() {
		case "esc":
			s.filtering = false
			s.filter.Blur()
			s.filter.SetValue("")
			s.applyFilter()
			return s, nil
		case "enter":
			s.filtering = false
			s.filter.Blur()
			return s, nil
		default:
			var cmd tea.Cmd
			s.filter, cmd = s.filter.Update(msg)
			s.applyFilter()
			return s, cmd
		}
	}

	switch msg.String() {
	case "/":
		s.filtering = true
		return s, s.filter.Focus()
	case "esc":
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.applyFilter()
			return s, nil
		}
		return s, popPane
	case "r":
		s.flash = ""
		s.loading = true
		s.next = ""
		return s, tea.Batch(s.spin.Tick, s.fetchCmd("", true))
	case "m":
		return s, s.loadMore()
	case "enter":
		return s, s.enter()
	}

	for _, a := range s.desc.actions {
		if a.key != msg.String() {
			continue
		}
		var item any
		if a.needsRow {
			idx := s.tbl.Cursor()
			if idx < 0 || idx >= len(s.visible) {
				return s, nil
			}
			item = s.visible[idx].item
		}
		if a.do != nil {
			s.flash = ""
			id, client, do := s.id, s.ui.Client, a.do
			ctx := s.ctx
			return s, func() tea.Msg {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				flash, err := do(cctx, client, item)
				return actDoneMsg{paneID: id, flash: flash, err: err}
			}
		}
		if p := a.run(s.ctx, s.ui, item); p != nil {
			s.flash = ""
			return s, pushPane(p)
		}
		return s, nil
	}

	var cmd tea.Cmd
	s.tbl, cmd = s.tbl.Update(msg)
	// Reaching the bottom of an unfinished list loads the next page.
	if s.next != "" && !s.more && !s.loading && s.tbl.Cursor() == len(s.visible)-1 {
		return s, tea.Batch(cmd, s.loadMore())
	}
	return s, cmd
}

func (s *screenPane) loadMore() tea.Cmd {
	if s.next == "" || s.more || s.loading {
		return nil
	}
	s.more = true
	return tea.Batch(s.spin.Tick, s.fetchCmd(s.next, false))
}

func (s *screenPane) enter() tea.Cmd {
	idx := s.tbl.Cursor()
	if idx < 0 || idx >= len(s.visible) {
		return nil
	}
	item := s.visible[idx].item
	if s.desc.open != nil {
		return pushPane(s.desc.open(s.ctx, s.ui, item))
	}
	if s.desc.detail != nil {
		return pushPane(newDetailPane(s.ctx, s.ui, s.desc, s.visible[idx]))
	}
	return nil
}

// applyFilter recomputes the visible subset (case-insensitive substring over
// every cell) and pushes it into the table.
func (s *screenPane) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(s.filter.Value()))
	if q == "" {
		s.visible = s.all
	} else {
		s.visible = nil
		for _, r := range s.all {
			for _, c := range r.cells {
				if strings.Contains(strings.ToLower(c), q) {
					s.visible = append(s.visible, r)
					break
				}
			}
		}
	}
	rows := make([]table.Row, len(s.visible))
	for i, r := range s.visible {
		rows[i] = table.Row(r.cells)
	}
	s.tbl.SetRows(rows)
	if s.tbl.Cursor() >= len(rows) {
		s.tbl.SetCursor(0)
	}
}

func (s *screenPane) setSize(w, h int) {
	s.w, s.h = w, h
	inner := w - 2 // pane gutter
	if inner < 20 {
		inner = 20
	}
	s.tbl.SetColumns(layoutColumns(s.desc.columns, inner))
	th := h - 1 - s.filterLines()
	if th < 3 {
		th = 3
	}
	s.tbl.SetWidth(inner)
	s.tbl.SetHeight(th)
	s.filter.Width = inner - 4
}

func (s *screenPane) filterLines() int {
	if s.filtering || s.filter.Value() != "" {
		return 1
	}
	return 0
}

func (s *screenPane) view() string {
	head := " " + stTitle.Render(s.desc.name) + stMeta.Render(fmt.Sprintf("  %d loaded", len(s.all)))
	if s.filter.Value() != "" && !s.filtering {
		head += stMeta.Render(fmt.Sprintf(" · %d match", len(s.visible)))
	}
	if s.next != "" {
		head += stMeta.Render(" · more available")
	}
	if s.loading || s.more {
		head += "  " + s.spin.View()
	}
	if s.flash != "" {
		head += "  " + stLive.Render(truncate(s.flash, max(8, s.w-lipglossSafeWidth(head)-4)))
	}
	if s.errMsg != "" {
		head += "  " + stErr.Render(truncate(s.errMsg, s.w-lipglossSafeWidth(head)-4))
	}
	parts := []string{head}
	if s.filterLines() > 0 {
		parts = append(parts, " "+s.filter.View())
	}
	parts = append(parts, s.tbl.View())
	return strings.Join(parts, "\n")
}

func (s *screenPane) title() string { return s.desc.name }

func (s *screenPane) hints() string {
	h := "enter detail"
	if s.desc.open != nil {
		h = "enter open"
	}
	for _, a := range s.desc.actions {
		h += " · " + a.label
	}
	h += " · / filter · r refresh · esc back"
	if s.next != "" {
		h += " · m more"
	}
	return h
}

func (s *screenPane) capturesInput() bool { return s.filtering }
func (s *screenPane) close()              {}

// layoutColumns distributes a content width over the declared columns; each
// rendered column costs its width plus 2 (cell padding), flex columns share
// what remains.
func layoutColumns(cols []column, avail int) []table.Column {
	fixed, flexes := 0, 0
	for _, c := range cols {
		if c.flex {
			flexes++
		} else {
			fixed += c.width
		}
	}
	rem := avail - fixed - 2*len(cols)
	per := 0
	if flexes > 0 {
		per = rem / flexes
		if per < 10 {
			per = 10
		}
	}
	out := make([]table.Column, len(cols))
	for i, c := range cols {
		w := c.width
		if c.flex {
			w = per
		}
		out[i] = table.Column{Title: c.title, Width: w}
	}
	return out
}

func lipglossSafeWidth(s string) int {
	// lipgloss.Width on a styled string; kept behind a helper so screen code
	// reads as layout.
	return width(s)
}
