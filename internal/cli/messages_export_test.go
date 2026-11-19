package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

const exportMailbox = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// exportMsg is one message the fake core holds: its listing row and the bytes
// /raw answers with. A nil raw answers 404, as core does for a message purged
// between the listing and the read.
type exportMsg struct {
	meta coreapi.ExpungedMessageMeta
	raw  []byte
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newExportMsg(id, envFrom, envTo, body string) exportMsg {
	raw := []byte("Subject: " + id + "\r\n\r\n" + body + "\r\n")
	return exportMsg{
		meta: coreapi.ExpungedMessageMeta{MessageMeta: coreapi.MessageMeta{
			ID: id, EnvelopeFrom: envFrom, EnvelopeTo: envTo,
			BlobHash: sha256Hex(raw), Size: int64(len(raw)), ReceivedAt: 1793800000,
		}},
		raw: raw,
	}
}

// fakeExportCore serves the two listings (paged by pageSize, cursor = the
// index of the next row) and /raw, and records every request it saw.
type fakeExportCore struct {
	t        *testing.T
	live     []exportMsg
	trash    []exportMsg
	pageSize int

	mu       sync.Mutex
	requests []string
}

func (f *fakeExportCore) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeExportCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.t.Errorf("export sent a %s %s; it must only read", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	prefix := "/api/v1/mailboxes/" + exportMailbox + "/messages"
	switch {
	case r.URL.Path == prefix:
		rows := f.live
		if r.URL.Query().Get("state") == "expunged" {
			rows = f.trash
		}
		start := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			for i, m := range rows {
				if m.meta.ID == c {
					start = i
				}
			}
		}
		end := start + f.pageSize
		next := ""
		if end < len(rows) {
			next = rows[end].meta.ID
		} else {
			end = len(rows)
		}
		page := make([]coreapi.ExpungedMessageMeta, 0, end-start)
		for _, m := range rows[start:end] {
			page = append(page, m.meta)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": page, "nextCursor": next})
	case strings.HasPrefix(r.URL.Path, prefix+"/") && strings.HasSuffix(r.URL.Path, "/raw"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/raw")
		for _, m := range append(append([]exportMsg(nil), f.live...), f.trash...) {
			if m.meta.ID == id && m.raw != nil {
				w.Header().Set("Content-Type", "message/rfc822")
				_, _ = w.Write(m.raw)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	case r.URL.Path == "/api/v1/identities/"+exportMailbox:
		// The client's own follow-up to a mail-route 404 (is the store
		// absent?). A read, so it may happen; answering 404 keeps the
		// original error.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

type exportRun struct {
	err    error
	stdout string
	stderr string
}

func runExport(t *testing.T, f *fakeExportCore, jsonMode bool, args ...string) exportRun {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	a := &app{apiURL: srv.URL, token: "test-token", tokenSource: "flag", flagMailbox: exportMailbox,
		out: &Printer{out: &stdout, err: &stderr, json: jsonMode}}
	cmd := newMessageExportCmd(a)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return exportRun{err: err, stdout: stdout.String(), stderr: stderr.String()}
}

type manifestLine struct {
	File    string                      `json:"file"`
	State   string                      `json:"state"`
	SHA256  string                      `json:"sha256"`
	Size    int64                       `json:"size"`
	Error   string                      `json:"error"`
	Message coreapi.ExpungedMessageMeta `json:"message"`
}

func readManifest(t *testing.T, dir string) []manifestLine {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer f.Close()
	var out []manifestLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l manifestLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("manifest line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

// listFiles returns every file under dir, relative and slash-separated.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func threeLiveTwoTrash() *fakeExportCore {
	return &fakeExportCore{
		live: []exportMsg{
			newExportMsg("01ARZ3NDEKTSV4RRFFQ69G5L03", "a@example.com", "box@example.net", "live three"),
			newExportMsg("01ARZ3NDEKTSV4RRFFQ69G5L02", "b@example.com", "box@example.net", "live two"),
			newExportMsg("01ARZ3NDEKTSV4RRFFQ69G5L01", "c@example.com", "box@example.net", "live one"),
		},
		trash: []exportMsg{
			newExportMsg("01ARZ3NDEKTSV4RRFFQ69G5D02", "d@example.com", "box@example.net", "deleted two"),
			newExportMsg("01ARZ3NDEKTSV4RRFFQ69G5D01", "e@example.com", "box@example.net", "deleted one"),
		},
		pageSize: 2,
	}
}

func TestMessageExportLiveOnlyByDefault(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	dir := filepath.Join(t.TempDir(), "out")
	run := runExport(t, f, false, "--out", dir)
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	want := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5L01.eml", "01ARZ3NDEKTSV4RRFFQ69G5L02.eml", "01ARZ3NDEKTSV4RRFFQ69G5L03.eml",
		"export.json", "manifest.jsonl",
	}
	if got := listFiles(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, m := range f.live {
		got, err := os.ReadFile(filepath.Join(dir, m.meta.ID+".eml"))
		if err != nil || !bytes.Equal(got, m.raw) {
			t.Fatalf("%s.eml = %q (%v), want %q", m.meta.ID, got, err, m.raw)
		}
	}
	for _, req := range f.seen() {
		if strings.Contains(req, "state=expunged") {
			t.Fatalf("listed the trash without --include-expunged: %s", req)
		}
	}
	lines := readManifest(t, dir)
	if len(lines) != 3 {
		t.Fatalf("manifest has %d lines, want 3", len(lines))
	}
	for i, l := range lines {
		m := f.live[i]
		if l.File != m.meta.ID+".eml" || l.State != "live" || l.SHA256 != m.meta.BlobHash ||
			l.Size != int64(len(m.raw)) || l.Error != "" || l.Message.ID != m.meta.ID ||
			l.Message.EnvelopeFrom != m.meta.EnvelopeFrom {
			t.Fatalf("manifest line %d = %+v, want the listing's order and %s", i, l, m.meta.ID)
		}
	}
}

func TestMessageExportIncludeExpungedWritesDeletedSubdirectory(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	f.trash[0].meta.ExpungedAt, f.trash[0].meta.PurgeAfter = 1793900000, 1795109600
	dir := filepath.Join(t.TempDir(), "out")
	run := runExport(t, f, false, "--out", dir, "--include-expunged")
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	for _, m := range f.trash {
		got, err := os.ReadFile(filepath.Join(dir, "deleted", m.meta.ID+".eml"))
		if err != nil || !bytes.Equal(got, m.raw) {
			t.Fatalf("deleted/%s.eml = %q (%v), want %q", m.meta.ID, got, err, m.raw)
		}
		if _, err := os.Stat(filepath.Join(dir, m.meta.ID+".eml")); !os.IsNotExist(err) {
			t.Fatalf("deleted message %s also written at the top level", m.meta.ID)
		}
	}
	lines := readManifest(t, dir)
	if len(lines) != 5 {
		t.Fatalf("manifest has %d lines, want 5", len(lines))
	}
	d := lines[3]
	if d.File != "deleted/01ARZ3NDEKTSV4RRFFQ69G5D02.eml" || d.State != "deleted" ||
		d.Message.ExpungedAt != 1793900000 || d.Message.PurgeAfter != 1795109600 {
		t.Fatalf("deleted manifest line = %+v", d)
	}
	var summary map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, "export.json"))
	if err != nil || json.Unmarshal(raw, &summary) != nil {
		t.Fatalf("export.json: %v %s", err, raw)
	}
	if summary["mailboxId"] != exportMailbox || summary["includeExpunged"] != true ||
		summary["live"] != float64(3) || summary["deleted"] != float64(2) || summary["failed"] != float64(0) {
		t.Fatalf("export.json = %s", raw)
	}
}

func TestMessageExportOnlyReadsTheMailbox(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	run := runExport(t, f, false, "--out", filepath.Join(t.TempDir(), "out"), "--include-expunged")
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	for _, req := range f.seen() {
		if !strings.HasPrefix(req, "GET ") {
			t.Fatalf("non-GET request %s", req)
		}
	}
}

func TestMessageExportFilesArePrivate(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	dir := filepath.Join(t.TempDir(), "out")
	if run := runExport(t, f, false, "--out", dir, "--include-expunged"); run.err != nil {
		t.Fatalf("export: %v", run.err)
	}
	for _, rel := range []string{".", "deleted"} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s mode = %v (%v), want 0700", rel, info.Mode().Perm(), err)
		}
	}
	for _, rel := range listFiles(t, dir) {
		info, _ := os.Stat(filepath.Join(dir, rel))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", rel, info.Mode().Perm())
		}
	}
}

func TestMessageExportRecordsMessagesItCouldNotSave(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	f.trash[1].raw = nil                                       // purged between the listing and the read
	f.live[1].meta.BlobHash = sha256Hex([]byte("other bytes")) // storage disagrees with the row
	dir := filepath.Join(t.TempDir(), "out")
	run := runExport(t, f, false, "--out", dir, "--include-expunged")
	var exitErr *ExitError
	if run.err == nil || !errors.As(run.err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("err = %v, want exit 1 for an incomplete export", run.err)
	}
	if !strings.Contains(run.stderr, "2 message(s) could not be exported") {
		t.Fatalf("stderr = %q, want the failure count", run.stderr)
	}
	for _, rel := range []string{"01ARZ3NDEKTSV4RRFFQ69G5L02.eml", "deleted/01ARZ3NDEKTSV4RRFFQ69G5D01.eml"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s written despite failing", rel)
		}
	}
	for _, rel := range listFiles(t, dir) {
		if strings.HasSuffix(rel, ".part") {
			t.Fatalf("partial file left behind: %s", rel)
		}
	}
	byID := map[string]manifestLine{}
	for _, l := range readManifest(t, dir) {
		byID[l.Message.ID] = l
	}
	if l := byID["01ARZ3NDEKTSV4RRFFQ69G5D01"]; l.Error != "not_found" || l.File != "" || l.State != "deleted" {
		t.Fatalf("purged message line = %+v", l)
	}
	if l := byID["01ARZ3NDEKTSV4RRFFQ69G5L02"]; l.Error != "sha256_mismatch" || l.File != "" ||
		l.SHA256 != sha256Hex(f.live[1].raw) {
		t.Fatalf("mismatched message line = %+v", l)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "export.json"))
	var summary map[string]any
	_ = json.Unmarshal(raw, &summary)
	if summary["live"] != float64(2) || summary["deleted"] != float64(1) || summary["failed"] != float64(2) {
		t.Fatalf("export.json = %s", raw)
	}
}

func TestMessageExportResumeSkipsVerifiedFiles(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// One file already exported intact, one left corrupt by an earlier run.
	good, bad := f.live[0], f.live[2]
	if err := os.WriteFile(filepath.Join(dir, good.meta.ID+".eml"), good.raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, bad.meta.ID+".eml"), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runExport(t, f, false, "--out", dir)
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	for _, req := range f.seen() {
		if strings.Contains(req, good.meta.ID+"/raw") {
			t.Fatalf("re-downloaded a file that already verified: %s", req)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, bad.meta.ID+".eml"))
	if !bytes.Equal(got, bad.raw) {
		t.Fatalf("corrupt file not replaced: %q", got)
	}
	if lines := readManifest(t, dir); len(lines) != 3 || lines[0].File != good.meta.ID+".eml" {
		t.Fatalf("manifest = %+v, want every message including the skipped one", lines)
	}
}

func TestMessageExportAddressFilter(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	f.live[1].meta.EnvelopeTo = "Chris@Example.net"
	f.live[2].meta.CC = []coreapi.ContentAddress{{Email: "chris@example.net"}}
	f.trash[0].meta.EnvelopeFrom = "chris@example.net"
	f.trash[1].meta.To = []coreapi.ContentAddress{{Email: "chrisx@example.net"}}
	dir := filepath.Join(t.TempDir(), "out")
	run := runExport(t, f, false, "--out", dir, "--include-expunged", "--address", "chris@example.net")
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	want := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5L01.eml", "01ARZ3NDEKTSV4RRFFQ69G5L02.eml",
		"deleted/01ARZ3NDEKTSV4RRFFQ69G5D02.eml", "export.json", "manifest.jsonl",
	}
	if got := listFiles(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, req := range f.seen() {
		if strings.Contains(req, "G5L03/raw") || strings.Contains(req, "G5D01/raw") {
			t.Fatalf("downloaded a message the filter excludes: %s", req)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "export.json"))
	if !strings.Contains(string(raw), `"chris@example.net"`) {
		t.Fatalf("export.json does not record the filter: %s", raw)
	}
}

func TestMessageExportJSONSummary(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	dir := filepath.Join(t.TempDir(), "out")
	run := runExport(t, f, true, "--out", dir, "--include-expunged")
	if run.err != nil {
		t.Fatalf("export: %v\n%s", run.err, run.stderr)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(run.stdout), &summary); err != nil {
		t.Fatalf("stdout is not JSON: %q", run.stdout)
	}
	if summary["live"] != float64(3) || summary["deleted"] != float64(2) || summary["dir"] != dir {
		t.Fatalf("summary = %v", summary)
	}
}

func TestMessageExportRequiresOut(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	run := runExport(t, f, false)
	var exitErr *ExitError
	if run.err == nil || !errors.As(run.err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("err = %v, want a usage error", run.err)
	}
	if len(f.seen()) != 0 {
		t.Fatalf("requests sent before validation: %v", f.seen())
	}
}

func TestMessageExportRefusesAnIDThatIsNotAFileName(t *testing.T) {
	f := threeLiveTwoTrash()
	f.t = t
	f.live[1].meta.ID = "../escape"
	root := t.TempDir()
	dir := filepath.Join(root, "out")
	run := runExport(t, f, false, "--out", dir)
	var exitErr *ExitError
	if run.err == nil || !errors.As(run.err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("err = %v, want exit 1", run.err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.eml")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside the export directory")
	}
	for _, req := range f.seen() {
		if strings.Contains(req, "escape") {
			t.Fatalf("read a message whose id is not a file name: %s", req)
		}
	}
	lines := readManifest(t, dir)
	if len(lines) != 3 || lines[1].Error != "unsafe_id" || lines[1].File != "" {
		t.Fatalf("manifest = %+v", lines)
	}
}
