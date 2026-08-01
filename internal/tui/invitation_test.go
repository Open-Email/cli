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

func loadedPane(t *testing.T, inv *coreapi.MessageInvitation) *invitationPane {
	t.Helper()
	p := newInvitationPane(context.Background(), &Options{}, "01M", "01X")
	p.inv = inv
	p.setSize(80, 24)
	return p
}

// canRespond is the RESPOND endpoint's own acceptance predicate, so gating on it
// is what makes "the buttons that are drawn will work" true by construction. The
// previous version guessed from the iTIP METHOD alone and offered an organizer
// buttons the write then refused with 403 is_organizer.
func TestInvitationRespondsOnlyWhenTheServerWould(t *testing.T) {
	addr := "alice@x.test"
	organizer := &coreapi.MessageInvitation{
		Section: "2", MyAddress: &addr, IsOrganizer: true, CanRespond: false,
	}
	p := loadedPane(t, organizer)
	for _, key := range []string{"a", "x", "t"} {
		next, cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		inv := next.(*invitationPane)
		if inv.submitting || cmd != nil {
			t.Fatalf("key %q answered an invitation the server would refuse", key)
		}
		if !strings.Contains(inv.errMsg, "organizer") {
			t.Errorf("errMsg = %q — it should say why", inv.errMsg)
		}
	}
	if strings.Contains(p.hints(), "a accept") {
		t.Errorf("hints = %q — buttons must not be advertised when canRespond is false", p.hints())
	}

	answerable := &coreapi.MessageInvitation{Section: "2", MyAddress: &addr, CanRespond: true}
	for _, key := range []string{"a", "x", "t"} {
		p := loadedPane(t, answerable)
		next, cmd := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if !next.(*invitationPane).submitting || cmd == nil {
			t.Errorf("key %q should answer the invitation", key)
		}
	}
	if !strings.Contains(loadedPane(t, answerable).hints(), "a accept") {
		t.Error("an answerable invitation must advertise its keys")
	}
}

// The pane forwards unhandled keys to a viewport whose default keymap binds
// half-page scrolling to `d` and `u`. The preview pane one level up scrolls with
// those keys, so binding an RSVP to one of them means a reader scrolling out of
// habit mails the organizer a decline with no undo.
func TestInvitationRsvpKeysAvoidTheViewportKeymap(t *testing.T) {
	addr := "alice@x.test"
	for _, key := range []string{"d", "u", "f", "b", "j", "k", " ", "pgup", "pgdown"} {
		p := loadedPane(t, &coreapi.MessageInvitation{Section: "2", MyAddress: &addr, CanRespond: true})
		next, _ := p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if next.(*invitationPane).submitting {
			t.Errorf("key %q triggered an RSVP — it is a viewport scroll key", key)
		}
	}
}

// Each refusal gets its own sentence: "not allowed" leaves a reader with nothing
// to do next.
func TestCannotRespondReason(t *testing.T) {
	addr := "alice@x.test"
	reply, cancel := "REPLY", "CANCEL"
	cases := []struct {
		name string
		inv  coreapi.MessageInvitation
		want string
	}{
		{"organizer", coreapi.MessageInvitation{IsOrganizer: true, MyAddress: &addr}, "you are the organizer"},
		{"not an attendee", coreapi.MessageInvitation{}, "does not name any of your addresses"},
		{"a reply", coreapi.MessageInvitation{MyAddress: &addr, Method: &reply}, "someone else's reply"},
		{"a cancellation", coreapi.MessageInvitation{MyAddress: &addr, Method: &cancel}, "cancels the event"},
	}
	for _, tc := range cases {
		if got := cannotRespondReason(&tc.inv); !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.name, got, tc.want)
		}
	}
	// Organizer wins over every other reading: it is the one that says "edit
	// your own copy" rather than "you cannot".
	both := coreapi.MessageInvitation{IsOrganizer: true, Method: &reply}
	if got := cannotRespondReason(&both); !strings.Contains(got, "organizer") {
		t.Errorf("organizer should take precedence, got %q", got)
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

// Times come from the server as epoch seconds with the object's timezone already
// applied — the whole reason the client-side iCalendar scan is gone, since it
// read a VTIMEZONE rule's 1970 DTSTART as the meeting time.
func TestInvitationRendering(t *testing.T) {
	start, end := int64(1785312000), int64(1785315600)
	summary, org, loc := "Budget review", "boss@acme.test", "Room 1"
	rrule := "FREQ=WEEKLY"
	inv := &coreapi.MessageInvitation{
		Section: "2", Summary: &summary, Organizer: &org, Location: &loc,
		DTStart: &start, DTEnd: &end, RRule: &rrule,
		Attendees: []coreapi.PimAttendee{
			{Email: "a@x.test", CN: "Ann", Partstat: "ACCEPTED"},
			{Email: "b@x.test", Partstat: "NEEDS-ACTION"},
		},
		AttendeeCount: 5,
	}
	when := invWhen(inv)
	if !strings.Contains(when, "→") || when != fmtEpoch(start)+" → "+fmtEpoch(end) {
		t.Errorf("when = %q", when)
	}
	if invWhen(&coreapi.MessageInvitation{}) != "" {
		t.Error("a dateless invitation must render no When row")
	}

	att := invAttendees(inv)
	if !strings.Contains(att, "Ann (accepted)") {
		t.Errorf("attendees = %q — a real answer should show", att)
	}
	if strings.Contains(att, "needs-action") {
		t.Errorf("attendees = %q — NEEDS-ACTION is the default and says nothing", att)
	}
	// The list is truncated to the iTIP fan-out bound, so the real total must
	// survive or a 5-person meeting reads as a 2-person one.
	if !strings.Contains(att, "5 total") {
		t.Errorf("attendees = %q — the real count must survive truncation", att)
	}

	p := loadedPane(t, inv)
	view := p.view()
	for _, want := range []string{"Budget review", "Room 1", "boss@acme.test"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}
}

// An RDATE-only series has no RRULE to print; the sentinel must not leak.
func TestInvitationRdateSeries(t *testing.T) {
	rrule := "RDATE"
	p := loadedPane(t, &coreapi.MessageInvitation{Section: "2", RRule: &rrule})
	if !strings.Contains(p.view(), "on set dates") {
		t.Errorf("view = %q", p.view())
	}
}
