package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// notePane shows a few lines that must be read verbatim — the one-time API-key
// token reveal. Lines are wrapped, never truncated.
type notePane struct {
	heading string
	lines   []string
	w, h    int
}

func newNotePane(heading string, lines []string) *notePane {
	return &notePane{heading: heading, lines: lines}
}

func (p *notePane) init() tea.Cmd { return nil }

func (p *notePane) update(msg tea.Msg) (pane, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && (k.String() == "esc" || k.String() == "enter") {
		return p, popPane
	}
	return p, nil
}

func (p *notePane) setSize(w, h int) { p.w, p.h = w, h }

func (p *notePane) view() string {
	var b strings.Builder
	b.WriteString(" " + stTitle.Render(p.heading) + "\n\n")
	for _, l := range p.lines {
		b.WriteString(indent(wrapPlain(l, max(20, p.w-4)), "  ") + "\n")
	}
	return b.String()
}

func (p *notePane) title() string       { return p.heading }
func (p *notePane) hints() string       { return "esc back" }
func (p *notePane) capturesInput() bool { return true }
func (p *notePane) close()              {}
