package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// width is the ANSI-aware display width (lipgloss handles styled strings).
func width(s string) int { return lipgloss.Width(s) }

// extraMsg carries a detail pane's async enrichment (traffic, members).
type extraMsg struct {
	paneID int64
	kvs    []kv
	err    error
}

// detailPane renders one selected item as aligned key/value lines in a
// scrollable viewport, with optional async enrichment appended when it lands.
type detailPane struct {
	ctx  context.Context
	ui   *Options
	desc resourceDesc
	row  rowData
	id   int64

	vp       viewport.Model
	spin     spinner.Model
	loading  bool
	base     []kv
	extraKVs []kv
	extraErr string

	w, h int
}

func newDetailPane(ctx context.Context, ui *Options, desc resourceDesc, row rowData) *detailPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &detailPane{
		ctx:  ctx,
		ui:   ui,
		desc: desc,
		row:  row,
		id:   nextPaneID(),
		vp:   viewport.New(0, 0),
		spin: sp,
		base: desc.detail(row.item),
	}
}

func (d *detailPane) init() tea.Cmd {
	if d.desc.extra == nil {
		return nil
	}
	d.loading = true
	item := d.row.item
	id := d.id
	fetch := func() tea.Msg {
		ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
		defer cancel()
		kvs, err := d.desc.extra(ctx, d.ui.Client, item)
		return extraMsg{paneID: id, kvs: kvs, err: err}
	}
	return tea.Batch(d.spin.Tick, fetch)
}

func (d *detailPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !d.loading {
			return d, nil
		}
		var cmd tea.Cmd
		d.spin, cmd = d.spin.Update(msg)
		return d, cmd

	case extraMsg:
		if msg.paneID != d.id {
			return d, nil
		}
		d.loading = false
		if msg.err != nil {
			d.extraErr = msg.err.Error()
		} else {
			d.extraKVs = msg.kvs
		}
		d.setContent()
		return d, nil

	case tea.KeyMsg:
		if msg.String() == "esc" {
			return d, popPane
		}
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(msg)
		return d, cmd
	}
	return d, nil
}

func (d *detailPane) setSize(w, h int) {
	d.w, d.h = w, h
	d.vp.Width = w - 2
	d.vp.Height = h - 1
	d.setContent()
}

func (d *detailPane) setContent() {
	kvs := d.base
	if len(d.extraKVs) > 0 {
		kvs = append(append([]kv{}, d.base...), d.extraKVs...)
	}
	d.vp.SetContent(renderKVs(kvs, d.vp.Width))
}

func (d *detailPane) view() string {
	head := " " + stTitle.Render(d.desc.name+" › "+d.title())
	if d.loading {
		head += "  " + d.spin.View()
	}
	if d.extraErr != "" {
		head += "  " + stErr.Render(truncate(d.extraErr, d.w-width(head)-4))
	}
	return head + "\n" + d.vp.View()
}

func (d *detailPane) title() string {
	if len(d.row.cells) > 0 {
		return d.row.cells[0]
	}
	return "detail"
}

func (d *detailPane) hints() string       { return "↑/↓ scroll · esc back" }
func (d *detailPane) capturesInput() bool { return false }
func (d *detailPane) close()              {}

// renderKVs lays out detail lines: keys right-padded to a shared column,
// values as-is (the viewport scrolls; long values are the rare case).
func renderKVs(kvs []kv, w int) string {
	keyW := 0
	for _, e := range kvs {
		if e.k != "" && len(e.k) > keyW {
			keyW = len(e.k)
		}
	}
	var b strings.Builder
	for _, e := range kvs {
		switch {
		case e.k == "" && e.v == "":
			b.WriteByte('\n')
		case e.k == "":
			b.WriteString(" " + stSection.Render(e.v) + "\n")
		default:
			pad := strings.Repeat(" ", keyW-len(e.k))
			val := e.v
			if max := w - keyW - 5; max > 8 {
				val = truncate(val, max)
			}
			b.WriteString("  " + stKey.Render(e.k) + pad + "  " + stVal.Render(val) + "\n")
		}
	}
	return b.String()
}
