package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// The palette is adaptive (light/dark terminals). Everything visual lives here
// so panes stay layout-only.
var (
	cAccent = lipgloss.AdaptiveColor{Light: "26", Dark: "39"}
	cOnAcc  = lipgloss.AdaptiveColor{Light: "231", Dark: "231"}
	cText   = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	cDim    = lipgloss.AdaptiveColor{Light: "245", Dark: "241"}
	cBad    = lipgloss.AdaptiveColor{Light: "124", Dark: "203"}
	cGood   = lipgloss.AdaptiveColor{Light: "28", Dark: "78"}
	cSubtle = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
)

var (
	stAppName = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stCrumb   = lipgloss.NewStyle().Foreground(cText)
	stMeta    = lipgloss.NewStyle().Foreground(cDim)
	stStatus  = lipgloss.NewStyle().Foreground(cDim)
	stErr     = lipgloss.NewStyle().Foreground(cBad)
	stLive    = lipgloss.NewStyle().Foreground(cGood)
	stDim     = lipgloss.NewStyle().Foreground(cDim)
	stTitle   = lipgloss.NewStyle().Bold(true)
	stKey     = lipgloss.NewStyle().Foreground(cDim)
	stVal     = lipgloss.NewStyle().Foreground(cText)
	stSection = lipgloss.NewStyle().Bold(true).Foreground(cAccent)

	stSidebar       = lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderRight(true).BorderForeground(cSubtle)
	stSideItem      = lipgloss.NewStyle().Foreground(cText).Padding(0, 1)
	stSideActive    = lipgloss.NewStyle().Foreground(cAccent).Bold(true).Padding(0, 1)
	stSideCursor    = lipgloss.NewStyle().Foreground(cOnAcc).Background(cAccent).Padding(0, 1)
	stSideCursorDim = lipgloss.NewStyle().Foreground(cText).Background(cSubtle).Padding(0, 1)
)

// newTable builds a table with the console keymap: half/full-page scrolling
// moves to ctrl+d/ctrl+u and pgup/pgdn so plain letters (d, u, f, b, n, e, t)
// stay free for pane actions.
func newTable() table.Model {
	t := table.New(table.WithFocused(true))
	t.SetStyles(tableStyles(true))
	km := table.DefaultKeyMap()
	km.LineUp = key.NewBinding(key.WithKeys("up", "k"))
	km.LineDown = key.NewBinding(key.WithKeys("down", "j"))
	km.PageUp = key.NewBinding(key.WithKeys("pgup"))
	km.PageDown = key.NewBinding(key.WithKeys("pgdown"))
	km.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"))
	km.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
	km.GotoTop = key.NewBinding(key.WithKeys("home", "g"))
	km.GotoBottom = key.NewBinding(key.WithKeys("end", "G"))
	t.KeyMap = km
	return t
}

// formTheme adapts huh's base theme to the console palette.
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = t.Focused.Title.Foreground(cAccent).Bold(true)
	t.Blurred.Title = t.Blurred.Title.Foreground(cDim)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(cOnAcc).Background(cAccent)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(cBad)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(cBad)
	t.Focused.Description = t.Focused.Description.Foreground(cDim)
	t.Blurred.Description = t.Blurred.Description.Foreground(cDim)
	return t
}

// tableStyles returns the shared table look; the selected row dims when the
// pane loses focus so the eye tracks the active pane.
func tableStyles(focused bool) table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(cSubtle).
		BorderBottom(true).
		Bold(true).
		Foreground(cDim)
	if focused {
		s.Selected = s.Selected.Foreground(cOnAcc).Background(cAccent).Bold(false)
	} else {
		s.Selected = s.Selected.Foreground(cText).Background(cSubtle).Bold(false)
	}
	return s
}
