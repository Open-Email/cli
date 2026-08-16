package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func m3Client(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:         baseURL,
		Token:           "oek_test",
		RetryBackoffMin: time.Millisecond,
		RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// APPEND: a post-commit 503 must retry (the upload carries an X-Delivery-Id, so
// getBody is set), and the retry must re-open the body with the right
// Content-Length.
func TestAppendStreamingRetryReopensBody(t *testing.T) {
	var calls int32
	var lastBody, lastDelivery, lastCT string
	var lastCL int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		lastCL = r.ContentLength
		lastDelivery = r.Header.Get("X-Delivery-Id")
		lastCT = r.Header.Get("Content-Type")
		if n == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"reaped"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"delivered","messageId":"m1","uid":5,"uidValidity":1,"threadId":"t1","duplicate":false}`))
	}))
	defer srv.Close()

	body := "From: a@b\r\n\r\nhello"
	getBody := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }
	res, _, err := m3Client(t, srv.URL).AppendMessage(context.Background(), "mbx",
		AppendOptions{EnvelopeFrom: "a@b", EnvelopeTo: "c@d", DeliveryID: "DID1"},
		getBody, int64(len(body)))
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want 2 attempts (503 then 200), got %d", got)
	}
	if lastBody != body {
		t.Fatalf("body on retry: got %q want %q", lastBody, body)
	}
	if lastCL != int64(len(body)) {
		t.Fatalf("Content-Length: got %d want %d", lastCL, len(body))
	}
	if lastDelivery != "DID1" {
		t.Fatalf("X-Delivery-Id: got %q", lastDelivery)
	}
	if lastCT != "message/rfc822" {
		t.Fatalf("Content-Type: got %q", lastCT)
	}
	if res.Status != "delivered" || res.MessageID != "m1" || res.UID == nil || *res.UID != 5 {
		t.Fatalf("result: %+v", res)
	}
}

// APPEND idempotent-replay variant: duplicate=true with null uid/uidValidity/threadId.
func TestAppendDuplicateNullCoordinates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"delivered","messageId":"m1","uid":null,"uidValidity":null,"threadId":null,"duplicate":true}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).AppendMessage(context.Background(), "mbx", AppendOptions{DeliveryID: "d"},
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }, 1)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if !res.Duplicate || res.UID != nil || res.ThreadID != nil {
		t.Fatalf("want duplicate with null coords, got %+v", res)
	}
}

// APPEND filtered variant.
func TestAppendFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"filtered","deliveryId":"d","redirected":true}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).AppendMessage(context.Background(), "mbx", AppendOptions{DeliveryID: "d"},
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }, 1)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if res.Status != "filtered" || !res.Redirected {
		t.Fatalf("want filtered/redirected, got %+v", res)
	}
}

// Raw download streams straight through, body stays open until Close.
func TestGetMessageRawStreams(t *testing.T) {
	want := "From: a\r\nSubject: hi\r\n\r\nhello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		w.Write([]byte(want))
	}))
	defer srv.Close()
	rc, err := m3Client(t, srv.URL).GetMessageRaw(context.Background(), "mbx", "m1")
	if err != nil {
		t.Fatalf("GetMessageRaw: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body: got %q want %q", got, want)
	}
}

// list messages carries the next-page cursor in the `nextCursor` field; an
// absent nextCursor means the last page.
func TestListMessagesCursorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"messages":[{"id":"m1"}],"nextCursor":"c9"}`))
		} else {
			w.Write([]byte(`{"messages":[{"id":"m2"}]}`))
		}
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)
	p1, err := c.ListMessages(context.Background(), "mbx", ListOpts{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if p1.NextCursor != "c9" {
		t.Fatalf("page1 cursor: got %q want c9", p1.NextCursor)
	}
	p2, _ := c.ListMessages(context.Background(), "mbx", ListOpts{Cursor: "c9"})
	if p2.NextCursor != "" {
		t.Fatalf("page2 cursor: got %q want empty", p2.NextCursor)
	}
}

// PATCH union: normal MessageMeta vs expunge notice.
func TestPatchMessageUnion(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"id":"m1","labels":[{"name":"INBOX","uid":7,"uidValidity":1}],"flags":["seen"],"envelopeFrom":"a","envelopeTo":"b","referencesIds":[],"receivedAt":10,"size":100,"blobHash":"h","blobGen":"g"}`))
		}))
		defer srv.Close()
		res, err := m3Client(t, srv.URL).PatchMessage(context.Background(), "mbx", "m1", PatchInput{FlagsSet: []string{"seen"}})
		if err != nil {
			t.Fatalf("PatchMessage: %v", err)
		}
		if res.Expunged {
			t.Fatalf("want non-expunged, got expunged")
		}
		if res.ID != "m1" || len(res.Labels) != 1 || res.Labels[0].UID != 7 {
			t.Fatalf("meta: %+v", res.MessageMeta)
		}
	})
	t.Run("expunged", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"expunged":true,"expungedAt":100,"purgeAfter":200}`))
		}))
		defer srv.Close()
		res, err := m3Client(t, srv.URL).PatchMessage(context.Background(), "mbx", "m1", PatchInput{LabelsRemove: []string{"INBOX"}})
		if err != nil {
			t.Fatalf("PatchMessage: %v", err)
		}
		if !res.Expunged || res.PurgeAfter == nil || *res.PurgeAfter != 200 {
			t.Fatalf("want expunged w/ purgeAfter=200, got %+v", res)
		}
	})
}

// removedFromLabel is the label NAME (string), not a boolean.
func TestDeleteMessageRemovedFromLabelIsString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("label") != "INBOX" {
			t.Errorf("label query: got %q", r.URL.Query().Get("label"))
		}
		w.Write([]byte(`{"removedFromLabel":"INBOX","expunged":false}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).DeleteMessage(context.Background(), "mbx", "m1", "INBOX", false)
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if res.RemovedFromLabel == nil || *res.RemovedFromLabel != "INBOX" || res.Expunged {
		t.Fatalf("got %+v", res)
	}
}

// Trash listing decodes the expunge-window fields.
func TestListTrashFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "expunged" {
			t.Errorf("state: got %q", r.URL.Query().Get("state"))
		}
		w.Write([]byte(`{"messages":[{"id":"m1","labels":[],"flags":[],"envelopeFrom":"a","envelopeTo":"b","referencesIds":[],"receivedAt":5,"size":1,"blobHash":"h","blobGen":"g","expungedAt":100,"purgeAfter":200,"expungedLabels":["INBOX"]}]}`))
	}))
	defer srv.Close()
	page, err := m3Client(t, srv.URL).ListTrash(context.Background(), "mbx", ListOpts{})
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items: %d", len(page.Items))
	}
	it := page.Items[0]
	if it.ExpungedAt != 100 || it.PurgeAfter != 200 || len(it.ExpungedLabels) != 1 || it.ID != "m1" {
		t.Fatalf("expunged meta: %+v", it)
	}
}

// UID-range label listing: no cursor, uidMin/uidMax/limit query, per-label uid.
func TestLabelMessagesUIDRange(t *testing.T) {
	var q map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = map[string]string{
			"uidMin": r.URL.Query().Get("uidMin"),
			"uidMax": r.URL.Query().Get("uidMax"),
			"limit":  r.URL.Query().Get("limit"),
		}
		w.Write([]byte(`{"messages":[{"id":"m1","labels":[],"flags":[],"envelopeFrom":"a","envelopeTo":"b","referencesIds":[],"receivedAt":5,"size":1,"blobHash":"h","blobGen":"g","uid":10}],"uidValidity":3}`))
	}))
	defer srv.Close()
	msgs, uidv, err := m3Client(t, srv.URL).LabelMessages(context.Background(), "mbx", "INBOX", 10, 20, 50)
	if err != nil {
		t.Fatalf("LabelMessages: %v", err)
	}
	if q["uidMin"] != "10" || q["uidMax"] != "20" || q["limit"] != "50" {
		t.Fatalf("query: %+v", q)
	}
	if uidv != 3 || len(msgs) != 1 || msgs[0].UID == nil || *msgs[0].UID != 10 {
		t.Fatalf("msgs=%+v uidv=%d", msgs, uidv)
	}
}

// The folder-sync projection: same query contract, the narrow row shape.
func TestLabelUidsUIDRange(t *testing.T) {
	var q map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mailboxes/mbx/labels/INBOX/uids" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q = map[string]string{
			"uidMin": r.URL.Query().Get("uidMin"),
			"uidMax": r.URL.Query().Get("uidMax"),
			"limit":  r.URL.Query().Get("limit"),
		}
		w.Write([]byte(`{"messages":[{"id":"m1","uid":10,"modseq":7,"flags":["seen"],"keywords":["$work"],"size":1,"receivedAt":5,"sentAt":null,"blobHash":"h"}],"uidValidity":3}`))
	}))
	defer srv.Close()
	rows, uidv, err := m3Client(t, srv.URL).LabelUids(context.Background(), "mbx", "INBOX", 10, 20, 5000)
	if err != nil {
		t.Fatalf("LabelUids: %v", err)
	}
	if q["uidMin"] != "10" || q["uidMax"] != "20" || q["limit"] != "5000" {
		t.Fatalf("query: %+v", q)
	}
	if uidv != 3 || len(rows) != 1 || rows[0].UID != 10 || rows[0].ModSeq != 7 || rows[0].SentAt != nil || len(rows[0].Keywords) != 1 {
		t.Fatalf("rows=%+v uidv=%d", rows, uidv)
	}
}

// Label expunge returns [{uid, messageId}] objects, not bare UIDs.
func TestExpungeLabelObjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"expunged":[{"uid":5,"messageId":"m1"},{"uid":9,"messageId":"m2"}],"messagesExpunged":1}`))
	}))
	defer srv.Close()
	got, count, err := m3Client(t, srv.URL).ExpungeLabel(context.Background(), "mbx", "INBOX")
	if err != nil {
		t.Fatalf("ExpungeLabel: %v", err)
	}
	if count != 1 || len(got) != 2 || got[0].UID != 5 || got[0].MessageID != "m1" || got[1].UID != 9 {
		t.Fatalf("got %+v count=%d", got, count)
	}
}

// Label rename sends {"name": newName} (NOT newName), matching core.
func TestRenameLabelBodyField(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"renamed":true}`))
	}))
	defer srv.Close()
	if err := m3Client(t, srv.URL).RenameLabel(context.Background(), "mbx", "Old", "New"); err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if !strings.Contains(body, `"name":"New"`) || strings.Contains(body, "newName") {
		t.Fatalf("rename body must be {\"name\":\"New\"}, got %s", body)
	}
}

// Sieve rename sends {"newName": ...} (distinct from labels).
func TestRenameSieveBodyField(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"name":"New","renamed":true}`))
	}))
	defer srv.Close()
	if err := m3Client(t, srv.URL).RenameSieveScript(context.Background(), "mbx", "Old", "New"); err != nil {
		t.Fatalf("RenameSieveScript: %v", err)
	}
	if !strings.Contains(body, `"newName":"New"`) {
		t.Fatalf("sieve rename body must be {\"newName\":\"New\"}, got %s", body)
	}
}

// Sieve PUT 422 surfaces invalid_script with line/col.
func TestSievePutInvalid422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"error":"invalid_script","message":"syntax error","line":3,"col":5}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).PutSieveScript(context.Background(), "mbx", "s", "bad")
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != 422 || ae.Code != "invalid_script" {
		t.Fatalf("want 422 invalid_script, got %v", err)
	}
	if ae.Line != 3 || ae.Col != 5 || ae.Message != "syntax error" {
		t.Fatalf("line/col/message: %d/%d/%q", ae.Line, ae.Col, ae.Message)
	}
}

// Sieve CHECK: a non-compiling script is a 200 with valid=false, NOT an error.
func TestSieveCheckInvalidIs200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"valid":false,"message":"boom","line":2,"col":1}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).CheckSieve(context.Background(), "mbx", "bad")
	if err != nil {
		t.Fatalf("CheckSieve must not error on invalid script: %v", err)
	}
	if res.Valid || res.Message != "boom" || res.Line != 2 {
		t.Fatalf("got %+v", res)
	}
}

// Grouped search sets group=thread and returns no cursor.
func TestSearchGroupedNoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("group") != "thread" || r.URL.Query().Get("q") != "hello" {
			t.Errorf("query: %v", r.URL.Query())
		}
		w.Write([]byte(`{"results":[{"id":"m1","labels":[],"flags":[],"envelopeFrom":"a","envelopeTo":"b","referencesIds":[],"receivedAt":1,"size":1,"blobHash":"h","blobGen":"g"}]}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).Search(context.Background(), "mbx", "hello", "", 0, "", true)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) != 1 || res.NextCursor != "" {
		t.Fatalf("got %+v", res)
	}
}

// Pickup update: an omitted password must not be sent (write-only preserve); a
// cleared name is sent as JSON null.
func TestUpdatePickupPatchBody(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"id":"p1","mailboxId":"mbx","name":null,"host":"h","port":110,"tls":"tls","username":"u","intervalMinutes":15,"deleteAfterFetch":true,"enabled":false,"lastRunAt":null,"lastStatus":null,"lastError":null,"consecutiveFailures":0,"createdAt":1}`))
	}))
	defer srv.Close()
	p, err := m3Client(t, srv.URL).UpdatePickup(context.Background(), "mbx", "p1", map[string]any{"name": nil, "enabled": false})
	if err != nil {
		t.Fatalf("UpdatePickup: %v", err)
	}
	if strings.Contains(body, "password") {
		t.Fatalf("omitted password must not be sent, body=%s", body)
	}
	if !strings.Contains(body, `"name":null`) {
		t.Fatalf("cleared name must be JSON null, body=%s", body)
	}
	if p.Enabled || p.Name != nil {
		t.Fatalf("decoded pickup: %+v", p)
	}
}

// Real booleans on pickups (deleteAfterFetch/enabled), never 0/1.
func TestPickupRealBooleans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"p1","mailboxId":"m","name":"n","host":"h","port":995,"tls":"tls","username":"u","intervalMinutes":15,"deleteAfterFetch":true,"enabled":true,"lastRunAt":123,"lastStatus":"ok","lastError":null,"consecutiveFailures":0,"createdAt":1}`))
	}))
	defer srv.Close()
	p, err := m3Client(t, srv.URL).GetPickup(context.Background(), "m", "p1")
	if err != nil {
		t.Fatalf("GetPickup: %v", err)
	}
	if !p.DeleteAfterFetch || !p.Enabled || p.LastStatus == nil || *p.LastStatus != "ok" {
		t.Fatalf("got %+v", p)
	}
}

// The listing knobs reach the wire, and — the half that is easy to lose — are
// ABSENT when unset. Core defaults to arrival/desc, so a client that always
// spelled a value would be choosing on the user's behalf; and a crawl must keep
// the key it started with, or core refuses the cursor it just handed out.
func TestListOptsQueryParams(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(200)
		w.Write([]byte(`{"messages":[],"threads":[]}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)
	ctx := context.Background()

	if _, err := c.ListMessages(ctx, "mbx", ListOpts{}); err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if queries[0] != "" {
		t.Fatalf("bare listing sent %q, want no query at all", queries[0])
	}

	if _, err := c.ListMessages(ctx, "mbx", ListOpts{
		Label: "INBOX", Limit: 25, Order: "asc", SortBy: "date",
	}); err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, want := range []string{"label=INBOX", "limit=25", "order=asc", "sortBy=date"} {
		if !strings.Contains(queries[1], want) {
			t.Fatalf("query %q missing %q", queries[1], want)
		}
	}

	if _, err := c.ListThreads(ctx, "mbx", ListOpts{SortBy: "date"}); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if !strings.Contains(queries[2], "sortBy=date") {
		t.Fatalf("thread listing dropped sortBy: %q", queries[2])
	}

	// The trash listing takes the same struct, and Label is dropped rather than
	// sent: core answers 400 label_not_applicable for it, so passing one through
	// would turn a caller's harmless leftover into a failed listing.
	if _, err := c.ListTrash(ctx, "mbx", ListOpts{Label: "INBOX", SortBy: "date"}); err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if strings.Contains(queries[3], "label=") {
		t.Fatalf("trash listing sent a label: %q", queries[3])
	}
	for _, want := range []string{"state=expunged", "sortBy=date"} {
		if !strings.Contains(queries[3], want) {
			t.Fatalf("query %q missing %q", queries[3], want)
		}
	}

	// A crawl step keeps every knob and moves only the cursor — the property
	// that makes Depaginate safe under a non-default key.
	step := ListOpts{Label: "INBOX", SortBy: "date", Order: "asc"}.WithCursor("1700000000:01MSG")
	if step.SortBy != "date" || step.Order != "asc" || step.Label != "INBOX" {
		t.Fatalf("WithCursor dropped a knob: %+v", step)
	}
	if step.Cursor != "1700000000:01MSG" {
		t.Fatalf("WithCursor did not set the cursor: %+v", step)
	}
}
