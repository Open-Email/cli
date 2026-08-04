package tui

import (
	"context"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	focusSidebar = iota
	focusContent
)

// rootModel owns the frame: title bar, sidebar, the content pane stack, and
// the status bar. All async messages are routed to the top pane only.
type rootModel struct {
	ctx   context.Context
	ui    Options
	side  sidebar
	descs map[string]resourceDesc
	stack []pane
	focus int
	w, h  int
	ready bool
}

func newRoot(ctx context.Context, opts Options) *rootModel {
	descs := allDescriptors()
	items := []sideItem{
		{key: "mailboxes", label: "Mailboxes"},
		{key: "domains", label: "Domains"},
		{key: "traffic", label: "Traffic"},
		{key: "routes", label: "Routes"},
		{key: "groups", label: "Groups"},
		{key: "patterns", label: "Patterns"},
		{key: "keys", label: "API Keys"},
	}
	// System-only screens. Suppressions and DKIM are refused with
	// system_credentials_required for anyone else, so offering them would be a
	// menu entry that can only ever answer 403.
	if opts.Role == coreapi.PrincipalSystem {
		items = append(items,
			sideItem{key: "accounts", label: "Accounts"},
			sideItem{key: "suppressions", label: "Suppressions"},
			sideItem{key: "dkim", label: "DKIM"},
		)
	}
	m := &rootModel{
		ctx:   ctx,
		ui:    opts,
		descs: descs,
		side:  sidebar{items: items},
		focus: focusContent,
	}
	m.stack = []pane{newScreenPane(ctx, &m.ui, descs[items[0].key])}
	return m
}

func (m *rootModel) Init() tea.Cmd { return m.top().init() }

func (m *rootModel) top() pane { return m.stack[len(m.stack)-1] }

func (m *rootModel) closeAll() {
	for _, p := range m.stack {
		p.close()
	}
}

// contentSize is the box available to the top pane.
func (m *rootModel) contentSize() (w, h int) {
	return m.w - m.side.w, m.h - 2 // title + status bars
}

func (m *rootModel) sizeTop() {
	w, h := m.contentSize()
	m.top().setSize(w, h)
}

func (m *rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.ready = true
		sw := 20
		if m.w < 90 {
			sw = 16
		}
		if sw > m.w/4 {
			sw = m.w / 4
		}
		m.side.w, m.side.h = sw, m.h-2
		m.sizeTop()
		return m, nil

	case pushPaneMsg:
		m.stack = append(m.stack, msg.p)
		m.sizeTop()
		return m, msg.p.init()

	case popPaneMsg:
		if len(m.stack) > 1 {
			m.top().close()
			m.stack = m.stack[:len(m.stack)-1]
			m.sizeTop()
		} else {
			m.focus = focusSidebar
			m.side.focused = true
		}
		return m, nil

	case popRefreshMsg:
		// A form/confirm completed: pop it, then hand the revealed listing a
		// refresh with the success notice.
		if len(m.stack) > 1 {
			m.top().close()
			m.stack = m.stack[:len(m.stack)-1]
			m.sizeTop()
		}
		return m.forwardTop(refreshMsg(msg))

	case tea.KeyMsg:
		return m.updateKey(msg)
	}

	// Async results (fetch pages, spinner ticks, live events) go to the top
	// pane; stale ones are dropped there by pane id.
	return m.forwardTop(msg)
}

func (m *rootModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.focus == focusContent && m.top().capturesInput() {
		return m.forwardTop(msg)
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "tab", "shift+tab":
		m.focus = 1 - m.focus
		m.side.focused = m.focus == focusSidebar
		return m, nil
	}

	if m.focus == focusSidebar {
		switch msg.String() {
		case "up", "k":
			m.side.move(-1)
		case "down", "j":
			m.side.move(1)
		case "enter", "right", "l":
			m.focus = focusContent
			m.side.focused = false
			return m, m.openScreen(m.side.cursor)
		}
		return m, nil
	}

	// Content focused: left at the top of the stack hops to the sidebar; every
	// other key (incl. esc → popPaneMsg) belongs to the pane.
	if msg.String() == "left" && len(m.stack) == 1 {
		m.focus = focusSidebar
		m.side.focused = true
		return m, nil
	}
	return m.forwardTop(msg)
}

// openScreen replaces the whole stack with the sidebar item's listing. If the
// item is already open at the stack root, it is kept as-is (no refetch).
func (m *rootModel) openScreen(idx int) tea.Cmd {
	if idx == m.side.active && len(m.stack) == 1 {
		return nil
	}
	m.closeAll()
	m.side.active = idx
	d := m.descs[m.side.items[idx].key]
	sp := newScreenPane(m.ctx, &m.ui, d)
	m.stack = []pane{sp}
	m.sizeTop()
	return sp.init()
}

func (m *rootModel) forwardTop(msg tea.Msg) (tea.Model, tea.Cmd) {
	p, cmd := m.top().update(msg)
	m.stack[len(m.stack)-1] = p
	return m, cmd
}

func (m *rootModel) View() string {
	if !m.ready {
		return "starting…"
	}
	title := m.titleBar()
	status := m.statusBar()
	content := m.top().view()
	cw, ch := m.contentSize()
	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		m.side.view(),
		lipgloss.NewStyle().Width(cw).Height(ch).MaxWidth(cw).MaxHeight(ch).Render(content),
	)
	return title + "\n" + body + "\n" + status
}

func (m *rootModel) titleBar() string {
	crumbs := make([]string, 0, len(m.stack))
	for _, p := range m.stack {
		crumbs = append(crumbs, p.title())
	}
	left := " " + stAppName.Render("OpenEmail") + "  " + stCrumb.Render(strings.Join(crumbs, " › "))
	role := m.ui.Role
	right := stMeta.Render(m.ui.Profile+" · "+role+" · v"+m.ui.Version) + " "
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, m.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *rootModel) statusBar() string {
	var hints string
	if m.focus == focusSidebar {
		hints = "↑/↓ navigate · enter open · tab content · q quit"
	} else {
		hints = m.top().hints() + " · tab sidebar · q quit"
	}
	return stStatus.Render(truncate(" "+hints, m.w))
}
