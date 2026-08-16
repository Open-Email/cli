package coreapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, baseURL string) *Client {
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

func TestErrorEnvelopeDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"validation_failed","issues":[{"path":"query.limit","code":"too_big","message":"too big"}],"maxMailboxes":5}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).ListAPIKeys(context.Background(), 0, "")
	ae, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %v", err)
	}
	if ae.Status != 400 || ae.Code != "validation_failed" {
		t.Fatalf("status/code: %d %q", ae.Status, ae.Code)
	}
	if !IsValidation(err) {
		t.Fatal("IsValidation false")
	}
	if len(ae.Issues) != 1 || ae.Issues[0].Path != "query.limit" {
		t.Fatalf("issues: %+v", ae.Issues)
	}
	if ae.Extra["maxMailboxes"] == nil {
		t.Fatalf("extra not preserved: %+v", ae.Extra)
	}
}

func TestRetryOn503ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"reaped"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"apiKeys":[],"nextCursor":""}`))
	}))
	defer srv.Close()

	if _, err := testClient(t, srv.URL).ListAPIKeys(context.Background(), 0, ""); err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
}

func TestNoRetryOn503ForNonIdempotentCreate(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"reaped"}`))
	}))
	defer srv.Close()

	// A POST create carries no idempotency key; a 503 could be post-commit, so it
	// must NOT be replayed (would duplicate the resource).
	_, err := testClient(t, srv.URL).CreateMailbox(context.Background(), MailboxCreateInput{})
	if Status(err) != 503 {
		t.Fatalf("want 503, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("non-idempotent 503 must not retry: %d attempts", got)
	}
}

func TestDepaginateStallGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"mailboxes":[{"id":"x"}],"nextCursor":"stuck"}`)) // never advances
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	_, err := Depaginate(context.Background(), func(ctx context.Context, cur string) (Page[Mailbox], error) {
		return c.ListMailboxes(ctx, 0, cur)
	})
	if err == nil {
		t.Fatal("want a stall error, got nil (would loop forever in production)")
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).GetMailbox(context.Background(), "x")
	if !IsNotFound(err) {
		t.Fatalf("want 404, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("4xx must not retry: %d attempts", got)
	}
}

func TestRawPathNotDoubleEncoded(t *testing.T) {
	var escapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escapedPath = r.URL.EscapedPath()
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"x","primaryAddress":null,"quotaBytes":null,"accountId":null,"createdAt":0}`))
	}))
	defer srv.Close()

	// An id with a space and a percent: PathEscape → "a%20b%25c". The kernel must
	// publish that verbatim (RawPath), not re-escape to "a%2520b%2525c".
	if _, err := testClient(t, srv.URL).GetMailbox(context.Background(), "a b%c"); err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if want := "/api/v1/mailboxes/a%20b%25c"; escapedPath != want {
		t.Fatalf("escaped path\n got %q\nwant %q", escapedPath, want)
	}
}

func TestDepaginate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"mailboxes":[{"id":"1"},{"id":"2"}],"nextCursor":"c2"}`))
		} else {
			w.Write([]byte(`{"mailboxes":[{"id":"3"}]}`))
		}
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	all, err := Depaginate(context.Background(), func(ctx context.Context, cur string) (Page[Mailbox], error) {
		return c.ListMailboxes(ctx, 0, cur)
	})
	if err != nil {
		t.Fatalf("Depaginate: %v", err)
	}
	if len(all) != 3 || all[0].ID != "1" || all[2].ID != "3" {
		t.Fatalf("got %+v", all)
	}
}

// TestStreamingUploadNotBoundedByRequestTimeout guards the fix that exempts
// streaming uploads (getBody) from the short per-request timeout. The server
// stalls far longer than RequestTimeout but the upload must still succeed —
// otherwise a large/slow send would abort mid-body and (being idempotent) retry
// and fail the same way.
func TestStreamingUploadNotBoundedByRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		time.Sleep(250 * time.Millisecond) // longer than RequestTimeout below
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"queued"}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL:         srv.URL,
		Token:           "oek_test",
		RequestTimeout:  60 * time.Millisecond, // far shorter than the server stall
		RetryBackoffMin: time.Millisecond,
		RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := []byte("From: a@x\r\nTo: b@y\r\n\r\nhi\r\n")
	getBody := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	res, _, err := c.DeliverInbound(context.Background(),
		InboundOptions{EnvelopeFrom: "a@x", EnvelopeTo: "b@y", DeliveryID: "01TESTDELIVERY000000000000"},
		getBody, int64(len(body)))
	if err != nil {
		t.Fatalf("streaming upload must not be bounded by RequestTimeout, got: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("status = %q, want queued", res.Status)
	}
}

// TestOrdinaryCallStillBoundedByRequestTimeout is the control: a plain buffered
// GET must still honor the per-request timeout, so the upload exemption did not
// disable timeouts everywhere.
func TestOrdinaryCallStillBoundedByRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"x","primaryAddress":null,"quotaBytes":null,"accountId":null,"createdAt":0}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL:         srv.URL,
		Token:           "oek_test",
		RequestTimeout:  60 * time.Millisecond,
		RetryBackoffMin: time.Millisecond,
		RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetMailbox(context.Background(), "x"); err == nil {
		t.Fatal("want a timeout error for a stalled ordinary call, got nil")
	}
}

func TestEscapedPathSegmentPreservation(t *testing.T) {
	var requestedURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedURI = r.RequestURI
		w.WriteHeader(200)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	// Query with special characters in path segment: user+tag@domain.com and slash/part
	segment := escapeSegment("user+tag@domain.com")
	_, err := c.GetRoute(context.Background(), "user+tag@domain.com")
	if err != nil {
		t.Fatalf("GetRoute failed: %v", err)
	}
	if !strings.Contains(requestedURI, segment) {
		t.Errorf("expected RequestURI %q to contain escaped segment %q", requestedURI, segment)
	}
}
