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

func TestPimObjectPutRejectsNullDataEnvelope(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	unmappable := `{"href":"journal.ics","uid":"u-2","etag":"e-2","component":"VJOURNAL","writable":false,"content":"BEGIN:VCALENDAR\r\nEND:VCALENDAR","data":null}`
	srv, puts := pimObjectCore(t, mbx, unmappable)
	dir := t.TempDir()

	envelope, stderr, code := runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--json")
	if code != 0 {
		t.Fatalf("get --json: exit %d, stderr %q", code, stderr)
	}
	envFile := filepath.Join(dir, "null_envelope.json")
	if err := os.WriteFile(envFile, []byte(envelope), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, srv.URL, "calendars", "--mailbox", mbx, "objects", "put", "work", "standup.ics", "--json", "--file", envFile)
	if code != 2 || !strings.Contains(stderr, ".data") {
		t.Fatalf("put of data:null envelope: exit %d, stderr %q; want exit 2 naming .data", code, stderr)
	}
	if got := puts(); len(got) != 0 {
		t.Fatalf("data:null envelope reached core: %v", got)
	}
}

func TestReadRawInputChecksAuthBeforeStdinAndRefusesTTY(t *testing.T) {
	// 1. Unauthenticated callers must fail with exit 4 before touching stdin.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	t.Setenv("OPENEMAIL_NO_UPDATE_NOTIFIER", "1")
	t.Setenv("OPENEMAIL_API_KEY", "")
	t.Setenv("OPENEMAIL_PROFILE", "")

	for _, args := range [][]string{
		{"rules", "put"},
		{"prefs", "put"},
		{"calendars", "objects", "put", "work", "standup.ics"},
	} {
		// Even if stdin were a TTY, auth check must run first and return exit 4.
		origTTY, origArgs, origErr := stdinIsTerminal, os.Args, os.Stderr
		stdinIsTerminal = func() bool { return true }
		errR, errW, _ := os.Pipe()
		os.Stderr = errW
		os.Args = append([]string{"openemail", "--api-url", "http://127.0.0.1:1"}, args...)
		code := Execute()
		_ = errW.Close()
		_, _ = io.ReadAll(errR)
		stdinIsTerminal, os.Args, os.Stderr = origTTY, origArgs, origErr
		if code != 4 {
			t.Fatalf("%v without auth: exit %d, want exit 4", args, code)
		}
	}

	// 2. Authenticated caller with no --file on an interactive terminal refuses with exit 2
	// instead of hanging forever waiting for EOF on stdin.
	origTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = origTTY }()

	_, stderr, code := runCLI(t, "http://127.0.0.1:1", "rules", "put")
	if code != 2 || !strings.Contains(stderr, "--file") {
		t.Fatalf("rules put on TTY stdin: exit %d, stderr %q; want exit 2 mentioning --file", code, stderr)
	}
}

func TestDomainsListWithCountsUsageAndAccountFlags(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/domains" {
			gotQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"domains":[{"domain":"example.com","unicodeName":"example.com","enabled":true,"receiving":true,"sending":true,"mailboxCount":12,"usedBytes":2048,"pimBytes":512}],"nextCursor":""}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	stdout, stderr, code := runCLI(t, srv.URL, "domains", "list", "--account", "01KXRS3SHN1N35G4YETVADSN0R", "--counts", "--usage")
	if code != 0 {
		t.Fatalf("domains list: exit %d, stderr %q", code, stderr)
	}
	for _, wantParam := range []string{"accountId=01KXRS3SHN1N35G4YETVADSN0R", "mailboxCounts=true", "usage=true"} {
		if !strings.Contains(gotQuery, wantParam) {
			t.Fatalf("query %q missing %q", gotQuery, wantParam)
		}
	}
	for _, wantCol := range []string{"MAILBOXES", "12", "2.0 KiB", "512 B"} {
		if !strings.Contains(stdout, wantCol) {
			t.Fatalf("stdout missing %q:\n%s", wantCol, stdout)
		}
	}
}
