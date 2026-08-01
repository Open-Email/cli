package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

// An inline text/calendar part is decoration inside a body, not something to
// RSVP to. Offering a banner for it would let a forwarded thread's quoted copy
// look like an invitation addressed to the reader.
func TestCalendarPartIgnoresInline(t *testing.T) {
	if got := calendarPart(nil); got != nil {
		t.Errorf("nil content must yield no part, got %+v", got)
	}
	c := &coreapi.ContentResult{Attachments: []coreapi.AttachmentRef{
		{Section: "2", ContentType: "text/calendar; method=REQUEST", Inline: true},
		{Section: "3", ContentType: "application/pdf"},
		{Section: "4", ContentType: "TEXT/CALENDAR; charset=utf-8"},
	}}
	got := calendarPart(c)
	if got == nil || got.Section != "4" {
		t.Fatalf("want the non-inline calendar part (section 4), got %+v", got)
	}
}

func TestCalendarPartAbsent(t *testing.T) {
	c := &coreapi.ContentResult{Attachments: []coreapi.AttachmentRef{
		{Section: "2", ContentType: "application/ics"}, // a lookalike, not text/calendar
	}}
	if got := calendarPart(c); got != nil {
		t.Errorf("want no part, got %+v", got)
	}
}

// RFC 5545 folds long lines by continuing them with a leading space; a scan
// that does not unfold reads a truncated UID and looks up the wrong event.
func TestScanICSUnfolds(t *testing.T) {
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"METHOD:REQUEST",
		"BEGIN:VEVENT",
		"UID:abc-123-very-long",
		" -continued",
		"SUMMARY:Weekly sync\\, standup",
		"ORGANIZER;CN=Ann:mailto:ann@x.test",
		"LOCATION:Room 1\\; upstairs",
		"DTSTART:20260801T090000Z",
		"DTEND:20260801T093000Z",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
	info := scanICS(ics)
	if info.uid != "abc-123-very-long-continued" {
		t.Errorf("uid = %q (unfolding failed)", info.uid)
	}
	if info.method != "REQUEST" {
		t.Errorf("method = %q", info.method)
	}
	if info.summary != "Weekly sync, standup" {
		t.Errorf("summary = %q (escaping not reversed)", info.summary)
	}
	if info.location != "Room 1; upstairs" {
		t.Errorf("location = %q", info.location)
	}
	// The mailto: prefix is transport, not the address a reader wants shown.
	if info.organizer != "ann@x.test" {
		t.Errorf("organizer = %q", info.organizer)
	}
	if !strings.Contains(info.dtStart, "(local)") {
		t.Errorf("a UTC DTSTART should render in local time, got %q", info.dtStart)
	}
}

// A VTIMEZONE's STANDARD/DAYLIGHT rules each carry their own DTSTART — a 1970
// rule date — and every mainstream producer emits VTIMEZONE BEFORE the VEVENT.
// With first-occurrence-wins and no component awareness, the pane showed the
// rule date as the meeting time and the user answered against it.
func TestScanICSSkipsVTimezone(t *testing.T) {
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"METHOD:REQUEST",
		"BEGIN:VTIMEZONE",
		"TZID:Europe/Berlin",
		"BEGIN:DAYLIGHT",
		"DTSTART:19700329T020000",
		"TZOFFSETFROM:+0100",
		"END:DAYLIGHT",
		"BEGIN:STANDARD",
		"DTSTART:19701025T030000",
		"END:STANDARD",
		"END:VTIMEZONE",
		"BEGIN:VEVENT",
		"UID:evt-1@google.com",
		"SUMMARY:Budget review",
		"DTSTART;TZID=Europe/Berlin:20260801T100000",
		"DTEND;TZID=Europe/Berlin:20260801T110000",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
	info := scanICS(ics)
	if info.dtStart != "2026-08-01 10:00 Europe/Berlin" {
		t.Errorf("dtStart = %q — a VTIMEZONE rule date leaked in as the meeting time", info.dtStart)
	}
	if info.dtEnd != "2026-08-01 11:00 Europe/Berlin" {
		t.Errorf("dtEnd = %q", info.dtEnd)
	}
	// METHOD is a VCALENDAR-level property: skipping the timezone must not cost
	// it, since respond() and hints() both gate on it.
	if info.method != "REQUEST" {
		t.Errorf("method = %q — skipping VTIMEZONE must not skip calendar-level properties", info.method)
	}
	if info.uid != "evt-1@google.com" || info.summary != "Budget review" {
		t.Errorf("uid = %q, summary = %q", info.uid, info.summary)
	}
}

// The FIRST occurrence wins: a REPLY carries the organizer's UID once, and a
// later VALARM or nested component must not overwrite what was already read.
func TestScanICSKeepsFirstValue(t *testing.T) {
	info := scanICS("UID:first\nSUMMARY:one\nUID:second\nSUMMARY:two\n")
	if info.uid != "first" || info.summary != "one" {
		t.Errorf("got uid=%q summary=%q, want the first of each", info.uid, info.summary)
	}
}

// A zoned or floating time is shown as written: resolving a TZID needs the
// VTIMEZONE this scan does not read, so converting would be a guess.
func TestFormatICSTimeZoned(t *testing.T) {
	if got := formatICSTime("20260801T090000", "TZID=Europe/Berlin"); got != "2026-08-01 09:00 Europe/Berlin" {
		t.Errorf("zoned = %q", got)
	}
	if got := formatICSTime("20260801T090000", ""); got != "2026-08-01 09:00" {
		t.Errorf("floating = %q", got)
	}
	if got := formatICSTime("20260801", ""); got != "2026-08-01 (all day)" {
		t.Errorf("date = %q", got)
	}
	// Unparseable values pass through rather than being dropped: showing the
	// raw value beats showing nothing.
	if got := formatICSTime("not-a-time", ""); got != "not-a-time" {
		t.Errorf("passthrough = %q", got)
	}
}

// The pane forwards unhandled keys to a viewport whose default keymap binds
// half-page scrolling to `d` and `u`. The preview pane one level up scrolls with
// those keys, so binding an RSVP to one of them means a reader scrolling out of
// habit mails the organizer a DECLINE with no undo.
func TestInvitationRsvpKeysAvoidTheViewportKeymap(t *testing.T) {
	// bubbles/viewport DefaultKeyMap.
	scroll := []string{"d", "u", "f", "b", "j", "k", " ", "pgup", "pgdown"}
	ics := "BEGIN:VCALENDAR\nMETHOD:REQUEST\nBEGIN:VEVENT\nUID:u1\nEND:VEVENT\nEND:VCALENDAR"

	for _, key := range scroll {
		p := newInvitationPane(context.Background(), &Options{}, "01M", "01X", "2")
		p.ics, p.info = ics, scanICS(ics)
		next, _ := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		inv := next.(*invitationPane)
		// respond() sets submitting before issuing the request; anything still
		// false means the key scrolled instead of answering.
		if inv.submitting {
			t.Errorf("key %q triggered an RSVP — it is a viewport scroll key", key)
		}
	}

	// …and the three that ARE bound still answer.
	for _, key := range []string{"a", "x", "t"} {
		p := newInvitationPane(context.Background(), &Options{}, "01M", "01X", "2")
		p.ics, p.info = ics, scanICS(ics)
		next, cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if !next.(*invitationPane).submitting || cmd == nil {
			t.Errorf("key %q should answer the invitation", key)
		}
	}
}

// A REPLY or CANCEL has nothing to answer; pressing an RSVP key must say so
// rather than posting one.
func TestInvitationRefusesToAnswerAReply(t *testing.T) {
	ics := "BEGIN:VCALENDAR\nMETHOD:REPLY\nBEGIN:VEVENT\nUID:u1\nEND:VEVENT\nEND:VCALENDAR"
	p := newInvitationPane(context.Background(), &Options{}, "01M", "01X", "2")
	p.ics, p.info = ics, scanICS(ics)
	next, cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	inv := next.(*invitationPane)
	if inv.submitting || cmd != nil {
		t.Fatal("a METHOD:REPLY must not be answerable")
	}
	if !strings.Contains(inv.errMsg, "REPLY") {
		t.Errorf("errMsg = %q — it should name what this message actually is", inv.errMsg)
	}
	if strings.Contains(p.hints(), "a accept") {
		t.Errorf("hints = %q — a REPLY must not advertise RSVP keys", p.hints())
	}
}

// The organizer is told by exactly ONE route, and the wording must say which:
// "updated directly" and "a reply was sent" are different states of the world.
func TestRsvpOutcomeIsExclusive(t *testing.T) {
	local := &coreapi.PimRsvpResult{Partstat: "ACCEPTED", OrganizerUpdated: true}
	if got := rsvpOutcome(local); !strings.Contains(got, "updated directly") {
		t.Errorf("local organizer = %q", got)
	}
	remote := &coreapi.PimRsvpResult{Partstat: "DECLINED", ReplySent: true}
	if got := rsvpOutcome(remote); !strings.Contains(got, "reply was sent") {
		t.Errorf("remote organizer = %q", got)
	}
	neither := &coreapi.PimRsvpResult{Partstat: "TENTATIVE"}
	if got := rsvpOutcome(neither); !strings.Contains(got, "not notified") {
		t.Errorf("neither = %q", got)
	}
	if got := rsvpOutcome(nil); got != "answered" {
		t.Errorf("nil = %q", got)
	}
}
