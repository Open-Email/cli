package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A mailbox that never configured an auto-reply reads as disabled with null
// fields — there is no 404-means-empty state to special-case (the mistake the
// prefs screen made).
func TestGetVacationUnset(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"isEnabled":false,"fromDate":null,"toDate":null,"subject":null,
			"textBody":null,"htmlBody":null,"state":"0"}`))
	}))
	defer srv.Close()
	v, err := m3Client(t, srv.URL).GetVacation(context.Background(), "01M")
	if err != nil {
		t.Fatalf("GetVacation: %v", err)
	}
	if gotPath != "/api/v1/mailboxes/01M/vacation" {
		t.Errorf("path = %q", gotPath)
	}
	if v.IsEnabled || v.Subject != nil || v.TextBody != nil || v.State != "0" {
		t.Fatalf("decode: %+v", v)
	}
}

// The state token rides If-Match, and an empty guard must send no header at all
// — an unconditional overwrite is a different request from a guarded one.
func TestPutVacationIfMatch(t *testing.T) {
	var gotIfMatch []string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfMatch = append(gotIfMatch, r.Header.Get("If-Match"))
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"isEnabled":true,"fromDate":null,"toDate":null,"subject":null,
			"textBody":null,"htmlBody":null,"state":"7"}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)

	text := "Back on the 15th."
	if _, err := c.PutVacation(context.Background(), "01M",
		VacationInput{IsEnabled: true, TextBody: &text}, "6"); err != nil {
		t.Fatalf("PutVacation: %v", err)
	}
	if _, err := c.PutVacation(context.Background(), "01M",
		VacationInput{IsEnabled: false}, ""); err != nil {
		t.Fatalf("PutVacation unconditional: %v", err)
	}
	if len(gotIfMatch) != 2 || gotIfMatch[0] != "6" || gotIfMatch[1] != "" {
		t.Fatalf("If-Match sequence = %v", gotIfMatch)
	}
	// isEnabled is required, so it must be on the wire even when false —
	// omitempty here would make "turn it off" indistinguishable from "leave it".
	if enabled, present := gotBody["isEnabled"]; !present || enabled != false {
		t.Errorf("isEnabled must always be sent, got %v (present=%v)", enabled, present)
	}
	// The nullable fields ARE omitted when unset: the PUT replaces the document,
	// and core defaults an absent field to null.
	if _, present := gotBody["subject"]; present {
		t.Errorf("an unset subject should be omitted, got %v", gotBody["subject"])
	}
}

func TestPutVacationConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(412)
		w.Write([]byte(`{"error":"state_conflict"}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).PutVacation(context.Background(), "01M",
		VacationInput{IsEnabled: true}, "stale")
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != 412 || ae.Code != "state_conflict" {
		t.Fatalf("want a 412 state_conflict, got %v", err)
	}
}

// A batch restore reports per-message outcomes in REQUEST order, and a missing
// id must not refuse the others.
func TestRestoreMessages(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"results":[
			{"id":"A","status":"restored","message":{"id":"A","labels":[{"name":"INBOX","uid":9,"uidValidity":1}]}},
			{"id":"B","status":"not_found"}
		]}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).RestoreMessages(context.Background(), "01M", []string{"A", "B"})
	if err != nil {
		t.Fatalf("RestoreMessages: %v", err)
	}
	if gotPath != "/api/v1/mailboxes/01M/messages/restore" {
		t.Errorf("path = %q", gotPath)
	}
	ids, _ := gotBody["ids"].([]any)
	if len(ids) != 2 || ids[0] != "A" {
		t.Errorf("body ids = %v", gotBody["ids"])
	}
	if len(res.Results) != 2 {
		t.Fatalf("results = %+v", res.Results)
	}
	// `message` is present iff the entry restored — a not_found row carrying a
	// message would mean the client shows labels for something still in trash.
	if res.Results[0].Message == nil || res.Results[0].Message.Labels[0].UID != 9 {
		t.Errorf("restored entry should carry its fresh label UIDs: %+v", res.Results[0])
	}
	if res.Results[1].Status != "not_found" || res.Results[1].Message != nil {
		t.Errorf("not_found entry = %+v", res.Results[1])
	}
}
