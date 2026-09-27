package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

func TestSchedulingNotFoundMessage(t *testing.T) {
	const mailboxID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/mailboxes/"+mailboxID+"/scheduling" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				requests <- struct{}{}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusNotFound {
					_, _ = w.Write([]byte(`{"error":"not_found"}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"forbidden"}`))
				}
			}))
			defer srv.Close()
			var stdout, stderr bytes.Buffer
			a := &app{apiURL: srv.URL, token: "test-token", tokenSource: "flag",
				out: &Printer{out: &stdout, err: &stderr}}
			cmd := newAdminSchedulingCmd(a)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{mailboxID})
			err := cmd.Execute()
			if len(requests) != 1 || err == nil {
				t.Fatalf("want a failed HTTP request, got %d requests and error %v", len(requests), err)
			}
			if status == http.StatusNotFound {
				want := "mailbox " + mailboxID + " has no calendar store initialized"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err, want)
				}
			} else if ae, ok := coreapi.AsAPIError(err); !ok || ae.Status != status {
				t.Errorf("non-404 API error was not preserved: %v", err)
			}
			if stdout.Len() != 0 {
				t.Errorf("failed read printed data: %s", stdout.String())
			}
		})
	}
}
