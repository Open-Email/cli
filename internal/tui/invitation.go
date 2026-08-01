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

// maxIcsBytes bounds what the console will read into memory from a calendar
// part. A scheduling message is kilobytes; anything past this is not something
// a terminal pane can usefully show, and reading it would only stall the UI.
const maxIcsBytes = 1 << 20 // 1 MiB

// calendarPart finds the scheduling part of a message, if it has one. Only a
// non-inline text/calendar part counts: an inline one is decoration inside a
// body, and treating it as an invitation would offer to RSVP to a quoted copy
// in a forwarded thread.
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

// icalInfo is the handful of properties worth showing before someone answers an
// invitation. This is deliberately a display-only scan, not a parser: the
// authoritative reading of the object happens server-side, and the bytes are
// posted back verbatim rather than re-serialized from this.
type icalInfo struct {
	method    string
	uid       string
	summary   string
	organizer string
	location  string
	dtStart   string
	dtEnd     string
}

// scanICS reads the properties above out of an iCalendar object, honouring RFC
// 5545 line folding (a continuation line begins with a space or tab).
func scanICS(s string) icalInfo {
	var info icalInfo
	var lines []string
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += raw[1:]
			continue
		}
		lines = append(lines, raw)
	}
	set := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	inTZ := false
	for _, l := range lines {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		// A VTIMEZONE's STANDARD/DAYLIGHT rules each carry their OWN DTSTART — a
		// 1970 rule date — and every mainstream producer (Google, Outlook, Apple)
		// emits VTIMEZONE BEFORE the VEVENT. With first-occurrence-wins those rule
		// dates would be read as the meeting time, so a zoned invitation rendered
		// "When: 1970-03-29 02:00" and the user answered against it. Nothing this
		// scan wants lives in a VTIMEZONE; skip it whole.
		//
		// Skipping only the timezone (rather than restricting to VEVENT/VTODO) is
		// deliberate: METHOD is a VCALENDAR-level property, and both respond() and
		// hints() gate on it. A plain bool suffices — VTIMEZONE is top-level and
		// never nests, and its subcomponents' BEGIN/END lines fall inside the span.
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "BEGIN":
			if strings.EqualFold(strings.TrimSpace(value), "VTIMEZONE") {
				inTZ = true
			}
		case "END":
			if strings.EqualFold(strings.TrimSpace(value), "VTIMEZONE") {
				inTZ = false
			}
		}
		if inTZ {
			continue
		}
		// A property name may carry parameters (DTSTART;TZID=…), so compare on
		// the part before the first semicolon.
		prop, params, _ := strings.Cut(name, ";")
		switch strings.ToUpper(strings.TrimSpace(prop)) {
		case "METHOD":
			set(&info.method, strings.ToUpper(strings.TrimSpace(value)))
		case "UID":
			set(&info.uid, strings.TrimSpace(value))
		case "SUMMARY":
			set(&info.summary, unescapeICSText(value))
		case "LOCATION":
			set(&info.location, unescapeICSText(value))
		case "ORGANIZER":
			set(&info.organizer, strings.TrimPrefix(strings.TrimSpace(value), "mailto:"))
		case "DTSTART":
			set(&info.dtStart, formatICSTime(strings.TrimSpace(value), params))
		case "DTEND":
			set(&info.dtEnd, formatICSTime(strings.TrimSpace(value), params))
		}
	}
	return info
}

// unescapeICSText reverses RFC 5545 §3.3.11 text escaping.
func unescapeICSText(v string) string {
	r := strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`)
	return strings.TrimSpace(r.Replace(v))
}

// formatICSTime renders a DATE-TIME for reading. A UTC value (trailing Z) is
// shown in local time; a floating or zoned one is shown as written, because
// resolving a TZID needs the VTIMEZONE this scan does not read — displaying it
// in the reader's own zone would be a guess presented as a fact.
func formatICSTime(v, params string) string {
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return t.Local().Format("2006-01-02 15:04") + " (local)"
	}
	if t, err := time.Parse("20060102T150405", v); err == nil {
		suffix := ""
		if tzid := icsParam(params, "TZID"); tzid != "" {
			suffix = " " + tzid
		}
		return t.Format("2006-01-02 15:04") + suffix
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t.Format("2006-01-02") + " (all day)"
	}
	return v
}

func icsParam(params, name string) string {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// ── the pane ─────────────────────────────────────────────────────────────────

type invLoadedMsg struct {
	paneID int64
	ics    string
	info   icalInfo
	status *coreapi.PimInvitationStatus
	err    error
}

type invRsvpMsg struct {
	paneID int64
	res    *coreapi.PimRsvpResult
	err    error
}

// invitationPane shows one scheduling message and answers it. The RSVP posts
// the part back VERBATIM: core patches the calendar copy and tells the
// organizer by exactly one route (a local organizer's copy is patched in place,
// a remote one is mailed a REPLY), so the console never has to decide which.
type invitationPane struct {
	ctx       context.Context
	ui        *Options
	mailboxID string
	messageID string
	section   string
	id        int64

	spin       spinner.Model
	vp         viewport.Model
	loading    bool
	submitting bool
	errMsg     string
	flash      string

	ics    string
	info   icalInfo
	status *coreapi.PimInvitationStatus

	w, h int
}

func newInvitationPane(ctx context.Context, ui *Options, mailboxID, messageID, section string) *invitationPane {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = stDim
	return &invitationPane{
		ctx: ctx, ui: ui, mailboxID: mailboxID, messageID: messageID,
		section: section, id: nextPaneID(), spin: sp, vp: viewport.New(0, 0),
	}
}

func (p *invitationPane) init() tea.Cmd {
	p.loading = true
	id, ui := p.id, p.ui
	mailboxID, messageID, section := p.mailboxID, p.messageID, p.section
	ctx := p.ctx
	return tea.Batch(p.spin.Tick, func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		body, _, err := ui.Client.GetPart(cctx, mailboxID, messageID, section, false)
		if err != nil {
			return invLoadedMsg{paneID: id, err: err}
		}
		defer body.Close()
		raw, err := io.ReadAll(io.LimitReader(body, maxIcsBytes))
		if err != nil {
			return invLoadedMsg{paneID: id, err: err}
		}
		ics := string(raw)
		info := scanICS(ics)
		// The status lookup is a courtesy — it says whether this event is
		// already in a calendar and how it was answered. A failure (no PIM
		// store, an unfiled invitation) must not stop the RSVP itself.
		var st *coreapi.PimInvitationStatus
		if info.uid != "" {
			if s, serr := ui.Client.InvitationStatus(cctx, mailboxID, info.uid); serr == nil {
				st = s
			}
		}
		return invLoadedMsg{paneID: id, ics: ics, info: info, status: st}
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
			p.errMsg = msg.err.Error()
		} else {
			p.ics, p.info, p.status = msg.ics, msg.info, msg.status
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
		if msg.res != nil {
			// Reflect the answer locally so the pane agrees with what was just
			// done, without a second round trip.
			ps := msg.res.Partstat
			if p.status == nil {
				p.status = &coreapi.PimInvitationStatus{Found: msg.res.Filed}
			}
			p.status.MyPartstat = &ps
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

// respond answers the invitation. It refuses locally when there is nothing to
// answer, rather than posting an empty body for the server to reject.
func (p *invitationPane) respond(partstat string) tea.Cmd {
	if p.loading || strings.TrimSpace(p.ics) == "" {
		return nil
	}
	if p.info.method == "REPLY" || p.info.method == "CANCEL" {
		p.errMsg = "this is a " + p.info.method + ", not an invitation — there is nothing to answer"
		p.setContent()
		return nil
	}
	p.submitting = true
	p.errMsg, p.flash = "", ""
	p.setContent()
	id, client, ctx := p.id, p.ui.Client, p.ctx
	mailboxID, ics := p.mailboxID, p.ics
	return tea.Batch(p.spin.Tick, func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		res, err := client.RespondToInvitation(cctx, mailboxID, ics, partstat)
		return invRsvpMsg{paneID: id, res: res, err: err}
	})
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
	if strings.TrimSpace(p.ics) == "" {
		p.vp.SetContent(b.String())
		return
	}

	row := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "  %s%s\n", stKey.Render(fmt.Sprintf("%-11s", k)), stVal.Render(truncate(v, max(10, w-16))))
	}
	method := p.info.method
	if method == "" {
		method = "PUBLISH"
	}
	row("Event", strOrEmptyText(p.info.summary, "(no summary)"))
	row("When", p.info.dtStart)
	row("Until", p.info.dtEnd)
	row("Where", p.info.location)
	row("Organizer", p.info.organizer)
	row("Method", method)

	fmt.Fprintf(&b, "\n%s\n", stDim.Render(strings.Repeat("─", max(4, w-2))))
	switch {
	case p.status != nil && p.status.MyPartstat != nil:
		fmt.Fprintf(&b, "  %s\n", stLive.Render("You answered: "+*p.status.MyPartstat))
	case p.status != nil && p.status.Found:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("This event is in your calendar; you have not answered."))
	case p.status != nil:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("Not in your calendar yet — answering files a copy."))
	default:
		fmt.Fprintf(&b, "  %s\n", stDim.Render("Calendar status unavailable — answering still works."))
	}
	if p.status != nil && p.status.MyAddress != nil {
		fmt.Fprintf(&b, "  %s\n", stDim.Render("Answering as "+*p.status.MyAddress))
	}
	if p.flash != "" {
		fmt.Fprintf(&b, "\n  %s\n", stLive.Render(truncate(p.flash, max(10, w-4))))
	}
	if method == "CANCEL" {
		fmt.Fprintf(&b, "\n  %s\n", stErr.Render("This message cancels the event."))
	}
	p.vp.SetContent(b.String())
}

func (p *invitationPane) view() string {
	head := " " + stTitle.Render(truncate(strOrEmptyText(p.info.summary, "Invitation"), max(10, p.w-20)))
	if p.loading || p.submitting {
		head += "  " + p.spin.View()
	}
	return head + "\n" + p.vp.View()
}

func (p *invitationPane) title() string {
	return truncate(strOrEmptyText(p.info.summary, "Invitation"), 32)
}

func (p *invitationPane) hints() string {
	if p.info.method == "REPLY" || p.info.method == "CANCEL" {
		return "↑/↓ scroll · esc back"
	}
	return "a accept · x decline · t tentative · ↑/↓ scroll · esc back"
}

func (p *invitationPane) capturesInput() bool { return false }
func (p *invitationPane) close()              {}

// strOrEmptyText is strOr for a plain string rather than a pointer.
func strOrEmptyText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
