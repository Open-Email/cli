package cli

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func strPtr(s string) *string { return &s }

// The refusal must name WHICH reason applies — "cannot answer" alone leaves a
// user with nothing to do next.
func TestInvitationRefusal(t *testing.T) {
	addr := "alice@x.test"
	cases := []struct {
		name string
		inv  coreapi.MessageInvitation
		want string
	}{
		{"organizer", coreapi.MessageInvitation{IsOrganizer: true, MyAddress: &addr}, "you are the organizer"},
		{"not an attendee", coreapi.MessageInvitation{}, "names none of your addresses"},
		{"a reply", coreapi.MessageInvitation{MyAddress: &addr, Method: strPtr("REPLY")}, "someone else's reply"},
		{"a cancellation", coreapi.MessageInvitation{MyAddress: &addr, Method: strPtr("CANCEL")}, "cancels the event"},
		{"unexplained", coreapi.MessageInvitation{MyAddress: &addr, Method: strPtr("REQUEST")}, "cannot be answered"},
	}
	for _, tc := range cases {
		if got := invitationRefusal(&tc.inv); !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.name, got, tc.want)
		}
	}
	// Being the organizer outranks every other reading: it is the one that says
	// "edit your own copy" rather than "you cannot".
	both := coreapi.MessageInvitation{IsOrganizer: true, Method: strPtr("REPLY")}
	if got := invitationRefusal(&both); !strings.Contains(got, "organizer") {
		t.Errorf("organizer should take precedence, got %q", got)
	}
}

// Times arrive as epoch seconds with the event's timezone already applied, so
// the CLI formats rather than interprets.
func TestInvitationWhen(t *testing.T) {
	start, end := int64(1785312000), int64(1785315600)
	if got := invitationWhen(&coreapi.MessageInvitation{}); got != "—" {
		t.Errorf("dateless = %q", got)
	}
	only := invitationWhen(&coreapi.MessageInvitation{DTStart: &start})
	if only != fmtEpoch(start) {
		t.Errorf("start only = %q", only)
	}
	both := invitationWhen(&coreapi.MessageInvitation{DTStart: &start, DTEnd: &end})
	if !strings.Contains(both, "→") {
		t.Errorf("range = %q", both)
	}
	rec := invitationWhen(&coreapi.MessageInvitation{DTStart: &start, RRule: strPtr("FREQ=WEEKLY")})
	if !strings.Contains(rec, "FREQ=WEEKLY") {
		t.Errorf("recurring = %q", rec)
	}
	// "RDATE" is a sentinel for a series with explicit dates and no rule; it
	// must not leak to the screen as if it were an RRULE.
	rd := invitationWhen(&coreapi.MessageInvitation{DTStart: &start, RRule: strPtr("RDATE")})
	if !strings.Contains(rd, "set dates") || strings.Contains(rd, "repeats: RDATE") {
		t.Errorf("RDATE series = %q", rd)
	}
}

// `respond` reads stdin when no --message is given. The guard matters because
// reaching that read WITH a message id would block on a terminal forever, which
// looks like a hang rather than a bug.
func TestRespondMessageAndFileAreExclusive(t *testing.T) {
	a := &app{}
	cmd := newInvitationRespondCmd(a)
	cmd.SetArgs([]string{"accepted", "--message", "01J", "--file", "x.ics"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--message with --file must be refused")
	}
	if !strings.Contains(err.Error(), "pass one") {
		t.Errorf("error = %v", err)
	}
}

// --section is meaningless without a message to take it from, and silently
// ignoring it would leave a user believing they pinned a part.
func TestRespondSectionRequiresMessage(t *testing.T) {
	cmd := newInvitationRespondCmd(&app{})
	cmd.SetArgs([]string{"accepted", "--section", "2"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--section only applies to --message") {
		t.Fatalf("error = %v", err)
	}
}

// `invitations status` answers "is this mine to change". Core resolves that
// server-side (`amOrganizer`, via the directory's identity list OR the full
// routing ladder) precisely because a client knows only the address it logged in
// with — an event or task organized from an alias reads as somebody else's.
// Dropping the pair leaves the one question the probe exists to answer unasked.
func TestInvitationStatusRowsSurfaceOrganizer(t *testing.T) {
	cal, href, org := "01C", "task.ics", "mailto:me@x.test"
	mine := &coreapi.PimInvitationStatus{
		Found: true, CalendarID: &cal, Href: &href, Organizer: &org, AmOrganizer: true,
	}
	rows := invitationStatusRows(mine)
	flat := flattenRows(rows)
	if !strings.Contains(flat, "me@x.test") {
		t.Errorf("the organizer must be shown\n%s", flat)
	}
	if strings.Contains(flat, "mailto:") {
		t.Errorf("mailto: prefix not stripped\n%s", flat)
	}
	if !strings.Contains(flat, "Yours to change") || !strings.Contains(flat, "yes") {
		t.Errorf("amOrganizer must be surfaced as its own row\n%s", flat)
	}

	addr := "me@x.test"
	theirs := &coreapi.PimInvitationStatus{Found: true, CalendarID: &cal, Href: &href, MyAddress: &addr}
	flat = flattenRows(invitationStatusRows(theirs))
	if !strings.Contains(flat, "Yours to change") || !strings.Contains(flat, "no") {
		t.Errorf("a third party's copy must read as not yours\n%s", flat)
	}
	// An organizer-less copy (an ordinary personal to-do) must not render a
	// mailto-shaped blank.
	none := flattenRows(invitationStatusRows(&coreapi.PimInvitationStatus{Found: true}))
	if !strings.Contains(none, "Organizer") || strings.Contains(none, "mailto") {
		t.Errorf("no organizer should render as a dash\n%s", none)
	}
}

// Knowing you are the organizer is only useful with the command that follows
// from it: the respond endpoint refuses an organizer with 403 is_organizer, so
// pointing them at RSVP would send them into a refusal.
func TestInvitationStatusNextStep(t *testing.T) {
	addr := "me@x.test"
	organizer := invitationStatusNextStep(&coreapi.PimInvitationStatus{Found: true, AmOrganizer: true})
	if !strings.Contains(organizer, "tasks set") && !strings.Contains(organizer, "objects put") {
		t.Errorf("an organizer must be pointed at the edit path, got %q", organizer)
	}
	if strings.Contains(organizer, "respond") && !strings.Contains(organizer, "refused") {
		t.Errorf("an organizer must not be pointed at RSVP, got %q", organizer)
	}
	attendee := invitationStatusNextStep(&coreapi.PimInvitationStatus{Found: true, MyAddress: &addr})
	if !strings.Contains(attendee, "respond") {
		t.Errorf("an attendee must be pointed at RSVP, got %q", attendee)
	}
	bystander := invitationStatusNextStep(&coreapi.PimInvitationStatus{Found: true})
	if !strings.Contains(bystander, "not an attendee") {
		t.Errorf("a bystander needs no command, got %q", bystander)
	}
}

// Core now schedules to-dos (RFC 5546 §3.4), so a mailed VTODO reaches this
// surface. Calling it an "Event" and its DUE a start time is the same defect the
// listing had — the label must follow the component.
func TestInvitationSubjectLabel(t *testing.T) {
	if got := invitationSubject(&coreapi.MessageInvitation{Component: strPtr("VTODO")}); got != "Task" {
		t.Errorf("VTODO subject = %q, want Task", got)
	}
	if got := invitationSubject(&coreapi.MessageInvitation{Component: strPtr("VEVENT")}); got != "Event" {
		t.Errorf("VEVENT subject = %q, want Event", got)
	}
	// An absent component is the pre-existing shape; Event stays the default.
	if got := invitationSubject(&coreapi.MessageInvitation{}); got != "Event" {
		t.Errorf("component-less subject = %q, want Event", got)
	}
}

// A to-do's single timestamp is a DEADLINE, not a start: core puts DUE in
// `dtend` and leaves `dtstart` empty for the commonest task. Rendering it under
// "When" with an empty value hides the only date the task has.
func TestInvitationWhenForATask(t *testing.T) {
	due := int64(1785312000)
	task := &coreapi.MessageInvitation{Component: strPtr("VTODO"), DTEnd: &due}
	got := invitationWhen(task)
	if got != fmtEpoch(due) {
		t.Errorf("a due-only task = %q, want the due date", got)
	}
	// With a start too, both show — a task may be scheduled as a span.
	start := int64(1785300000)
	span := invitationWhen(&coreapi.MessageInvitation{Component: strPtr("VTODO"), DTStart: &start, DTEnd: &due})
	if !strings.Contains(span, "→") {
		t.Errorf("a started task = %q", span)
	}
	// An event with only an end is not a thing core produces, and must not
	// start reading as if it had a start.
	if got := invitationWhen(&coreapi.MessageInvitation{DTEnd: &due}); got != "—" {
		t.Errorf("a startless event = %q, want the dash", got)
	}
}

// A bad partstat is caught before anything touches the network or stdin.
func TestRespondValidatesPartstatFirst(t *testing.T) {
	cmd := newInvitationRespondCmd(&app{})
	cmd.SetArgs([]string{"maybe", "--message", "01J"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid partstat") {
		t.Fatalf("error = %v", err)
	}
}

// flattenRows joins a FIELD/VALUE table into one searchable string.
func flattenRows(rows [][]string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t"))
		b.WriteString("\n")
	}
	return b.String()
}
