package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// Outbound sends the envelope in headers and save/bounce as strict true/false
// query strings; delivered result decodes with nullable coords + sentCopy.
func TestSubmitOutboundHeadersAndFlags(t *testing.T) {
	var hFrom, hTo, hDID, hCT, qSave, qBounce string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hFrom = r.Header.Get("X-Envelope-From")
		hTo = r.Header.Get("X-Envelope-To")
		hDID = r.Header.Get("X-Delivery-Id")
		hCT = r.Header.Get("Content-Type")
		qSave = r.URL.Query().Get("save")
		qBounce = r.URL.Query().Get("bounce")
		w.Write([]byte(`{"status":"delivered","messageId":"m1","uid":3,"uidValidity":7,"threadId":"t","duplicate":false,"sentCopy":"saved","sentThreadId":"st"}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).SubmitOutbound(context.Background(),
		OutboundOptions{EnvelopeFrom: "a@x", EnvelopeTo: "b@y", DeliveryID: "D1", Save: boolPtr(true), Bounce: boolPtr(false)},
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("raw")), nil }, 3)
	if err != nil {
		t.Fatalf("SubmitOutbound: %v", err)
	}
	if hFrom != "a@x" || hTo != "b@y" || hDID != "D1" || hCT != "message/rfc822" {
		t.Fatalf("headers: from=%q to=%q did=%q ct=%q", hFrom, hTo, hDID, hCT)
	}
	if qSave != "true" || qBounce != "false" {
		t.Fatalf("strict flags: save=%q bounce=%q", qSave, qBounce)
	}
	if res.Status != "delivered" || res.SentCopy != "saved" || res.UID == nil || *res.UID != 3 {
		t.Fatalf("result: %+v", res)
	}
}

// Omitted save/bounce must not appear in the query at all.
func TestSubmitOutboundOmitsUnsetFlags(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		w.Write([]byte(`{"status":"queued","deliveryId":"D1"}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).SubmitOutbound(context.Background(),
		OutboundOptions{EnvelopeFrom: "a@x", EnvelopeTo: "b@y", DeliveryID: "D1"},
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }, 1)
	if err != nil {
		t.Fatalf("SubmitOutbound: %v", err)
	}
	if strings.Contains(q, "save") || strings.Contains(q, "bounce") {
		t.Fatalf("unset flags leaked into query: %q", q)
	}
	if res.Status != "queued" {
		t.Fatalf("status: %q", res.Status)
	}
}

// Outbound retries a post-commit 503 (idempotent via X-Delivery-Id / getBody).
func TestSubmitOutboundRetries503(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"reaped"}`))
			return
		}
		w.Write([]byte(`{"status":"delivered","messageId":"m","duplicate":true}`))
	}))
	defer srv.Close()
	res, _, err := m3Client(t, srv.URL).SubmitOutbound(context.Background(),
		OutboundOptions{EnvelopeFrom: "a@x", EnvelopeTo: "b@y", DeliveryID: "D1"},
		func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }, 1)
	if err != nil {
		t.Fatalf("SubmitOutbound: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 || !res.Duplicate {
		t.Fatalf("want 2 calls + duplicate, got calls=%d res=%+v", calls, res)
	}
}

// CheckRecipient: accepted → true; rejection surfaces as an APIError with code.
func TestCheckRecipient(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("to") != "a@x" {
				t.Errorf("to=%q", r.URL.Query().Get("to"))
			}
			w.Write([]byte(`{"accepted":true}`))
		}))
		defer srv.Close()
		ok, err := m3Client(t, srv.URL).CheckRecipient(context.Background(), "a@x", "", "", "", "")
		if err != nil || !ok {
			t.Fatalf("want accepted, got ok=%v err=%v", ok, err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"unknown_address","message":"User unknown"}`))
		}))
		defer srv.Close()
		ok, err := m3Client(t, srv.URL).CheckRecipient(context.Background(), "a@x", "", "", "", "")
		if ok || Code(err) != "unknown_address" {
			t.Fatalf("want unknown_address, got ok=%v err=%v", ok, err)
		}
	})
}

// VerifyLogin decodes nullable accountId + permittedFrom array.
func TestVerifyLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"mailboxId":"mb","accountId":null,"credentialId":"c","kind":"app_password","canSend":true,"permittedFrom":["a@x"]}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).VerifyLogin(context.Background(), "a@x", "pw")
	if err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	if res.MailboxID != "mb" || res.AccountID != nil || !res.CanSend || len(res.PermittedFrom) != 1 {
		t.Fatalf("got %+v", res)
	}
}

// RawRequest returns status + body verbatim even on non-2xx (no APIError, no retry).
func TestRawRequestPassthrough(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != "DELETE" {
			t.Errorf("method=%q", r.Method)
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()
	resp, err := m3Client(t, srv.URL).RawRequest(context.Background(), "DELETE", "/routes/x", nil, nil, nil)
	if err != nil {
		t.Fatalf("RawRequest: %v", err)
	}
	if resp.Status != 404 || !strings.Contains(string(resp.Body), "not_found") {
		t.Fatalf("got status=%d body=%s", resp.Status, resp.Body)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("RawRequest must not retry: %d calls", calls)
	}
}

// Reindex is idempotent → retries a 503.
func TestReindexRetries503(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"reaped"}`))
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"enqueued":4}`))
	}))
	defer srv.Close()
	n, err := m3Client(t, srv.URL).Reindex(context.Background(), "mb", 100)
	if err != nil || n != 4 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("Reindex: n=%d err=%v calls=%d", n, err, calls)
	}
}
