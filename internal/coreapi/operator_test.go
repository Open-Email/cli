package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A reply carries only what the user wrote — no recipient, no threading
// headers — and the submission knobs ride the query/headers, not the body.
func TestReplyToThread(t *testing.T) {
	var gotPath, gotQuery, gotDeliveryID string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotDeliveryID = r.Header.Get("X-Delivery-Id")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"deliveryId":"d1","recipients":[{"address":"a@x.test","status":"queued"}],"sentCopy":"stored"}`))
	}))
	defer srv.Close()

	text := "On it."
	no := false
	res, _, err := m3Client(t, srv.URL).ReplyToThread(context.Background(), "01M", "01T",
		ThreadReplyRequest{Text: &text}, SendOptions{Save: &no, DeliveryID: "dlv-1"})
	if err != nil {
		t.Fatalf("ReplyToThread: %v", err)
	}
	if gotPath != "/api/v1/mailboxes/01M/threads/01T/reply" {
		t.Errorf("path = %q", gotPath)
	}
	// save=false must be sent EXPLICITLY: the endpoint defaults to true, so
	// omitting it would silently keep a copy the caller asked not to keep.
	if gotQuery != "save=false" {
		t.Errorf("query = %q, want save=false", gotQuery)
	}
	if gotDeliveryID != "dlv-1" {
		t.Errorf("X-Delivery-Id = %q", gotDeliveryID)
	}
	if gotBody["text"] != "On it." {
		t.Errorf("body = %v", gotBody)
	}
	for _, k := range []string{"to", "inReplyTo", "references", "from"} {
		if _, present := gotBody[k]; present {
			t.Errorf("body must not carry %q — the server derives it", k)
		}
	}
	if len(res.Recipients) != 1 || res.Recipients[0].Status != "queued" {
		t.Errorf("decode: %+v", res)
	}
}

// Unset submission knobs are omitted entirely so the server's own defaults apply.
func TestReplyToThreadOmitsUnsetOptions(t *testing.T) {
	var gotQuery, gotDeliveryID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotDeliveryID = r.URL.RawQuery, r.Header.Get("X-Delivery-Id")
		w.Write([]byte(`{"deliveryId":"d1","recipients":[]}`))
	}))
	defer srv.Close()
	if _, _, err := m3Client(t, srv.URL).ReplyToThread(context.Background(), "01M", "01T",
		ThreadReplyRequest{}, SendOptions{}); err != nil {
		t.Fatalf("ReplyToThread: %v", err)
	}
	if gotQuery != "" || gotDeliveryID != "" {
		t.Errorf("unset options must not be sent: query=%q delivery-id=%q", gotQuery, gotDeliveryID)
	}
}

// Compose files a message without sending: same body as a send, APPEND's query.
func TestComposeMessage(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"status":"delivered","messageId":"01X","uid":7,"uidValidity":1,"threadId":null,"duplicate":false}`))
	}))
	defer srv.Close()

	at := int64(1700000000)
	res, _, err := m3Client(t, srv.URL).ComposeMessage(context.Background(), "01M",
		SendRequest{From: SendAddress{Address: "me@x.test"}, Subject: "Draft"},
		ComposeOptions{Label: "Drafts", Flags: []string{"draft", "seen"}, InternalDate: &at, DeliveryID: "d9"})
	if err != nil {
		t.Fatalf("ComposeMessage: %v", err)
	}
	if gotPath != "/api/v1/mailboxes/01M/messages/compose" {
		t.Errorf("path = %q", gotPath)
	}
	// Flags are comma-joined into ONE parameter, matching APPEND.
	if gotQuery != "flags=draft%2Cseen&internaldate=1700000000&label=Drafts" {
		t.Errorf("query = %q", gotQuery)
	}
	if from, _ := gotBody["from"].(map[string]any); from["address"] != "me@x.test" {
		t.Errorf("body = %v", gotBody)
	}
	if res.Status != "delivered" || res.MessageID != "01X" || res.UID == nil || *res.UID != 7 {
		t.Errorf("decode: %+v", res)
	}
}

// A Sieve-filtered compose is a success with nothing stored.
func TestComposeMessageFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"filtered","deliveryId":"d1","redirected":true}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).ComposeMessage(context.Background(), "01M",
		SendRequest{From: SendAddress{Address: "me@x.test"}}, ComposeOptions{Filter: true})
	if err != nil {
		t.Fatalf("ComposeMessage: %v", err)
	}
	if res.Status != "filtered" || !res.Redirected || res.MessageID != "" {
		t.Errorf("decode: %+v", res)
	}
}

// Core documents compose's 503 as "retry with the same X-Delivery-Id". That
// retry only happens when the request is marked idempotent, and it is only SAFE
// when a delivery id is actually carried — otherwise a replay files the message
// twice.
func TestComposeMessageRetriesOnlyWithADeliveryID(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"superseded"}`))
			return
		}
		w.Write([]byte(`{"status":"delivered","messageId":"01X","uid":1,"uidValidity":1,"threadId":null,"duplicate":false}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)
	req := SendRequest{From: SendAddress{Address: "me@x.test"}}

	res, _, err := c.ComposeMessage(context.Background(), "01M", req, ComposeOptions{DeliveryID: "d1"})
	if err != nil {
		t.Fatalf("with a delivery id the 503 should be retried: %v", err)
	}
	if res.MessageID != "01X" || calls != 2 {
		t.Fatalf("calls = %d, res = %+v", calls, res)
	}

	// Without one, the same 503 must surface rather than be replayed blind.
	calls = 0
	if _, _, err := c.ComposeMessage(context.Background(), "01M", req, ComposeOptions{}); err == nil {
		t.Fatal("without a delivery id the 503 must NOT be retried")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no replay)", calls)
	}
}

func TestLearnMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(202)
		w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()
	status, err := m3Client(t, srv.URL).LearnMessage(context.Background(), "01M", "01X", "spam")
	if err != nil {
		t.Fatalf("LearnMessage: %v", err)
	}
	if gotPath != "/api/v1/mailboxes/01M/messages/01X/learn" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["class"] != "spam" {
		t.Errorf("body = %v", gotBody)
	}
	if status != "accepted" {
		t.Errorf("status = %q", status)
	}
}

func TestSuppressionRoundTrip(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		switch {
		case r.Method == http.MethodDelete:
			w.Write([]byte(`{"deleted":true}`))
		case r.URL.Path == "/api/v1/suppressions":
			w.Write([]byte(`{"suppressions":[{"address":"a@x.test","reason":"hard_bounce","detail":null,
				"sourceDeliveryId":null,"eventCount":2,"firstEventAt":1,"lastEventAt":9}],"nextCursor":"c2"}`))
		default:
			w.Write([]byte(`{"address":"a@x.test","reason":"complaint","detail":"abuse","sourceDeliveryId":"d1",
				"eventCount":1,"firstEventAt":1,"lastEventAt":1}`))
		}
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)

	pg, err := c.ListSuppressions(context.Background(), 50, "")
	if err != nil {
		t.Fatalf("ListSuppressions: %v", err)
	}
	if len(pg.Items) != 1 || pg.Items[0].Detail != nil || pg.NextCursor != "c2" {
		t.Fatalf("list decode: %+v", pg)
	}

	// The address is a path SEGMENT, so its @ must survive escaping intact.
	s, err := c.GetSuppression(context.Background(), "a@x.test")
	if err != nil {
		t.Fatalf("GetSuppression: %v", err)
	}
	if gotPath != "/api/v1/suppressions/a@x.test" {
		t.Errorf("get path = %q", gotPath)
	}
	if s.Reason != "complaint" || s.Detail == nil || *s.Detail != "abuse" {
		t.Errorf("get decode: %+v", s)
	}

	deleted, err := c.LiftSuppression(context.Background(), "a@x.test")
	if err != nil {
		t.Fatalf("LiftSuppression: %v", err)
	}
	if !deleted || gotMethod != http.MethodDelete {
		t.Errorf("lift: deleted=%v method=%s", deleted, gotMethod)
	}
}

// "Not suppressed" is a 404, and callers must be able to tell it apart from a
// real failure — it is the answer "clear to send".
func TestGetSuppressionNotFoundIsAnswerable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).GetSuppression(context.Background(), "a@x.test")
	if !IsNotFound(err) {
		t.Fatalf("want a not-found error, got %v", err)
	}
}

func TestDkimStatusAndRotation(t *testing.T) {
	var gotPaths []string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotQuery = r.URL.RawQuery
		if r.URL.Path == "/api/v1/dkim" {
			w.Write([]byte(`{"configured":true,"dnsRoot":"dkim.open.email","activeSelector":"oe1",
				"nextRunAt":1700000000000,
				"keys":[{"selector":"oe1","state":"active","recordName":"oe1.dkim.open.email",
					"publicTxt":"v=DKIM1; k=rsa; p=AAA","createdAt":1,"publishedAt":2,"activatedAt":3},
					{"selector":"oe2","state":"staged","recordName":"oe2.dkim.open.email",
					"publicTxt":"v=DKIM1; k=rsa; p=BBB","createdAt":4,"publishedAt":null,"activatedAt":null}],
				"cnames":[{"host":"oe1._domainkey","target":"oe1.dkim.open.email"}]}`))
			return
		}
		w.Write([]byte(`{"bootstrapped":false,"activeSelector":"oe1","stagedSelector":"oe2"}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)

	st, err := c.GetDkim(context.Background())
	if err != nil {
		t.Fatalf("GetDkim: %v", err)
	}
	if !st.Configured || st.ActiveSelector == nil || *st.ActiveSelector != "oe1" {
		t.Fatalf("status decode: %+v", st)
	}
	if len(st.Keys) != 2 || st.Keys[1].PublishedAt != nil {
		t.Errorf("a staged-but-unpublished key must decode as a null publishedAt: %+v", st.Keys)
	}
	if st.NextRunAt == nil || *st.NextRunAt != 1700000000000 {
		t.Errorf("nextRunAt = %v", st.NextRunAt)
	}

	if _, err := c.RotateDkim(context.Background()); err != nil {
		t.Fatalf("RotateDkim: %v", err)
	}
	if _, err := c.ActivateDkim(context.Background(), true); err != nil {
		t.Fatalf("ActivateDkim: %v", err)
	}
	if gotQuery != "force=true" {
		t.Errorf("activate query = %q, want force=true", gotQuery)
	}
	want := []string{"/api/v1/dkim", "/api/v1/dkim/rotate", "/api/v1/dkim/activate"}
	for i, p := range want {
		if i >= len(gotPaths) || gotPaths[i] != p {
			t.Fatalf("paths = %v, want %v", gotPaths, want)
		}
	}
}

// A rotation racing the scheduler loses cleanly; the code must survive so a
// caller can say so rather than reporting a generic failure.
func TestRotateDkimConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte(`{"error":"rotation_in_progress"}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).RotateDkim(context.Background())
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != 409 || ae.Code != "rotation_in_progress" {
		t.Fatalf("want a 409 rotation_in_progress, got %v", err)
	}
}
