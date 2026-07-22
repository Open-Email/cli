package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// sideItem is one sidebar entry; key names the resource descriptor it opens.
type sideItem struct {
	key   string
	label string
}

// sidebar is the left navigation rail. cursor is the highlighted line; active
// is the item whose screen is currently open in the content pane.
type sidebar struct {
	items   []sideItem
	cursor  int
	active  int
	focused bool
	w, h    int // total box including the right border column
}

func (s *sidebar) move(delta int) {
	s.cursor += delta
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.cursor >= len(s.items) {
		s.cursor = len(s.items) - 1
	}
}

func (s *sidebar) view() string {
	inner := s.w - 1 // right border column
	if inner < 4 {
		inner = 4
	}
	lines := make([]string, 0, len(s.items))
	for i, it := range s.items {
		label := truncate(it.label, inner-2)
		var st lipgloss.Style
		switch {
		case i == s.cursor && s.focused:
			st = stSideCursor
		case i == s.cursor:
			st = stSideCursorDim
		case i == s.active:
			st = stSideActive
		default:
			st = stSideItem
		}
		lines = append(lines, st.Width(inner).Render(label))
	}
	body := strings.Join(lines, "\n")
	return stSidebar.Width(inner).Height(s.h).Render(body)
}
