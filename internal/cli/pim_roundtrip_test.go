package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pimObjectCore serves one calendar object as JSON and records PUT bodies.
func pimObjectCore(t *testing.T, mbx, getBody string) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var puts []map[string]any
	path := "/api/v1/identities/" + mbx + "/calendars/work/objects/standup.ics"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/whoami":
			_, _ = io.WriteString(w, accountWhoami)
		case r.Method == http.MethodGet && r.URL.Path == path:
			_, _ = io.WriteString(w, getBody)
		case r.Method == http.MethodPut && r.URL.Path == path:
			var doc map[string]any
			_ = json.NewDecoder(r.Body).Decode(&doc)
			mu.Lock()
			puts = append(puts, doc)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"href":"standup.ics","uid":"u-1","etag":"e-2","created":false}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), puts...)
	}
}

const standupJSON = `{"href":"standup.ics","uid":"u-1","etag":"e-1","component":"VEVENT","size":42,"updatedAt":1751000000,"writable":true,"content":"BEGIN:VCALENDAR","data":{"@type":"Event","uid":"u-1","title":"Standup"}}`

// The documented loop: fetch the document, edit it, write it back. The
// document is --jscalendar's output; the global --json answers the whole
// envelope (href, etag, writable, data), which core would read as an event
// with none of its fields. Before #35 the local --json printed the document,
// so scripts and the COOKBOOK still feed the envelope to put: refused by
// name, never written.
func TestPimObjectGetPutRoundTrip(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	srv, puts := pimObjectCore(t, mbx, standupJSON)
	dir := t.TempDir()

	doc, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--jscalendar")
	if code != 0 {
		t.Fatalf("get --jscalendar: exit %d, stderr %q", code, stderr)
	}
	docFile := filepath.Join(dir, "event.json")
	if err := os.WriteFile(docFile, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "put", "work", "standup.ics", "--jscalendar", "--file", docFile); code != 0 {
		t.Fatalf("put --jscalendar: exit %d, stderr %q", code, stderr)
	}
	if got := puts(); len(got) != 1 || got[0]["title"] != "Standup" || got[0]["href"] != nil {
		t.Fatalf("put sent %v; want the Event document", got)
	}

	envelope, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--json")
	if code != 0 {
		t.Fatalf("get --json: exit %d, stderr %q", code, stderr)
	}
	envFile := filepath.Join(dir, "envelope.json")
	if err := os.WriteFile(envFile, []byte(envelope), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "put", "work", "standup.ics", "--json", "--file", envFile)
	if code != 2 || !strings.Contains(stderr, ".data") {
		t.Fatalf("put of the get --json envelope: exit %d, stderr %q; want exit 2 naming .data", code, stderr)
	}
	if got := puts(); len(got) != 1 {
		t.Fatalf("the envelope reached core: %v", got[1:])
	}
}

// -o was ignored on the structured path, so the document went to stdout.
func TestPimObjectGetStructuredWritesOutputFile(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	srv, _ := pimObjectCore(t, mbx, standupJSON)
	out := filepath.Join(t.TempDir(), "event.json")
	stdout, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--jscalendar", "-o", out)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "Standup") {
		t.Fatalf("the document went to stdout despite -o: %q", stdout)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc["title"] != "Standup" {
		t.Fatalf("file holds %q; want the Event document", raw)
	}
}

// With no JSON mapping the human view falls back to the wire text, which can
// come from an inbound invitation; it must not carry terminal escapes.
func TestPimObjectGetStructuredFallbackIsSanitized(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	srv, _ := pimObjectCore(t, mbx, `{"href":"standup.ics","uid":"u-1","etag":"e-1","component":"VJOURNAL","writable":false,"content":"BEGIN:VCALENDAR\r\nSUMMARY:hi\u001b]52;c;ZXZpbA==\u0007\r\nEND:VCALENDAR","data":null}`)
	stdout, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--jscalendar")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, "SUMMARY:hi") {
		t.Fatalf("fallback wire text not sanitized: %q", stdout)
	}
}
