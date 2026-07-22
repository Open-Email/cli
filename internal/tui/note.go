package tui

import (
	"fmt"
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
		// A reveal note sits over the listing of the resource just created; its
		// refetch was issued while this note was on top, so its result was routed
		// here and dropped. Refresh the revealed listing on dismiss (when it is
		// top again) so the new row appears without a manual refresh.
		return p, popRefresh("")
	}
	return p, nil
}

func (p *notePane) setSize(w, h int) { p.w, p.h = w, h }

func (p *notePane) view() string {
	var b strings.Builder
	fmt.Fprintf(&b, " %s\n\n", stTitle.Render(p.heading))
	for _, l := range p.lines {
		fmt.Fprintf(&b, "%s\n", indent(wrapPlain(l, max(20, p.w-4)), "  "))
	}
	return b.String()
}

func (p *notePane) title() string       { return p.heading }
func (p *notePane) hints() string       { return "esc back" }
func (p *notePane) capturesInput() bool { return true }
func (p *notePane) close()              {}
