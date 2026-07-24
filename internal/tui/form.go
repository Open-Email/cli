package tui

import (
	"context"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// formSpec declares one mutation form. build constructs the huh form over
// variables the spec's closures share, so a failed submit rebuilds the form
// with everything the user typed still in place. submit runs the mutation;
// flash is the success notice for the underlying listing, after (optional) is
// a pane to push once the form pops (e.g. the one-time API-key token reveal).
type formSpec struct {
	title  string
	submit func(ctx context.Context, c *coreapi.Client) (flash string, after pane, err error)
	build  func() *huh.Form
}

// formDoneMsg carries a submit's outcome back to its formPane.
type formDoneMsg struct {
	paneID int64
	flash  string
	after  pane
	err    error
}

// formPane hosts a huh form in the pane stack. esc cancels (huh itself only
// quits on ctrl+c), a completed form submits async, and an API error re-opens
// the form with the error shown and values preserved.
type formPane struct {
	ctx  context.Context
	ui   *Options
	spec formSpec
	id   int64

	form       *huh.Form
	submitting bool
	spin       spinner.Model
	errMsg     string
	w, h       int
}

func newFormPane(ctx context.Context, ui *Options, spec formSpec) *formPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &formPane{
		ctx:  ctx,
		ui:   ui,
		spec: spec,
		id:   nextPaneID(),
		form: styledForm(spec.build()),
		spin: sp,
	}
}

func styledForm(f *huh.Form) *huh.Form {
	return f.WithTheme(formTheme()).WithShowHelp(false)
}

func (p *formPane) init() tea.Cmd { return p.form.Init() }

func (p *formPane) submitCmd() tea.Cmd {
	id := p.id
	spec := p.spec
	client := p.ui.Client
	ctx := p.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		flash, after, err := spec.submit(cctx, client)
		return formDoneMsg{paneID: id, flash: flash, after: after, err: err}
	}
}

func (p *formPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.submitting {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case formDoneMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.submitting = false
		if msg.err != nil {
			// Re-open the form: the bound variables still hold the input, so
			// the user edits rather than retypes.
			p.errMsg = msg.err.Error()
			p.form = styledForm(p.spec.build())
			p.setSize(p.w, p.h)
			return p, p.form.Init()
		}
		if msg.after != nil {
			// Pop this form and show the after-pane (e.g. the one-time token
			// reveal). Do NOT refresh the revealed listing here: it would issue a
			// fetch whose result is routed to the after-pane (now on top) and
			// dropped, leaving the listing stale AND stuck loading. The after-pane
			// refreshes the listing when it is dismissed and back on top.
			return p, tea.Sequence(popPane, pushPane(msg.after))
		}
		return p, popRefresh(msg.flash)

	case tea.KeyMsg:
		if p.submitting {
			return p, nil // modal while the mutation is in flight
		}
		if msg.String() == "esc" {
			return p, popPane
		}
	}

	if p.submitting {
		return p, nil
	}
	fm, cmd := p.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		p.form = f
	}
	switch p.form.State {
	case huh.StateCompleted:
		p.submitting = true
		p.errMsg = ""
		return p, tea.Batch(p.spin.Tick, p.submitCmd())
	case huh.StateAborted:
		return p, popPane
	}
	return p, cmd
}

func (p *formPane) setSize(w, h int) {
	p.w, p.h = w, h
	fw := w - 4
	if fw > 72 {
		fw = 72
	}
	if fw < 24 {
		fw = 24
	}
	// Height must be constrained too: without it a tall form (compose) clips
	// past the content box and the LAST field — the send step — is invisible
	// on short terminals. With a height, huh scrolls the focused field into
	// view instead.
	fh := h - 2 // pane header + blank line
	if fh < 6 {
		fh = 6
	}
	p.form = p.form.WithWidth(fw).WithHeight(fh)
}

func (p *formPane) view() string {
	head := " " + stTitle.Render(p.spec.title)
	if p.submitting {
		head += "  " + p.spin.View() + stMeta.Render(" saving…")
	}
	if p.errMsg != "" {
		head += "  " + stErr.Render(truncate(p.errMsg, max(8, p.w-width(head)-4)))
	}
	return head + "\n\n" + indent(p.form.View(), " ")
}

func (p *formPane) title() string       { return p.spec.title }
func (p *formPane) hints() string       { return "enter next field · shift+tab back · esc cancel" }
func (p *formPane) capturesInput() bool { return true }
func (p *formPane) close()              {}
