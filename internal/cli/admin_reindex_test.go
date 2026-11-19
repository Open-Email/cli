package cli

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

/*
 * `admin reindex` covers the WHOLE mailbox. Core queues one page per request
 * (core #888): each answer's `last` is the next request's `after`, until fewer
 * than a page come back. A queued message stays unindexed until its job runs,
 * so a request without the cursor queues the same first page again; the one
 * call this command used to make covered 500 messages of a mailbox of any size.
 *
 * These drive the real command tree against a fake core and pin the requests
 * it makes, the count it reports and the exit code.
 */

// reindexMailboxID is the mailbox the tests reindex: an id the command's
// shape check accepts.
const reindexMailboxID = "01KXRS3SHN1N35G4YETVADSN0R"

// reindexCore answers the reindex route with pages in order (status, body),
// recording each request's query.
func reindexCore(t *testing.T, pages ...[2]string) (*mailCore, func() []url.Values) {
	t.Helper()
	var mu sync.Mutex
	var queries []url.Values
	core := newMailCore(t, systemWhoami, func(r *http.Request) (int, string) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/mailboxes/"+reindexMailboxID+"/reindex" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return 404, `{"error":"not_found"}`
		}
		mu.Lock()
		defer mu.Unlock()
		queries = append(queries, r.URL.Query())
		if len(queries) > len(pages) {
			t.Errorf("request %d past the %d pages the test answers", len(queries), len(pages))
			return 500, `{"error":"internal"}`
		}
		p := pages[len(queries)-1]
		status := 202
		if p[0] != "202" {
			status = 500
		}
		return status, p[1]
	})
	return core, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()
		return append([]url.Values(nil), queries...)
	}
}

func TestAdminReindexPagesThroughTheMailbox(t *testing.T) {
	core, queries := reindexCore(t,
		[2]string{"202", `{"enqueued":500,"last":"01A"}`},
		[2]string{"202", `{"enqueued":500,"last":"01B"}`},
		[2]string{"202", `{"enqueued":7,"last":"01C"}`},
	)
	out, errOut, code := runCLI(t, core.srv.URL, "admin", "reindex", reindexMailboxID)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(errOut, "Enqueued 1007 index job(s) for "+reindexMailboxID) {
		t.Errorf("the count is not the sum of the pages: stdout %q stderr %q", out, errOut)
	}
	got := queries()
	if len(got) != 3 {
		t.Fatalf("made %d requests, want 3 (stop once fewer than a page come back)", len(got))
	}
	wantAfter := []string{"", "01A", "01B"}
	for i, q := range got {
		if q.Get("limit") != "500" {
			t.Errorf("request %d limit = %q, want 500", i+1, q.Get("limit"))
		}
		if q.Get("after") != wantAfter[i] {
			t.Errorf("request %d after = %q, want %q (the previous page's last)", i+1, q.Get("after"), wantAfter[i])
		}
	}

	// --json keeps its shape: the one count, now the whole mailbox's.
	core2, _ := reindexCore(t,
		[2]string{"202", `{"enqueued":500,"last":"01A"}`},
		[2]string{"202", `{"enqueued":0,"last":null}`},
	)
	out, errOut, code = runCLI(t, core2.srv.URL, "admin", "reindex", reindexMailboxID, "--json")
	if code != 0 || strings.Join(strings.Fields(out), "") != `{"enqueued":500}` {
		t.Errorf("--json: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// --limit keeps what it always meant: one page of at most that many, and no
// paging. A full page says more may remain.
func TestAdminReindexLimitIsOnePage(t *testing.T) {
	core, queries := reindexCore(t, [2]string{"202", `{"enqueued":20,"last":"01T"}`})
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "reindex", reindexMailboxID, "--limit", "20")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	got := queries()
	if len(got) != 1 || got[0].Get("limit") != "20" || got[0].Has("after") {
		t.Fatalf("requests = %v, want one with limit=20 and no after", got)
	}
	if !strings.Contains(errOut, "Enqueued 20 index job(s)") || !strings.Contains(errOut, "more may remain") {
		t.Errorf("a full --limit page must say more may remain: %s", errOut)
	}
}

// A core older than the cursor answers a full page with no `last`. Asking again
// would queue that same page, so the command stops after one and says the rest
// is not queued, in both output modes.
func TestAdminReindexStopsOnACoreWithoutTheCursor(t *testing.T) {
	core, queries := reindexCore(t, [2]string{"202", `{"enqueued":500}`})
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "reindex", reindexMailboxID, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if n := len(queries()); n != 1 {
		t.Fatalf("made %d requests against a core with no cursor, want 1", n)
	}
	if !strings.Contains(errOut, "cannot page a reindex") {
		t.Errorf("no warning that the rest is not queued: %s", errOut)
	}
}

// A failure partway reports what was already queued, then the error, exit 1.
func TestAdminReindexFailurePartwaySaysWhatWasQueued(t *testing.T) {
	core, queries := reindexCore(t,
		[2]string{"202", `{"enqueued":500,"last":"01A"}`},
		[2]string{"500", `{"error":"internal"}`},
	)
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "reindex", reindexMailboxID)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, errOut)
	}
	if n := len(queries()); n != 2 {
		t.Fatalf("made %d requests, want 2", n)
	}
	if !strings.Contains(errOut, "500 index job(s) were queued before this stopped") {
		t.Errorf("the count queued before the failure is not said: %s", errOut)
	}
}

// The same cursor twice would page forever; the command stops with an error.
func TestAdminReindexRefusesARepeatedCursor(t *testing.T) {
	core, queries := reindexCore(t,
		[2]string{"202", `{"enqueued":500,"last":"01A"}`},
		[2]string{"202", `{"enqueued":500,"last":"01A"}`},
	)
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "reindex", reindexMailboxID)
	if code != 1 || !strings.Contains(errOut, "cursor 01A twice") {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if n := len(queries()); n != 2 {
		t.Fatalf("made %d requests, want 2", n)
	}
}
