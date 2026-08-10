package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// sourceMaxBytes bounds what the source view will hold in memory. A message
// with attachments is routinely tens of megabytes of base64 that nobody reads
// by scrolling, so the view reads a head and says so; the full bytes are a
// `openemail messages raw` away.
const sourceMaxBytes = 1 << 20

// sourceMsg is the fetched raw RFC 5322 blob, already read and bounded.
type sourceMsg struct {
	paneID    int64
	text      string
	n         int64
	truncated bool
	err       error
}

// sourcePane shows one message's raw MIME source, scrollable. Bytes are shown
// as they were stored — no decoding, no reflowing of the content itself — with
// only the transformations a terminal forces: invalid UTF-8 and control bytes
// are made visible (a raw body may carry ESC sequences, which would otherwise
// repaint the UI), and over-long lines are hard-wrapped with a continuation
// marker so a folded header is never mistaken for a new field.
type sourcePane struct {
	ctx       context.Context
	ui        *Options
	mailboxID string
	meta      coreapi.MessageMeta
	id        int64

	vp      viewport.Model
	spin    spinner.Model
	loading bool
	errMsg  string

	lines     []string // sanitized logical lines; wrapped per width at render
	n         int64
	truncated bool

	w, h int
}

func newSourcePane(ctx context.Context, ui *Options, mailboxID string, meta coreapi.MessageMeta) *sourcePane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &sourcePane{
		ctx:       ctx,
		ui:        ui,
		mailboxID: mailboxID,
		meta:      meta,
		id:        nextPaneID(),
		vp:        viewport.New(0, 0),
		spin:      sp,
	}
}

func (p *sourcePane) init() tea.Cmd {
	p.loading = true
	id := p.id
	fetch := func() tea.Msg {
		// The whole read happens inside this timeout: the body is consumed here,
		// never handed back to the UI as a live stream.
		ctx, cancel := context.WithTimeout(p.ctx, 60*time.Second)
		defer cancel()
		rc, err := p.ui.Client.GetMessageRaw(ctx, p.mailboxID, p.meta.ID)
		if err != nil {
			return sourceMsg{paneID: id, err: err}
		}
		defer rc.Close()
		// One byte past the cap is what distinguishes "exactly at the cap" from
		// "there is more"; closing the reader early drops the rest of the transfer.
		buf, err := io.ReadAll(io.LimitReader(rc, sourceMaxBytes+1))
		if err != nil {
			return sourceMsg{paneID: id, err: err}
		}
		trunc := len(buf) > sourceMaxBytes
		if trunc {
			buf = buf[:sourceMaxBytes]
		}
		return sourceMsg{paneID: id, text: string(buf), n: int64(len(buf)), truncated: trunc}
	}
	return tea.Batch(p.spin.Tick, fetch)
}

func (p *sourcePane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.loading {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case sourceMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.loading = false
		if msg.err != nil {
			p.errMsg = msg.err.Error()
		} else {
			p.lines = sanitizeSource(msg.text)
			p.n, p.truncated = msg.n, msg.truncated
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

func (p *sourcePane) setSize(w, h int) {
	p.w, p.h = w, h
	p.vp.Width = w - 2
	p.vp.Height = h - 1
	p.setContent()
}

func (p *sourcePane) setContent() {
	if p.lines == nil {
		if p.errMsg != "" {
			p.vp.SetContent("\n  " + stErr.Render(p.errMsg))
		}
		return
	}
	w := max(20, p.vp.Width-2)
	var b strings.Builder
	for _, l := range p.lines {
		for _, seg := range wrapSource(l, w) {
			b.WriteString(" ")
			b.WriteString(seg)
			b.WriteByte('\n')
		}
	}
	if p.truncated {
		// Wrapped, never truncated: this line names the command that gets the rest,
		// and an ellipsis through the middle of it would defeat the point.
		note := fmt.Sprintf("(source truncated at %s — save the whole message with "+
			"`openemail messages raw %s -o msg.eml`)", fmtBytes(p.n), p.meta.ID)
		fmt.Fprintf(&b, "\n%s\n", stDim.Render(indent(wrapPlain(note, w), " ")))
	}
	p.vp.SetContent(b.String())
}

func (p *sourcePane) view() string {
	head := " " + stTitle.Render("Source — "+truncate(strOr(p.meta.Subject, "(no subject)"), max(10, p.w-32)))
	if p.lines != nil {
		head += stMeta.Render("  " + fmtBytes(p.n))
		if p.truncated {
			head += stMeta.Render(" of " + fmtBytes(p.meta.Size) + " shown")
		}
	}
	if p.loading {
		head += "  " + p.spin.View()
	}
	if p.errMsg != "" && p.lines != nil {
		head += "  " + stErr.Render(truncate(p.errMsg, p.w-width(head)-4))
	}
	return head + "\n" + p.vp.View()
}

func (p *sourcePane) title() string {
	return "source"
}

func (p *sourcePane) hints() string       { return "↑/↓ scroll · pgup/pgdn page · esc back" }
func (p *sourcePane) capturesInput() bool { return false }
func (p *sourcePane) close()              {}

// sanitizeSource turns stored message bytes into lines a terminal may print.
// Everything here is forced by the medium, not by taste: bytes that are not
// valid UTF-8 (an 8-bit body, or the rune the byte cap cut in half) become the
// replacement char, CRLF becomes LF, tabs are expanded to 8-column stops, and
// control bytes are shown as their Unicode Control Picture rather than
// executed — a message body carrying ESC[2J would otherwise clear the screen.
func sanitizeSource(s string) []string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = sanitizeSourceLine(l)
	}
	return lines
}

func sanitizeSourceLine(l string) string {
	if strings.IndexFunc(l, isSourceHazard) < 0 {
		return l
	}
	var b strings.Builder
	b.Grow(len(l))
	col := 0
	for _, r := range l {
		switch {
		case r == '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r < 0x20:
			b.WriteRune(0x2400 + r) // ␀ … ␟
			col++
		case r == 0x7f:
			b.WriteRune(0x2421) // ␡
			col++
		case r >= 0x80 && r <= 0x9f:
			b.WriteRune(0xfffd) // C1 controls have no picture; show them as unknown
			col++
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

func isSourceHazard(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// contMark prefixes a wrapped continuation row. Two display columns wide.
var contMark = stDim.Render("↪") + " "

// wrapSource hard-splits one source line at w columns, preserving every
// character (no whitespace collapsing — leading whitespace is what makes a
// folded header a continuation). Rows after the first carry contMark.
func wrapSource(line string, w int) []string {
	if w < 8 {
		w = 8
	}
	r := []rune(line)
	if len(r) <= w {
		return []string{line}
	}
	out := []string{string(r[:w])}
	r = r[w:]
	cw := w - 2 // room for contMark
	for len(r) > cw {
		out = append(out, contMark+string(r[:cw]))
		r = r[cw:]
	}
	return append(out, contMark+string(r))
}
