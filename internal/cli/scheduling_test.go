package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// The three tables support reads: what is still being tried, what finished,
// and who was not reached. A finished job's plan is gone, so its progress
// reads as unknown rather than zero.
func TestRenderMailboxScheduling(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	data := &coreapi.MailboxScheduling{
		MailboxID: "M1",
		Pending: []coreapi.SchedulingJob{{ID: 7, UID: "retrying-meeting", State: "pending", Attempts: 2,
			CreatedAt: 1790200000, DueAt: i(1790200060), ExpiresAt: 1790286400, LastError: s("unfinished_recipients"),
			Recipients: i(3), Completed: i(2), Refused: i(0)}},
		Recent: []coreapi.SchedulingJob{{ID: 6, UID: "refused-meeting", State: "failed", Attempts: 1,
			CreatedAt: 1790100000, ExpiresAt: 1790186400, LastError: s("recipient_refused")}},
		Failures: []coreapi.SchedulingFailure{{ID: "r1", UID: "refused-meeting", Method: "REQUEST",
			Counterpart: "guest@example.test", Outcome: s("refused"), Reason: s("organizer_mismatch"),
			Status: s("5.1"), CreatedAt: 1790100001}},
	}
	var out bytes.Buffer
	renderMailboxScheduling(&out, &Printer{}, data)
	text := out.String()
	for _, want := range []string{
		"PENDING", "retrying-meeting", "2/3", "unfinished_recipients",
		"RECENT", "refused-meeting", "failed", "recipient_refused",
		"FAILURES", "guest@example.test", "5.1", "organizer_mismatch",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	empty := &coreapi.MailboxScheduling{MailboxID: "M2"}
	out.Reset()
	renderMailboxScheduling(&out, &Printer{}, empty)
	if !strings.Contains(out.String(), "nothing pending") || !strings.Contains(out.String(), "no failures") {
		t.Errorf("an idle mailbox should say so:\n%s", out.String())
	}
}
