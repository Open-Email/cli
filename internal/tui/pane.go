package tui

import (
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
)

// pane is one content view (a listing, a detail, a live tail). Panes stack:
// drill-downs push, esc pops. Async results are routed to the TOP pane only,
// so every fetch message carries its pane's id and panes drop foreign ids.
type pane interface {
	init() tea.Cmd
	update(msg tea.Msg) (pane, tea.Cmd)
	view() string
	setSize(w, h int)
	title() string
	hints() string       // status-bar key help
	capturesInput() bool // true while typing (filter) — root suspends q/tab
	close()              // release goroutines (event subscriptions); idempotent
}

// pushPaneMsg / popPaneMsg are emitted by panes to navigate the stack.
type pushPaneMsg struct{ p pane }
type popPaneMsg struct{}

func pushPane(p pane) tea.Cmd { return func() tea.Msg { return pushPaneMsg{p: p} } }
func popPane() tea.Msg        { return popPaneMsg{} }

// popRefreshMsg pops the top pane (a completed form/confirm) and asks the
// revealed pane to re-fetch, showing flash as a success notice. The root turns
// it into a refreshMsg delivered to the new top.
type popRefreshMsg struct{ flash string }
type refreshMsg struct{ flash string }

func popRefresh(flash string) tea.Cmd { return func() tea.Msg { return popRefreshMsg{flash: flash} } }

// paneIDs tags async results to their owning pane instance, so a result that
// lands after the user navigated away is dropped instead of misapplied.
var paneIDs atomic.Int64

func nextPaneID() int64 { return paneIDs.Add(1) }
