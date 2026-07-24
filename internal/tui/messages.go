package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// msgsPageMsg is one fetched page of the mailbox's live messages.
type msgsPageMsg struct {
	paneID  int64
	seq     int
	items   []coreapi.MessageMeta
	next    string
	replace bool
	err     error
}

// mbxStatsMsg is the GetMailbox enrichment for the header line (best-effort).
type mbxStatsMsg struct {
	paneID int64
	mbx    *coreapi.Mailbox
}

// msgActMsg is a completed inline mutation (flag toggle).
type msgActMsg struct {
	paneID int64
	err    error
}

// labelsMsg caches the mailbox's labels for the label editor (best-effort).
type labelsMsg struct {
	paneID int64
	labels []coreapi.LabelInfo
}

// messagesPane lists one mailbox's live messages newest-first and tails the
// mailbox's events channel: any change signal triggers a first-page re-sync
// (events carry identifiers, never content — REST is the source of truth).
type messagesPane struct {
	ctx    context.Context
	cancel context.CancelFunc
	ui     *Options
	mbx    coreapi.Mailbox
	stats  *coreapi.Mailbox
	labels []coreapi.LabelInfo // for the label editor; fetched once at init
	id     int64
	seq    int

	tbl     table.Model
	spin    spinner.Model
	loading bool
	more    bool
	errMsg  string
	flash   string

	all  []coreapi.MessageMeta
	next string

	sub       *subscription
	liveTxt   string // "", "live", "connecting", or a terminal reason
	liveOK    bool
	refreshQd bool // an event arrived while a fetch was in flight

	w, h int
}

func newMessagesPane(parent context.Context, ui *Options, mbx coreapi.Mailbox) *messagesPane {
	ctx, cancel := context.WithCancel(parent)
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	t := newTable()
	return &messagesPane{
		ctx:    ctx,
		cancel: cancel,
		ui:     ui,
		mbx:    mbx,
		id:     nextPaneID(),
		tbl:    t,
		spin:   sp,
	}
}

func (p *messagesPane) init() tea.Cmd {
	p.loading = true
	p.liveTxt = "connecting"
	p.sub = subscribe(p.ctx, p.ui.APIURL, p.ui.Token, p.mbx.ID, p.id)
	return tea.Batch(p.spin.Tick, p.fetchCmd("", true), p.statsCmd(), p.labelsCmd(), p.sub.wait())
}

func (p *messagesPane) labelsCmd() tea.Cmd {
	id := p.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		labels, err := p.ui.Client.ListLabels(ctx, p.mbx.ID)
		if err != nil {
			return labelsMsg{paneID: id} // best-effort; `l` just stays inert
		}
		return labelsMsg{paneID: id, labels: labels}
	}
}

func (p *messagesPane) fetchCmd(cursor string, replace bool) tea.Cmd {
	p.seq++
	seq := p.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
		defer cancel()
		pg, err := p.ui.Client.ListMessages(ctx, p.mbx.ID, "", pageLimit, cursor)
		return msgsPageMsg{paneID: p.id, seq: seq, items: pg.Items, next: pg.NextCursor, replace: replace, err: err}
	}
}

func (p *messagesPane) statsCmd() tea.Cmd {
	id := p.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		mbx, err := p.ui.Client.GetMailbox(ctx, p.mbx.ID)
		if err != nil {
			return mbxStatsMsg{paneID: id} // best-effort; header just omits stats
		}
		return mbxStatsMsg{paneID: id, mbx: mbx}
	}
}

func (p *messagesPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.loading && !p.more {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case mbxStatsMsg:
		if msg.paneID == p.id && msg.mbx != nil {
			p.stats = msg.mbx
		}
		return p, nil

	case labelsMsg:
		if msg.paneID == p.id {
			p.labels = msg.labels
		}
		return p, nil

	case refreshMsg:
		p.flash = msg.flash
		if p.loading {
			p.refreshQd = true
			return p, nil
		}
		return p, p.refresh()

	case msgActMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		if msg.err != nil {
			p.errMsg = msg.err.Error()
			return p, nil
		}
		p.errMsg = ""
		if p.loading {
			p.refreshQd = true
			return p, nil
		}
		return p, p.refresh()

	case msgsPageMsg:
		if msg.paneID != p.id || msg.seq != p.seq {
			return p, nil
		}
		p.loading, p.more = false, false
		if msg.err != nil {
			p.errMsg = msg.err.Error()
			// A live event that arrived mid-fetch queued a re-sync; honor it even
			// though this fetch failed, so the list doesn't stay stale until the
			// next event or manual refresh. The retry clears errMsg on success.
			if p.refreshQd {
				p.refreshQd = false
				return p, p.refresh()
			}
			return p, nil
		}
		p.errMsg = ""
		if msg.replace {
			selected := p.selectedID()
			p.all = msg.items
			p.next = msg.next
			p.setRows()
			p.restoreCursor(selected)
		} else {
			p.all = append(p.all, msg.items...)
			p.next = msg.next
			p.setRows()
		}
		if p.refreshQd {
			p.refreshQd = false
			return p, p.refresh()
		}
		return p, nil

	case liveEvent:
		if msg.paneID != p.id {
			return p, nil
		}
		cmds := []tea.Cmd{p.sub.wait()}
		if p.loading || p.more {
			p.refreshQd = true
		} else if c := p.refresh(); c != nil {
			cmds = append(cmds, c)
		}
		return p, tea.Batch(cmds...)

	case liveState:
		if msg.paneID != p.id {
			return p, nil
		}
		p.liveOK = msg.connected
		p.liveTxt = msg.detail
		if msg.terminal {
			return p, nil // channel is closing; nothing to re-arm
		}
		return p, p.sub.wait()

	case tea.KeyMsg:
		return p.updateKey(msg)
	}
	return p, nil
}

// refresh re-syncs the first page (the events contract: a signal means "ask
// REST again"). Load-more depth is intentionally reset — newest-first, and the
// user is looking at the top when mail arrives.
func (p *messagesPane) refresh() tea.Cmd {
	p.loading = true
	return tea.Batch(p.spin.Tick, p.fetchCmd("", true))
}

func (p *messagesPane) updateKey(msg tea.KeyMsg) (pane, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return p, popPane
	case "r":
		if p.loading {
			return p, nil
		}
		p.flash = ""
		return p, p.refresh()
	case "m":
		if p.next == "" || p.more || p.loading {
			return p, nil
		}
		p.more = true
		return p, tea.Batch(p.spin.Tick, p.fetchCmd(p.next, false))
	case "enter":
		idx := p.tbl.Cursor()
		if idx < 0 || idx >= len(p.all) {
			return p, nil
		}
		return p, pushPane(newPreviewPane(p.ctx, p.ui, p.mbx.ID, p.all[idx]))
	case "t":
		return p, p.toggleFlagCmd("seen")
	case "!":
		return p, p.toggleFlagCmd("flagged")
	case "c":
		return p, pushPane(composeFormPane(p.ctx, p.ui, p.mbx))
	case "l":
		idx := p.tbl.Cursor()
		if idx < 0 || idx >= len(p.all) || len(p.labels) == 0 {
			return p, nil
		}
		return p, pushPane(labelEditFormPane(p.ctx, p.ui, p.mbx.ID, p.all[idx], p.labels))
	case "T":
		return p, pushPane(newScreenPane(p.ctx, p.ui, trashDesc(p.mbx)))
	case "d":
		idx := p.tbl.Cursor()
		if idx < 0 || idx >= len(p.all) {
			return p, nil
		}
		m := p.all[idx]
		return p, pushPane(newConfirmPane(p.ctx, p.ui, confirmSpec{
			title: "Move to trash",
			body: "Expunge \"" + strOr(m.Subject, "(no subject)") + "\": labels detach and the " +
				"message moves to trash, restorable for 14 days, then purges automatically. " +
				"An immediate purge (no undo) is CLI-only.",
			verb: "move to trash",
			submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
				if _, err := c.DeleteMessage(sctx, p.mbx.ID, m.ID, "", false); err != nil {
					return "", err
				}
				return "moved to trash", nil
			},
		}))
	}
	var cmd tea.Cmd
	p.tbl, cmd = p.tbl.Update(msg)
	if p.next != "" && !p.more && !p.loading && p.tbl.Cursor() == len(p.all)-1 {
		p.more = true
		return p, tea.Batch(cmd, p.spin.Tick, p.fetchCmd(p.next, false))
	}
	return p, cmd
}

// toggleFlagCmd flips one flag on the selected message; the follow-up refresh
// (plus the mailbox's own change event) re-syncs the row.
func (p *messagesPane) toggleFlagCmd(flag string) tea.Cmd {
	idx := p.tbl.Cursor()
	if idx < 0 || idx >= len(p.all) || p.loading {
		return nil
	}
	m := p.all[idx]
	in := coreapi.PatchInput{FlagsSet: []string{flag}}
	for _, f := range m.Flags {
		if f == flag {
			in = coreapi.PatchInput{FlagsClear: []string{flag}}
			break
		}
	}
	id := p.id
	client := p.ui.Client
	ctx := p.ctx
	mbxID := p.mbx.ID
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err := client.PatchMessage(cctx, mbxID, m.ID, in)
		return msgActMsg{paneID: id, err: err}
	}
}

func (p *messagesPane) selectedID() string {
	idx := p.tbl.Cursor()
	if idx >= 0 && idx < len(p.all) {
		return p.all[idx].ID
	}
	return ""
}

func (p *messagesPane) restoreCursor(id string) {
	if id == "" {
		return
	}
	for i, m := range p.all {
		if m.ID == id {
			p.tbl.SetCursor(i)
			return
		}
	}
	p.tbl.SetCursor(0)
}

func (p *messagesPane) setRows() {
	rows := make([]table.Row, len(p.all))
	for i, m := range p.all {
		rows[i] = table.Row{
			fmtEpoch(m.ReceivedAt),
			fmtMsgFlags(m.Flags),
			m.EnvelopeFrom,
			strOr(m.Subject, "(no subject)"),
			fmtBytes(m.Size),
		}
	}
	p.tbl.SetRows(rows)
	if p.tbl.Cursor() >= len(rows) {
		p.tbl.SetCursor(0)
	}
}

func (p *messagesPane) setSize(w, h int) {
	p.w, p.h = w, h
	inner := w - 2
	if inner < 20 {
		inner = 20
	}
	p.tbl.SetColumns(layoutColumns([]column{
		{title: "DATE", width: 16},
		{title: "FLAGS", width: 5},
		{title: "FROM", width: 26},
		{title: "SUBJECT", flex: true},
		{title: "SIZE", width: 9},
	}, inner))
	th := h - 1
	if th < 3 {
		th = 3
	}
	p.tbl.SetWidth(inner)
	p.tbl.SetHeight(th)
}

func (p *messagesPane) view() string {
	head := " " + stTitle.Render("Messages — "+p.title())
	if p.stats != nil && p.stats.MessageCount != nil {
		head += stMeta.Render(fmt.Sprintf("  %d msgs", *p.stats.MessageCount))
		if p.stats.UsedBytes != nil {
			head += stMeta.Render(" · " + fmtBytes(*p.stats.UsedBytes))
		}
	}
	if p.next != "" {
		head += stMeta.Render(" · more available")
	}
	switch {
	case p.liveOK:
		head += "  " + stLive.Render("● live")
	case p.liveTxt != "":
		head += "  " + stDim.Render("○ "+p.liveTxt)
	}
	if p.loading || p.more {
		head += "  " + p.spin.View()
	}
	if p.flash != "" {
		head += "  " + stLive.Render(truncate(p.flash, max(8, p.w-width(head)-4)))
	}
	if p.errMsg != "" {
		head += "  " + stErr.Render(truncate(p.errMsg, p.w-width(head)-4))
	}
	return head + "\n" + p.tbl.View()
}

func (p *messagesPane) title() string {
	if a := strOr(p.mbx.PrimaryAddress, ""); a != "" {
		return a
	}
	return p.mbx.ID
}

func (p *messagesPane) hints() string {
	h := "enter open · c compose · t read/unread · ! flag · l labels · d trash · T trash view · r refresh · esc back"
	if p.next != "" {
		h += " · m more"
	}
	return h
}

func (p *messagesPane) capturesInput() bool { return false }

func (p *messagesPane) close() {
	p.cancel()
}
