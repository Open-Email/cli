package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPimObjectsGetAndPutJSONFlag(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	var gotPutContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities/"+mbx+"/calendars/work/objects/standup.ics":
			if !strings.Contains(r.Header.Get("Accept"), "application/json") {
				t.Errorf("expected Accept: application/json, got %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"href":"standup.ics","uid":"u-1","etag":"e-1","component":"VEVENT","size":42,"updatedAt":1751000000,"writable":true,"content":"BEGIN:VCALENDAR","data":{"@type":"Event","title":"Standup"}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/identities/"+mbx+"/calendars/work/objects/standup.ics":
			gotPutContentType = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"href":"standup.ics","uid":"u-1","etag":"e-2","created":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_API_URL", srv.URL)
	t.Setenv("OPENEMAIL_API_KEY", "oek_test")

	// 1. objects get --json should emit full JSON envelope to a.out.out (not os.Stdout).
	var outBuf, errBuf bytes.Buffer
	a := &app{}
	root := newRootCmd(a)
	root.SetArgs([]string{"calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--json"})
	origPreRun := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := origPreRun(cmd, args); err != nil {
			return err
		}
		a.out.out = &outBuf
		a.out.err = &errBuf
		return nil
	}
	if err := root.Execute(); err != nil {
		t.Fatalf("objects get --json: %v", err)
	}
	var gotObj map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &gotObj); err != nil {
		t.Fatalf("objects get --json did not write valid JSON to Printer.out: %v (got %q)", err, outBuf.String())
	}
	if gotObj["href"] != "standup.ics" {
		t.Errorf("href = %v, want standup.ics", gotObj["href"])
	}

	// 2. objects get --jscalendar (without --json) should write the JSCalendar data document to Printer.out.
	outBuf.Reset()
	errBuf.Reset()
	a = &app{}
	root = newRootCmd(a)
	root.SetArgs([]string{"calendars", "--mailbox", mbx, "objects", "get", "work", "standup.ics", "--jscalendar"})
	origPreRun = root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := origPreRun(cmd, args); err != nil {
			return err
		}
		a.out.out = &outBuf
		a.out.err = &errBuf
		return nil
	}
	if err := root.Execute(); err != nil {
		t.Fatalf("objects get --jscalendar: %v", err)
	}
	if !strings.Contains(outBuf.String(), `"Standup"`) {
		t.Errorf("objects get --jscalendar output missing Standup: %q", outBuf.String())
	}

	// 3. objects put --json should upload as JSCalendar AND emit JSON result to Printer.out.
	outBuf.Reset()
	errBuf.Reset()
	tmpFile := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(tmpFile, []byte(`{"@type":"Event","title":"Standup"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a = &app{}
	root = newRootCmd(a)
	root.SetArgs([]string{"calendars", "--mailbox", mbx, "objects", "put", "work", "standup.ics", "--json", "-f", tmpFile})
	origPreRun = root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := origPreRun(cmd, args); err != nil {
			return err
		}
		a.out.out = &outBuf
		a.out.err = &errBuf
		return nil
	}
	if err := root.Execute(); err != nil {
		t.Fatalf("objects put --json: %v", err)
	}
	if gotPutContentType != "application/json" {
		t.Errorf("expected PUT Content-Type=application/json, got %q", gotPutContentType)
	}
	var putRes map[string]any
	if err := json.Unmarshal(outBuf.Bytes(), &putRes); err != nil {
		t.Fatalf("objects put --json did not emit JSON result: %v (got %q)", err, outBuf.String())
	}
	if putRes["etag"] != "e-2" {
		t.Errorf("etag = %v, want e-2", putRes["etag"])
	}
}

func TestTasksListAllStuckCursorGuard(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 5 {
			t.Errorf("infinite pagination loop detected")
			http.Error(w, "loop", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"objects":[{"href":"t1.ics","uid":"u1"}],"nextCursor":"stuck"}`))
	}))
	defer srv.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_API_URL", srv.URL)
	t.Setenv("OPENEMAIL_API_KEY", "oek_test")

	a := &app{}
	root := newRootCmd(a)
	root.SetArgs([]string{"calendars", "--mailbox", mbx, "tasks", "list", "work", "--all"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "pagination cursor did not advance") {
		t.Fatalf("expected stuck cursor error, got %v", err)
	}
}

func TestVacationShowWritesBodyToStdout(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mailboxes/" + mbx + "/vacation":
			_, _ = w.Write([]byte(`{"isEnabled":true,"subject":"Away","textBody":"Out of office until Monday","state":"v1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_API_URL", srv.URL)
	t.Setenv("OPENEMAIL_API_KEY", "oek_test")

	var outBuf, errBuf bytes.Buffer
	a := &app{}
	root := newRootCmd(a)
	root.SetArgs([]string{"vacation", "-m", mbx, "show"})
	origPreRun := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := origPreRun(cmd, args); err != nil {
			return err
		}
		a.out.out = &outBuf
		a.out.err = &errBuf
		return nil
	}
	if err := root.Execute(); err != nil {
		t.Fatalf("vacation show: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Out of office until Monday") {
		t.Errorf("expected vacation textBody on stdout, got stdout=%q stderr=%q", outBuf.String(), errBuf.String())
	}
}

func TestOpenRawBodyDashReadsStdin(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	want := "From: alice@example.com\r\nTo: bob@example.com\r\n\r\nHello\r\n"
	go func() {
		_, _ = io.WriteString(w, want)
		_ = w.Close()
	}()

	getBody, length, cleanup, err := openRawBody("-")
	if err != nil {
		t.Fatalf("openRawBody(\"-\"): %v", err)
	}
	defer cleanup()
	if length != int64(len(want)) {
		t.Errorf("length = %d, want %d", length, len(want))
	}
	rc, err := getBody()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got %q, want %q", string(got), want)
	}
}

func TestPreFlightValidationBeforeAuth(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_API_KEY", "")

	cases := [][]string{
		{"admin", "hold", "bogus", "01KXRS3SHN1N35G4YETVADSN0R", "--pause"},
		{"admin", "hold", "domain", "not_a_domain", "--pause"},
		{"admin", "hold", "mailbox", "not-a-ulid", "--pause"},
		{"admin", "release", "account", "not-a-ulid"},
		{"calendars", "create", "work", "--visibility", "bogus"},
		{"calendars", "update", "work", "--visibility", "bogus"},
		{"mailboxes", "shares", "set", "alice@example.com", "--rights", " "},
		{"templates", "variants", "put", "order-shipped", "en", "--subject", "Hi", "--text-file", "-", "--html-file", "-"},
		{"compose", "--from", "a@example.com", "--to", "b@example.com", "--subject", "Hi", "--text", "Yo", "--save", "--no-save"},
	}
	for _, args := range cases {
		root := newRootCmd(&app{})
		root.SetArgs(args)
		err := root.Execute()
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Code != 2 {
			t.Errorf("%v: expected usage error (exit 2) before auth, got %v", args, err)
		}
	}
}
