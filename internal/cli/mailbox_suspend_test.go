package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// suspendCore answers the suspension PATCH with a scripted status and body,
// then serves GET /mailboxes/:id from a scripted sequence, keeping every
// request so a test can tell how many PATCHes went out.
type suspendCore struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []string
	patch []map[string]any
	query []string

	patchStatus int
	patchBody   any
	gets        []map[string]any // served in order; the last repeats
	list        map[string]any
}

func newSuspendCore(t *testing.T) *suspendCore {
	t.Helper()
	f := &suspendCore{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/identities/01KXRS3SHN1N35G4YETVADSN0R":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			f.patch = append(f.patch, body)
			if f.patchStatus == http.StatusAccepted {
				w.Header().Set("Retry-After", "5")
			}
			w.WriteHeader(f.patchStatus)
			_ = json.NewEncoder(w).Encode(f.patchBody)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/mailboxes/01KXRS3SHN1N35G4YETVADSN0R":
			if len(f.gets) == 0 {
				http.NotFound(w, r)
				return
			}
			out := f.gets[0]
			if len(f.gets) > 1 {
				f.gets = f.gets[1:]
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities":
			f.query = append(f.query, r.URL.RawQuery)
			_ = json.NewEncoder(w).Encode(f.list)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *suspendCore) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func mbJSON(suspendedAt any, pending bool) map[string]any {
	return map[string]any{
		"id": "01KXRS3SHN1N35G4YETVADSN0R", "primaryAddress": "alice@acme.test", "accountId": "ACC1",
		"createdAt": 1700000000, "suspendedAt": suspendedAt, "suspensionPending": pending,
	}
}

// fastPolls removes core's five seconds between --wait polls.
func fastPolls(t *testing.T) {
	t.Helper()
	old := suspensionPollDelay
	suspensionPollDelay = func(time.Duration) time.Duration { return time.Millisecond }
	t.Cleanup(func() { suspensionPollDelay = old })
}

func TestMailboxSuspendSendsOnlySuspended(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 200, mbJSON(1760000000, false)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.patch) != 1 || len(core.patch[0]) != 1 || core.patch[0]["suspended"] != true {
		t.Fatalf("patch bodies = %v, want exactly [{suspended:true}]", core.patch)
	}
	if !strings.Contains(errOut, "Suspended mailbox 01KXRS3SHN1N35G4YETVADSN0R") {
		t.Fatalf("stderr = %q, want the done line", errOut)
	}
}

func TestMailboxResumeSendsOnlySuspendedFalse(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 200, mbJSON(nil, false)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "resume", "01KXRS3SHN1N35G4YETVADSN0R")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.patch) != 1 || len(core.patch[0]) != 1 || core.patch[0]["suspended"] != false {
		t.Fatalf("patch bodies = %v, want exactly [{suspended:false}]", core.patch)
	}
	if !strings.Contains(errOut, "Resumed mailbox 01KXRS3SHN1N35G4YETVADSN0R") {
		t.Fatalf("stderr = %q, want the done line", errOut)
	}
}

// A 202 is committed but not enforced: it must not read as done, by its
// wording or its exit code, and without --wait nothing is polled.
func TestMailboxSuspendAcceptedIsNotDone(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 202, mbJSON(1760000000, true)
	out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R")
	if code != exitSuspensionPending {
		t.Fatalf("exit %d, want %d: %s", code, exitSuspensionPending, errOut)
	}
	if strings.Contains(errOut, "Suspended mailbox") || !strings.Contains(errOut, "still applying") {
		t.Fatalf("stderr = %q, want the applying wording and no done line", errOut)
	}
	if !strings.Contains(out, "(applying)") {
		t.Fatalf("stdout = %q, want the Suspended row marked applying", out)
	}
	if n := core.count("GET "); n != 0 {
		t.Fatalf("polled %d times without --wait", n)
	}
}

// --wait polls GET until suspensionPending clears and never sends the PATCH a
// second time: a re-sent change could reassert an intent someone has since
// reversed.
func TestMailboxSuspendWaitPollsWithoutResending(t *testing.T) {
	fastPolls(t)
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 202, mbJSON(1760000000, true)
	core.gets = []map[string]any{mbJSON(1760000000, true), mbJSON(1760000000, true), mbJSON(1760000000, false)}
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R", "--wait")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if n := core.count("PATCH "); n != 1 {
		t.Fatalf("sent %d PATCHes, want exactly 1", n)
	}
	if n := core.count("GET /api/v1/mailboxes/01KXRS3SHN1N35G4YETVADSN0R"); n != 3 {
		t.Fatalf("polled %d times, want 3 (until pending cleared)", n)
	}
	if !strings.Contains(errOut, "Suspended mailbox 01KXRS3SHN1N35G4YETVADSN0R") {
		t.Fatalf("stderr = %q, want the done line once applied", errOut)
	}
}

func TestMailboxSuspendWaitTimeoutStaysPending(t *testing.T) {
	fastPolls(t)
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 202, mbJSON(1760000000, true)
	core.gets = []map[string]any{mbJSON(1760000000, true)}
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R", "--wait", "--wait-timeout", "20ms")
	if code != exitSuspensionPending {
		t.Fatalf("exit %d, want %d: %s", code, exitSuspensionPending, errOut)
	}
	if n := core.count("PATCH "); n != 1 {
		t.Fatalf("sent %d PATCHes, want exactly 1", n)
	}
	if strings.Contains(errOut, "Suspended mailbox") {
		t.Fatalf("stderr = %q claims completion", errOut)
	}
}

// A poll that sees the opposite state found a newer change: reported as
// superseded, not as this request's success.
func TestMailboxSuspendWaitSeesNewerResume(t *testing.T) {
	fastPolls(t)
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 202, mbJSON(1760000000, true)
	core.gets = []map[string]any{mbJSON(nil, false)}
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R", "--wait")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	if !strings.Contains(errOut, "superseded") || core.count("PATCH ") != 1 {
		t.Fatalf("stderr = %q (patches %d), want superseded after one PATCH", errOut, core.count("PATCH "))
	}
}

func TestMailboxSuspendSupersededShowsCurrentState(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus = 409
	core.patchBody = map[string]any{"error": "suspension_changed", "mailbox": mbJSON(nil, false)}
	out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	if !strings.Contains(errOut, "superseded") || !strings.Contains(errOut, "newer resume") {
		t.Fatalf("stderr = %q, want the superseded line naming the newer resume", errOut)
	}
	if strings.Contains(errOut, "Suspended mailbox") || !strings.Contains(out, "alice@acme.test") {
		t.Fatalf("stdout = %q, want the current mailbox and no done line", out)
	}
}

func TestMailboxSuspendSupersededJSON(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus = 409
	core.patchBody = map[string]any{"error": "suspension_changed", "mailbox": mbJSON(nil, false)}
	out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R", "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	var got struct {
		Error   string         `json:"error"`
		Mailbox map[string]any `json:"mailbox"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Error != "suspension_changed" || got.Mailbox["id"] != "01KXRS3SHN1N35G4YETVADSN0R" {
		t.Fatalf("stdout = %q (%v), want the error and the current mailbox", out, err)
	}
}

func TestMailboxSuspendNotEnabled(t *testing.T) {
	core := newSuspendCore(t)
	core.patchStatus, core.patchBody = 403, map[string]any{"error": "suspension_not_enabled"}
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "suspend", "01KXRS3SHN1N35G4YETVADSN0R")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	if !strings.Contains(errOut, "suspension_not_enabled") || !strings.Contains(errOut, "not enabled on this deployment") {
		t.Fatalf("stderr = %q, want the code and the rollout hint", errOut)
	}
}

func TestMailboxListShowsSuspendedColumn(t *testing.T) {
	core := newSuspendCore(t)
	other := mbJSON(nil, false)
	other["id"], other["primaryAddress"] = "MB2", "bob@acme.test"
	core.list = map[string]any{"identities": []any{mbJSON(1760000000, false), other}}
	out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "SUSPENDED") {
		t.Fatalf("stdout = %q, want a header with SUSPENDED and two rows", out)
	}
	want := time.Unix(1760000000, 0).Format("2006-01-02 15:04")
	if !strings.Contains(lines[1], want) || !strings.HasSuffix(strings.TrimSpace(lines[2]), "no") {
		t.Fatalf("rows = %q, want 01KXRS3SHN1N35G4YETVADSN0R suspended since %s and MB2 not", lines[1:], want)
	}
	if q := core.query[0]; strings.Contains(q, "state=") {
		t.Fatalf("plain list sent %q, want no state filter", q)
	}
}

func TestMailboxListSuspendedFilter(t *testing.T) {
	core := newSuspendCore(t)
	core.list = map[string]any{"identities": []any{mbJSON(1760000000, true)}}
	out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "list", "--suspended", "--account", "ACC1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	q := core.query[0]
	if !strings.Contains(q, "state=suspended") || !strings.Contains(q, "accountId=ACC1") {
		t.Fatalf("query = %q, want state=suspended and accountId=ACC1", q)
	}
	if !strings.Contains(out, "(applying)") {
		t.Fatalf("stdout = %q, want the pending suspension marked applying", out)
	}
}

func TestMailboxListSuspendedFlagConflicts(t *testing.T) {
	core := newSuspendCore(t)
	for _, args := range [][]string{
		{"mailboxes", "list", "--suspended", "--deleted"},
		{"mailboxes", "list", "--account", "ACC1"},
	} {
		_, _, code := runCLI(t, core.srv.URL, args...)
		if code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
	if n := core.count(""); n != 0 {
		t.Fatalf("refused flags still reached core %d times", n)
	}
}
