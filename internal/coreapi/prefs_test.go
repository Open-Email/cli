package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Prefs are addressed by identity — the one spelling core serves.
func TestPrefsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"prefs":{"theme":"dark"},"version":3}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)

	p, err := c.GetPrefs(context.Background(), "01M")
	if err != nil {
		t.Fatalf("GetPrefs: %v", err)
	}
	if gotPath != "/api/v1/identities/01M/prefs" {
		t.Errorf("path = %q", gotPath)
	}
	if p.Version != 3 || p.Prefs["theme"] != "dark" {
		t.Errorf("decode: %+v", p)
	}
}

// The version guard rides If-Match; an empty guard is an unconditional upsert
// and must send no header at all.
func TestPutPrefsIfMatch(t *testing.T) {
	var gotIfMatch []string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfMatch = append(gotIfMatch, r.Header.Get("If-Match"))
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"prefs":{},"version":4}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)

	if _, err := c.PutPrefs(context.Background(), "01M", map[string]any{"a": 1}, "3"); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if _, err := c.PutPrefs(context.Background(), "01M", nil, ""); err != nil {
		t.Fatalf("PutPrefs unconditional: %v", err)
	}
	if len(gotIfMatch) != 2 || gotIfMatch[0] != "3" || gotIfMatch[1] != "" {
		t.Fatalf("If-Match sequence = %v", gotIfMatch)
	}
	// A nil map must serialize as {} — the field is required, and null would be
	// refused rather than meaning "no preferences".
	if prefs, ok := gotBody["prefs"].(map[string]any); !ok || prefs == nil {
		t.Errorf("nil prefs must marshal as an object, got %#v", gotBody["prefs"])
	}
}

// A 412 carries the current version so a client can re-read and re-apply.
func TestPutPrefsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(412)
		w.Write([]byte(`{"error":"version_conflict","version":9}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).PutPrefs(context.Background(), "01M", map[string]any{}, "3")
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != 412 || ae.Code != "version_conflict" {
		t.Fatalf("want a 412 version_conflict, got %v", err)
	}
	if v, _ := ae.Extra["version"].(float64); v != 9 {
		t.Errorf("the current version should survive in Extra, got %v", ae.Extra["version"])
	}
}

// The invitation status decodes its all-nullable shape, including the
// not-found case where every field but `found` is null.
func TestInvitationStatus(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"found":false,"calendarId":null,"href":null,"myPartstat":null,"myAddress":"a@x.test"}`))
	}))
	defer srv.Close()
	st, err := m3Client(t, srv.URL).InvitationStatus(context.Background(), "01M", "uid-1")
	if err != nil {
		t.Fatalf("InvitationStatus: %v", err)
	}
	if gotQuery != "uid=uid-1" {
		t.Errorf("query = %q", gotQuery)
	}
	if st.Found || st.CalendarID != nil || st.MyAddress == nil || *st.MyAddress != "a@x.test" {
		t.Fatalf("decode: %+v", st)
	}
}

// The RSVP posts the raw part plus the partstat and reports whether a copy was
// filed and how the organizer was told.
func TestRespondToInvitation(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"partstat":"ACCEPTED","calendarId":"01C","href":"e.ics","myAddress":"a@x.test",
			"filed":true,"etag":"e1","organizerUpdated":false,"replySent":true,"summary":"Standup"}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).RespondToInvitation(context.Background(), "01M", "BEGIN:VCALENDAR", "ACCEPTED")
	if err != nil {
		t.Fatalf("RespondToInvitation: %v", err)
	}
	if gotBody["ics"] != "BEGIN:VCALENDAR" || gotBody["partstat"] != "ACCEPTED" {
		t.Errorf("body = %v", gotBody)
	}
	if !res.Filed || !res.ReplySent || res.OrganizerUpdated || res.Summary == nil || *res.Summary != "Standup" {
		t.Fatalf("decode: %+v", res)
	}
}

// The JSON representation decodes data/writable, and a VTODO-style unmapped
// object answers data:null with the wire text alongside.
func TestGetPimObjectJSON(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte(`{"id":"01O","href":"e.ics","uid":"u","etag":"e","size":10,"contentType":"text/calendar",
			"component":"VTODO","dtstart":null,"dtend":null,"rrule":null,"organizer":null,"attendees":null,
			"sequence":0,"eventStatus":null,"transp":null,"vcardFn":null,"vcardEmail":null,"vcardN":null,
			"createdAt":1,"updatedAt":1,"data":null,"writable":false,"content":"BEGIN:VCALENDAR"}`))
	}))
	defer srv.Close()
	obj, err := m3Client(t, srv.URL).GetPimObjectJSON(context.Background(),
		PimScope{MailboxID: "01M"}, PimCalendars, "work", "e.ics", "")
	if err != nil {
		t.Fatalf("GetPimObjectJSON: %v", err)
	}
	// This read WANTS the JSON representation, so it must not override Accept
	// the way the wire-text read does.
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if obj.Data != nil || obj.Writable || obj.Content != "BEGIN:VCALENDAR" || obj.Href != "e.ics" {
		t.Fatalf("decode: %+v", obj)
	}
}

// A JSON write sends the document as application/json with the preconditions.
func TestPutPimObjectJSON(t *testing.T) {
	var gotCT, gotIfMatch string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT, gotIfMatch = r.Header.Get("Content-Type"), r.Header.Get("If-Match")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"id":"01O","href":"e.ics","etag":"e2","created":false,"syncToken":"t"}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).PutPimObjectJSON(context.Background(),
		PimScope{MailboxID: "01M"}, PimCalendars, "work", "e.ics",
		map[string]any{"@type": "Event", "title": "x"}, PimPutOpts{IfMatch: `"e1"`})
	if err != nil {
		t.Fatalf("PutPimObjectJSON: %v", err)
	}
	if gotCT != "application/json" || gotIfMatch != `"e1"` {
		t.Errorf("headers: ct=%q if-match=%q", gotCT, gotIfMatch)
	}
	if gotBody["@type"] != "Event" {
		t.Errorf("body = %v", gotBody)
	}
	if res.Created || res.Etag != "e2" {
		t.Errorf("decode: %+v", res)
	}
}
