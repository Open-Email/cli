package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/openemail/openemail-cli/internal/coreapi"
)

// confirmSpec declares one guarded destructive action: what it is, what will
// happen, and the mutation to run on confirmation.
type confirmSpec struct {
	title  string
	body   string // consequences, in full sentences
	verb   string // the y-affirmed action, e.g. "delete route"
	submit func(ctx context.Context, c *coreapi.Client) (flash string, err error)
}

type confirmDoneMsg struct {
	paneID int64
	flash  string
	err    error
}

// confirmPane is a modal y/esc gate. It captures input (q and tab do not leak
// through), submits async, and on error stays open so the user can retry or
// leave.
type confirmPane struct {
	ctx  context.Context
	ui   *Options
	spec confirmSpec
	id   int64

	submitting bool
	spin       spinner.Model
	errMsg     string
	w, h       int
}

func newConfirmPane(ctx context.Context, ui *Options, spec confirmSpec) *confirmPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &confirmPane{ctx: ctx, ui: ui, spec: spec, id: nextPaneID(), spin: sp}
}

func (p *confirmPane) init() tea.Cmd { return nil }

func (p *confirmPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.submitting {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case confirmDoneMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.submitting = false
		if msg.err != nil {
			p.errMsg = msg.err.Error()
			return p, nil
		}
		return p, popRefresh(msg.flash)

	case tea.KeyMsg:
		if p.submitting {
			return p, nil
		}
		switch msg.String() {
		case "y", "Y", "enter":
			p.submitting = true
			p.errMsg = ""
			id := p.id
			spec := p.spec
			client := p.ui.Client
			ctx := p.ctx
			return p, tea.Batch(p.spin.Tick, func() tea.Msg {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				flash, err := spec.submit(cctx, client)
				return confirmDoneMsg{paneID: id, flash: flash, err: err}
			})
		case "esc", "n", "N":
			return p, popPane
		}
	}
	return p, nil
}

func (p *confirmPane) setSize(w, h int) { p.w, p.h = w, h }

func (p *confirmPane) view() string {
	var b strings.Builder
	fmt.Fprintf(&b, " %s\n\n", stTitle.Render(p.spec.title))
	fmt.Fprintf(&b, "%s\n\n", indent(wrapPlain(p.spec.body, max(20, p.w-4)), "  "))
	switch {
	case p.submitting:
		fmt.Fprintf(&b, "  %s%s", p.spin.View(), stMeta.Render(" working…"))
	default:
		fmt.Fprintf(&b, "  %s%s", stErr.Render("[y] "+p.spec.verb), stMeta.Render("   [esc] cancel"))
	}
	if p.errMsg != "" {
		fmt.Fprintf(&b, "\n\n  %s", stErr.Render(truncate(p.errMsg, max(8, p.w-6))))
	}
	return b.String()
}

func (p *confirmPane) title() string       { return p.spec.title }
func (p *confirmPane) hints() string       { return "y confirm · esc cancel" }
func (p *confirmPane) capturesInput() bool { return true }
func (p *confirmPane) close()              {}
