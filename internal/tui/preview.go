package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// contentMsg is the fetched email-client view of one message.
type contentMsg struct {
	paneID int64
	c      *coreapi.ContentResult
	err    error
}

// previewPane shows one message: decoded headers, the text body inline, and
// attachments as references (read-only; parts are fetched with the CLI).
type previewPane struct {
	ctx       context.Context
	ui        *Options
	mailboxID string
	meta      coreapi.MessageMeta
	id        int64

	vp      viewport.Model
	spin    spinner.Model
	loading bool
	errMsg  string
	content *coreapi.ContentResult

	w, h int
}

func newPreviewPane(ctx context.Context, ui *Options, mailboxID string, meta coreapi.MessageMeta) *previewPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &previewPane{
		ctx:       ctx,
		ui:        ui,
		mailboxID: mailboxID,
		meta:      meta,
		id:        nextPaneID(),
		vp:        viewport.New(0, 0),
		spin:      sp, // a zero-value spinner renders the literal "(error)"
	}
}

func (p *previewPane) init() tea.Cmd {
	p.loading = true
	id := p.id
	fetch := func() tea.Msg {
		ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
		defer cancel()
		c, err := p.ui.Client.GetContent(ctx, p.mailboxID, p.meta.ID)
		return contentMsg{paneID: id, c: c, err: err}
	}
	return tea.Batch(p.spin.Tick, fetch)
}

func (p *previewPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.loading {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case contentMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.loading = false
		if msg.err != nil {
			p.errMsg = msg.err.Error()
		} else {
			p.content = msg.c
		}
		p.setContent()
		return p, nil

	case tea.KeyMsg:
		if msg.String() == "esc" {
			return p, popPane
		}
		var cmd tea.Cmd
		p.vp, cmd = p.vp.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p *previewPane) setSize(w, h int) {
	p.w, p.h = w, h
	p.vp.Width = w - 2
	p.vp.Height = h - 1
	p.setContent()
}

func (p *previewPane) setContent() {
	if p.content == nil {
		if p.errMsg != "" {
			p.vp.SetContent("\n  " + stErr.Render(p.errMsg))
		}
		return
	}
	c := p.content
	w := p.vp.Width
	var b strings.Builder

	hdr := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "  %s%s\n", stKey.Render(fmt.Sprintf("%-8s", k)), stVal.Render(truncate(v, w-12)))
	}
	hdr("From", fmtAddrs(c.Headers.From))
	hdr("To", fmtAddrs(c.Headers.To))
	hdr("Cc", fmtAddrs(c.Headers.Cc))
	hdr("Date", fmtEpochPtr(c.Headers.Date))
	hdr("Subject", strOr(c.Headers.Subject, "(no subject)"))
	fmt.Fprintf(&b, "\n%s\n\n", stDim.Render(strings.Repeat("─", max(4, w-2))))

	switch {
	case c.Text != nil && c.Text.Content != nil:
		b.WriteString(indent(wrapPlain(*c.Text.Content, w-4), "  "))
		b.WriteByte('\n')
	case c.Text != nil && c.Text.Truncated:
		fmt.Fprintf(&b, "  %s\n", stDim.Render(fmt.Sprintf("(text body too large to inline — %s; fetch with `openemail messages content`)", fmtBytes(c.Text.Size))))
	case c.HTML != nil:
		fmt.Fprintf(&b, "  %s\n", stDim.Render(fmt.Sprintf("(no text body — HTML body, %s)", fmtBytes(c.HTML.Size))))
	default:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("(no body)"))
	}

	if len(c.Attachments) > 0 {
		fmt.Fprintf(&b, "\n%s\n", stDim.Render(strings.Repeat("─", max(4, w-2))))
		fmt.Fprintf(&b, " %s\n", stSection.Render(fmt.Sprintf("Attachments (%d)", len(c.Attachments))))
		for _, a := range c.Attachments {
			name := a.Filename
			if name == "" {
				name = "(unnamed)"
			}
			line := fmt.Sprintf("  %s  %s  %s", truncate(name, max(12, w-40)), a.ContentType, fmtBytes(a.Size))
			if a.Inline {
				line += "  inline"
			}
			fmt.Fprintf(&b, "%s%s\n", stVal.Render(line), stDim.Render("  [part "+a.Section+"]"))
		}
	}
	p.vp.SetContent(b.String())
}

func (p *previewPane) view() string {
	head := " " + stTitle.Render(truncate(strOr(p.meta.Subject, "(no subject)"), max(10, p.w-20)))
	if p.loading {
		head += "  " + p.spin.View()
	}
	if p.errMsg != "" && p.content != nil {
		head += "  " + stErr.Render(truncate(p.errMsg, p.w-width(head)-4))
	}
	return head + "\n" + p.vp.View()
}

func (p *previewPane) title() string {
	return truncate(strOr(p.meta.Subject, "(no subject)"), 32)
}

func (p *previewPane) hints() string       { return "↑/↓ scroll · esc back" }
func (p *previewPane) capturesInput() bool { return false }
func (p *previewPane) close()              {}

func fmtAddrs(addrs []coreapi.ContentAddress) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		if a.Name != nil && *a.Name != "" {
			parts[i] = *a.Name + " <" + a.Email + ">"
		} else {
			parts[i] = a.Email
		}
	}
	return strings.Join(parts, ", ")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
