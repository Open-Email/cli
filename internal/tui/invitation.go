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

// calendarPart reports whether a message carries a scheduling part, from the
// content view the preview pane has already fetched. It decides only whether to
// show the banner — everything about the invitation itself comes from the
// server, which is the one authority on what the part actually says.
//
// Only a non-inline text/calendar part counts: an inline one is decoration
// inside a body, and treating it as an invitation would offer to RSVP to a
// forwarded thread's quoted copy.
func calendarPart(c *coreapi.ContentResult) *coreapi.AttachmentRef {
	if c == nil {
		return nil
	}
	for i := range c.Attachments {
		a := c.Attachments[i]
		if a.Inline {
			continue
		}
		base, _, _ := strings.Cut(strings.ToLower(a.ContentType), ";")
		if strings.TrimSpace(base) == "text/calendar" {
			return &c.Attachments[i]
		}
	}
	return nil
}

// ── the pane ─────────────────────────────────────────────────────────────────

type invLoadedMsg struct {
	paneID int64
	inv    *coreapi.MessageInvitation
	err    error
}

type invRsvpMsg struct {
	paneID int64
	res    *coreapi.PimRsvpResult
	err    error
}

// invitationPane shows the scheduling part a message carries, and answers it.
//
// Both halves are ONE server call each. The read is
// `GET /messages/:id/invitation`, which locates the part, parses the iCalendar,
// and merges in what this mailbox has already filed and answered; the write
// names the message rather than posting bytes back, so the console never
// re-uploads a part the server already holds. Nothing here parses RFC 5545 —
// that belonged to no terminal client, and the hand-rolled version read a
// VTIMEZONE rule date as the meeting time.
//
// The RSVP tells the organizer by exactly one route (their copy is patched when
// they are local, a METHOD:REPLY is mailed when they are not), so the pane never
// has to decide which.
type invitationPane struct {
	ctx       context.Context
	ui        *Options
	mailboxID string
	messageID string
	id        int64

	spin       spinner.Model
	vp         viewport.Model
	loading    bool
	submitting bool
	errMsg     string
	flash      string

	inv *coreapi.MessageInvitation

	w, h int
}

func newInvitationPane(ctx context.Context, ui *Options, mailboxID, messageID string) *invitationPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &invitationPane{
		ctx: ctx, ui: ui, mailboxID: mailboxID, messageID: messageID,
		id: nextPaneID(), spin: sp, vp: viewport.New(0, 0),
	}
}

func (p *invitationPane) init() tea.Cmd {
	p.loading = true
	id, client, ctx := p.id, p.ui.Client, p.ctx
	mailboxID, messageID := p.mailboxID, p.messageID
	return tea.Batch(p.spin.Tick, func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		inv, err := client.GetMessageInvitation(cctx, mailboxID, messageID)
		return invLoadedMsg{paneID: id, inv: inv, err: err}
	})
}

func (p *invitationPane) update(msg tea.Msg) (pane, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !p.loading && !p.submitting {
			return p, nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return p, cmd

	case invLoadedMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.loading = false
		if msg.err != nil {
			// `no_invitation` is the ordinary answer for ordinary mail. It should
			// be unreachable here (the banner is drawn from the same content
			// view), but a message re-parsed differently must read as "nothing to
			// show" rather than as a failure.
			if coreapi.IsNotFound(msg.err) {
				p.errMsg = "this message carries no invitation"
			} else {
				p.errMsg = msg.err.Error()
			}
		} else {
			p.inv = msg.inv
		}
		p.setContent()
		return p, nil

	case invRsvpMsg:
		if msg.paneID != p.id {
			return p, nil
		}
		p.submitting = false
		if msg.err != nil {
			p.errMsg = msg.err.Error()
			p.setContent()
			return p, nil
		}
		p.errMsg = ""
		p.flash = rsvpOutcome(msg.res)
		if msg.res != nil && p.inv != nil {
			// Reflect the answer locally so the pane agrees with what was just
			// done, without a second round trip.
			ps := msg.res.Partstat
			p.inv.MyPartstat = &ps
			p.inv.Found = true
		}
		p.setContent()
		return p, nil

	case tea.KeyMsg:
		if p.submitting {
			return p, nil // modal while the answer is in flight
		}
		switch msg.String() {
		case "esc":
			return p, popPane
		case "a":
			return p, p.respond("ACCEPTED")
		// `x`, NOT `d`: this pane forwards unhandled keys to a viewport whose
		// default keymap binds half-page-down to `d` (and half-page-up to `u`).
		// The preview pane one level up scrolls with those keys, so a reader who
		// pressed `i` and kept scrolling out of habit would have posted a DECLINE
		// — mailed to the organizer, with no undo. a/x/t are all outside the
		// viewport keymap, and d/u keep scrolling here exactly as they do there.
		case "x":
			return p, p.respond("DECLINED")
		case "t":
			return p, p.respond("TENTATIVE")
		}
		var cmd tea.Cmd
		p.vp, cmd = p.vp.Update(msg)
		return p, cmd
	}
	return p, nil
}

// respond answers the invitation, refusing locally whenever the server has
// already said it would not accept one. `canRespond` is that endpoint's own
// predicate, so a pane that gates on it cannot offer a button the write then
// rejects — which is exactly what a client guessing from the METHOD alone did.
func (p *invitationPane) respond(partstat string) tea.Cmd {
	if p.loading || p.inv == nil {
		return nil
	}
	if !p.inv.CanRespond {
		p.errMsg = cannotRespondReason(p.inv)
		p.setContent()
		return nil
	}
	p.submitting = true
	p.errMsg, p.flash = "", ""
	p.setContent()
	id, client, ctx := p.id, p.ui.Client, p.ctx
	mailboxID, messageID, section := p.mailboxID, p.messageID, p.inv.Section
	return tea.Batch(p.spin.Tick, func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res, err := client.RespondToMessageInvitation(cctx, mailboxID, messageID, section, partstat)
		return invRsvpMsg{paneID: id, res: res, err: err}
	})
}

// cannotRespondReason turns the server's refusal into the specific sentence a
// reader needs, rather than a bare "not allowed".
func cannotRespondReason(inv *coreapi.MessageInvitation) string {
	switch {
	case inv.IsOrganizer:
		return "you are the organizer — answer by editing the event in your calendar"
	case inv.MyAddress == nil:
		return "this invitation does not name any of your addresses as an attendee"
	case inv.Method != nil && *inv.Method == "REPLY":
		return "this is someone else's reply, not an invitation — there is nothing to answer"
	case inv.Method != nil && *inv.Method == "CANCEL":
		return "this message cancels the event — there is nothing to answer"
	default:
		return "this invitation cannot be answered from here"
	}
}

// rsvpOutcome states which of the two mutually exclusive notification routes
// core took. They cannot both happen, and saying which one did is the
// difference between "the organizer knows" and "a mail is on its way".
func rsvpOutcome(res *coreapi.PimRsvpResult) string {
	if res == nil {
		return "answered"
	}
	switch {
	case res.OrganizerUpdated:
		return res.Partstat + " — the organizer's copy was updated directly"
	case res.ReplySent:
		return res.Partstat + " — a reply was sent to the organizer"
	default:
		return res.Partstat + " — your copy was updated; the organizer was not notified"
	}
}

func (p *invitationPane) setSize(w, h int) {
	p.w, p.h = w, h
	p.vp.Width = w - 2
	p.vp.Height = h - 1
	p.setContent()
}

func (p *invitationPane) setContent() {
	w := p.vp.Width
	var b strings.Builder
	if p.errMsg != "" {
		fmt.Fprintf(&b, "  %s\n\n", stErr.Render(truncate(p.errMsg, max(10, w-4))))
	}
	if p.loading {
		p.vp.SetContent(b.String() + "  " + stDim.Render("reading the invitation…"))
		return
	}
	if p.inv == nil {
		p.vp.SetContent(b.String())
		return
	}
	inv := p.inv

	row := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "  %s%s\n", stKey.Render(fmt.Sprintf("%-11s", k)), stVal.Render(truncate(v, max(10, w-16))))
	}
	row("Event", strOr(inv.Summary, "(no summary)"))
	row("When", invWhen(inv))
	row("Where", strOr(inv.Location, ""))
	row("Organizer", strOr(inv.Organizer, ""))
	if inv.RRule != nil {
		// The literal "RDATE" marks a series recurring by explicit dates only.
		if *inv.RRule == "RDATE" {
			row("Repeats", "on set dates")
		} else {
			row("Repeats", *inv.RRule)
		}
	}
	if inv.AttendeeCount > 0 {
		row("Attendees", invAttendees(inv))
	}
	if inv.Method != nil {
		row("Method", *inv.Method)
	}

	fmt.Fprintf(&b, "\n%s\n", stDim.Render(strings.Repeat("─", max(4, w-2))))
	switch {
	case inv.MyPartstat != nil:
		fmt.Fprintf(&b, "  %s\n", stLive.Render("You answered: "+*inv.MyPartstat))
	case inv.Found:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("This event is in your calendar; you have not answered."))
	default:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("Not in your calendar yet — answering files a copy."))
	}
	if inv.MyAddress != nil {
		fmt.Fprintf(&b, "  %s\n", stDim.Render("Answering as "+*inv.MyAddress))
	}
	// Say WHY there are no buttons, rather than leaving a reader hunting for
	// keys the status bar does not offer.
	if !inv.CanRespond {
		fmt.Fprintf(&b, "  %s\n", stDim.Render(cannotRespondReason(inv)))
	}
	if inv.Method != nil && *inv.Method == "CANCEL" {
		fmt.Fprintf(&b, "\n  %s\n", stErr.Render("This message cancels the event."))
	}
	if p.flash != "" {
		fmt.Fprintf(&b, "\n  %s\n", stLive.Render(truncate(p.flash, max(10, w-4))))
	}
	p.vp.SetContent(b.String())
}

// invWhen renders the start (and end) the SERVER resolved — epoch seconds, with
// the object's timezone already applied, so there is no VTIMEZONE to interpret.
func invWhen(inv *coreapi.MessageInvitation) string {
	if inv.DTStart == nil {
		return ""
	}
	when := fmtEpoch(*inv.DTStart)
	if inv.DTEnd != nil {
		when += " → " + fmtEpoch(*inv.DTEnd)
	}
	return when
}

// invAttendees summarizes the guest list, keeping the real total visible when
// the list itself was truncated to the iTIP fan-out bound.
func invAttendees(inv *coreapi.MessageInvitation) string {
	names := make([]string, 0, len(inv.Attendees))
	for _, a := range inv.Attendees {
		name := a.Email
		if a.CN != "" {
			name = a.CN
		}
		// NEEDS-ACTION is the default and says nothing; only a real answer is
		// worth the width.
		if a.Partstat != "" && a.Partstat != "NEEDS-ACTION" {
			name += " (" + strings.ToLower(a.Partstat) + ")"
		}
		names = append(names, name)
	}
	s := strings.Join(names, ", ")
	if int64(len(inv.Attendees)) < inv.AttendeeCount {
		s += fmt.Sprintf(" … %d total", inv.AttendeeCount)
	}
	return s
}

func (p *invitationPane) view() string {
	head := " " + stTitle.Render(truncate(p.title(), max(10, p.w-20)))
	if p.loading || p.submitting {
		head += "  " + p.spin.View()
	}
	return head + "\n" + p.vp.View()
}

func (p *invitationPane) title() string {
	if p.inv != nil {
		return truncate(strOr(p.inv.Summary, "Invitation"), 32)
	}
	return "Invitation"
}

func (p *invitationPane) hints() string {
	// The buttons are advertised iff the server would accept them.
	if p.inv != nil && p.inv.CanRespond {
		return "a accept · x decline · t tentative · ↑/↓ scroll · esc back"
	}
	return "↑/↓ scroll · esc back"
}

func (p *invitationPane) capturesInput() bool { return false }
func (p *invitationPane) close()              {}
