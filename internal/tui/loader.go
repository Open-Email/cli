package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/openemail/openemail-cli/internal/coreapi"
)

// loadedPaneMsg is a loader's outcome: the pane to swap in, or an error.
type loadedPaneMsg struct {
	paneID int64
	p      pane
	err    error
}

// loaderPane fetches prerequisite data (e.g. the accounts list a picker
// needs), showing a spinner, then replaces itself with the pane built from the
// result. esc cancels; an error shows in place with esc to back out.
type loaderPane struct {
	ctx     context.Context
	ui      *Options
	heading string
	build   func(ctx context.Context, c *coreapi.Client) (pane, error)
	id      int64
	spin    spinner.Model
	errMsg  string
	w, h    int
}

func newLoaderPane(ctx context.Context, ui *Options, heading string, build func(ctx context.Context, c *coreapi.Client) (pane, error)) *loaderPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &loaderPane{ctx: ctx, ui: ui, heading: heading, build: build, id: nextPaneID(), spin: sp}
}

func (p *loaderPane) init() tea.Cmd {
	id := p.id
	build := p.build
	client := p.ui.Client
	ctx := p.ctx
	return tea.Batch(p.spin.Tick, func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		next, err := build(cctx, client)
		return loadedPaneMsg{paneID: id, p: next, err: err}
	})
}

func (p *loaderPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if p.errMsg != "" {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case loadedPaneMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		if msg.err != nil {
			p.errMsg = msg.err.Error()
			return p, nil
		}
		// Swap: pop this loader, push the built pane.
		return p, tea.Sequence(popPane, pushPane(msg.p))

	case tea.KeyMsg:
		if msg.String() == "esc" {
			return p, popPane
		}
	}
	return p, nil
}

func (p *loaderPane) setSize(w, h int) { p.w, p.h = w, h }

func (p *loaderPane) view() string {
	s := " " + stTitle.Render(p.heading) + "\n\n  " + p.spin.View() + stMeta.Render(" loading…")
	if p.errMsg != "" {
		s = " " + stTitle.Render(p.heading) + "\n\n  " + stErr.Render(truncate(p.errMsg, max(8, p.w-6))) + "\n\n  " + stMeta.Render("esc to go back")
	}
	return s
}

func (p *loaderPane) title() string       { return p.heading }
func (p *loaderPane) hints() string       { return "esc cancel" }
func (p *loaderPane) capturesInput() bool { return true }
func (p *loaderPane) close()              {}
