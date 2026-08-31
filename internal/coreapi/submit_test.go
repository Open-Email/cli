package coreapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 200 is the ordinary send: per-recipient outcomes, nothing pending.
func TestSubmitDraftSent(t *testing.T) {
	var gotPath, gotMethod, gotDeliveryID, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotQuery = r.URL.RawQuery
		gotDeliveryID = r.Header.Get("X-Delivery-Id")
		w.Write([]byte(`{"deliveryId":"d1","recipients":[{"address":"a@x.test","status":"queued"}],"sentCopy":"stored"}`))
	}))
	defer srv.Close()

	no := false
	res, _, err := m3Client(t, srv.URL).SubmitDraft(context.Background(), "01M", "01MSG",
		SubmitOptions{Bounce: &no, DeliveryID: "dlv-1"})
	if err != nil {
		t.Fatalf("SubmitDraft: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/mailboxes/01M/messages/01MSG/submit" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	// bounce=false must be sent EXPLICITLY: core defaults it to true, so omitting
	// it would deliver a DSN the caller opted out of.
	if gotQuery != "bounce=false" {
		t.Errorf("query = %q, want bounce=false", gotQuery)
	}
	if gotDeliveryID != "dlv-1" {
		t.Errorf("X-Delivery-Id = %q", gotDeliveryID)
	}
	if res.Pending != nil {
		t.Fatalf("Pending = %+v, want nil on a 200", res.Pending)
	}
	if res.Sent == nil || len(res.Sent.Recipients) != 1 {
		t.Fatalf("Sent = %+v", res.Sent)
	}
	if res.Status != http.StatusOK {
		t.Errorf("Status = %d", res.Status)
	}
}

// The status SELECTS the shape. A 202 is a ScheduledSend, and decoding it as a
// SendResult would yield a plausible-looking struct with no recipients — a
// submission core is still working on, reported as one that reached nobody.
func TestSubmitDraftPendingDecodesAsScheduled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{
			"scheduleId":"01S","deliveryId":"d1","messageId":"01MSG","threadId":null,
			"sendAt":1700000000,"releaseAt":1700000000,"state":"pending",
			"recipients":["a@x.test"],"attempts":1,"deferrals":1,
			"createdAt":1700000000,"finishedAt":null,
			"result":[{"address":"a@x.test","status":"deferred","deliveryId":"d1#rcpt:a@x.test"}],
			"error":null
		}`))
	}))
	defer srv.Close()

	res, _, err := m3Client(t, srv.URL).SubmitDraft(context.Background(), "01M", "01MSG", SubmitOptions{})
	if err != nil {
		t.Fatalf("SubmitDraft: %v", err)
	}
	if res.Sent != nil {
		t.Fatalf("Sent = %+v, want nil on a 202", res.Sent)
	}
	if res.Pending == nil {
		t.Fatal("Pending = nil, want the scheduled record")
	}
	if res.Status != http.StatusAccepted {
		t.Errorf("Status = %d", res.Status)
	}
	if res.Pending.State != "pending" || res.Pending.ScheduleID != "01S" {
		t.Errorf("pending = %+v", res.Pending)
	}
	if res.Pending.FinishedAt != nil || res.Pending.Error != nil {
		t.Errorf("nulls decoded as values: %+v", res.Pending)
	}
	if len(res.Pending.Result) != 1 || res.Pending.Result[0].Status != "deferred" {
		t.Errorf("result = %+v", res.Pending.Result)
	}
}

// Without a delivery id nothing is sent for it, and the call is NOT marked
// idempotent — an unkeyed re-submission is how one draft gets sent twice.
func TestSubmitDraftOmitsUnsetOptions(t *testing.T) {
	var gotQuery, gotDeliveryID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotDeliveryID = r.URL.RawQuery, r.Header.Get("X-Delivery-Id")
		w.Write([]byte(`{"deliveryId":"d1","recipients":[]}`))
	}))
	defer srv.Close()

	if _, _, err := m3Client(t, srv.URL).SubmitDraft(context.Background(), "01M", "01MSG", SubmitOptions{}); err != nil {
		t.Fatalf("SubmitDraft: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
	if gotDeliveryID != "" {
		t.Errorf("X-Delivery-Id = %q, want empty", gotDeliveryID)
	}
}
