package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Collection listing unwraps the family-keyed array and relays the family path.
func TestListPimCollections(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"addressbooks":[{"id":"01B","kind":"addressbook","name":"default","displayName":null,"color":null,"description":null,"visibility":"private","role":null,"syncToken":"oe-sync:1:5","createdAt":1,"objectCount":2}]}`))
	}))
	defer srv.Close()
	cols, err := m3Client(t, srv.URL).ListPimCollections(context.Background(),
		PimScope{MailboxID: "m1"}, PimAddressbooks)
	if err != nil {
		t.Fatalf("ListPimCollections: %v", err)
	}
	if gotPath != "/api/v1/identities/m1/addressbooks" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(cols) != 1 || cols[0].Name != "default" || cols[0].ObjectCount != 2 {
		t.Fatalf("bad decode: %+v", cols)
	}
}

// The range query serializes every option, and expand is the string "true".
func TestListPimObjectsQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"objects":[],"window":{"start":100,"end":200,"clamped":true}}`))
	}))
	defer srv.Close()
	start, end := int64(100), int64(200)
	page, err := m3Client(t, srv.URL).ListPimObjects(context.Background(),
		PimScope{MailboxID: "m1"}, PimCalendars, "work", PimObjectListOpts{
			Limit: 10, Cursor: "c1", Fields: "meta", Start: &start, End: &end,
			Component: "VEVENT", Expand: true,
		})
	if err != nil {
		t.Fatalf("ListPimObjects: %v", err)
	}
	for _, want := range []string{"limit=10", "cursor=c1", "fields=meta", "start=100", "end=200", "component=VEVENT", "expand=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if page.Window == nil || !page.Window.Clamped {
		t.Fatalf("window not decoded: %+v", page.Window)
	}
}

// PUT sends the raw body under the family media type with the preconditions,
// and a differing Acting identity rides X-Acting-Identity.
func TestPutPimObjectHeaders(t *testing.T) {
	var gotCT, gotIfMatch, gotActing, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotIfMatch = r.Header.Get("If-Match")
		gotActing = r.Header.Get("X-Acting-Identity")
		gotPath = r.URL.Path
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"01O","href":"evt.ics","etag":"abc","created":true,"syncToken":"oe-sync:1:6"}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).PutPimObject(context.Background(),
		PimScope{MailboxID: "owner1", Acting: "me1"}, PimCalendars, "01C", "evt.ics",
		[]byte("BEGIN:VCALENDAR"), PimPutOpts{IfMatch: `"abc"`})
	if err != nil {
		t.Fatalf("PutPimObject: %v", err)
	}
	if gotPath != "/api/v1/identities/owner1/calendars/01C/objects/evt.ics" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotCT != "text/calendar" || gotIfMatch != `"abc"` || gotActing != "me1" {
		t.Fatalf("headers: ct=%q if-match=%q acting=%q", gotCT, gotIfMatch, gotActing)
	}
	if !res.Created || res.Etag != "abc" {
		t.Fatalf("bad decode: %+v", res)
	}
}

// Acting equal to the owner sends NO X-Acting-Identity (owner mode).
func TestPimScopeOwnerSendsNoActingHeader(t *testing.T) {
	var sawActing bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawActing = r.Header["X-Acting-Identity"]
		w.Write([]byte(`{"calendars":[]}`))
	}))
	defer srv.Close()
	_, err := m3Client(t, srv.URL).ListPimCollections(context.Background(),
		PimScope{MailboxID: "m1", Acting: "m1"}, PimCalendars)
	if err != nil {
		t.Fatalf("ListPimCollections: %v", err)
	}
	if sawActing {
		t.Fatal("X-Acting-Identity sent for an owner-mode call")
	}
}

// Object GET must override the kernel's blanket Accept: application/json —
// core content-negotiates that path (Vary: Accept) and would answer the JSON
// metadata representation instead of the promised wire text.
func TestGetPimObjectAcceptsWireText(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		w.Write([]byte("BEGIN:VCARD\r\nEND:VCARD\r\n"))
	}))
	defer srv.Close()
	body, _, err := m3Client(t, srv.URL).GetPimObject(context.Background(),
		PimScope{MailboxID: "m1"}, PimAddressbooks, "default", "a.vcf", "")
	if err != nil {
		t.Fatalf("GetPimObject: %v", err)
	}
	defer body.Close()
	if gotAccept != "text/vcard" {
		t.Fatalf("Accept = %q, want text/vcard", gotAccept)
	}
}

// The sync diff decodes changed refs + deleted hrefs and passes since/limit.
func TestPimCollectionChanges(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"syncToken":"oe-sync:1:9","changed":[{"href":"a.ics","etag":"e1","id":"01A","uid":"u1"}],"deleted":["b.ics"],"truncated":true}`))
	}))
	defer srv.Close()
	ch, err := m3Client(t, srv.URL).PimCollectionChanges(context.Background(),
		PimScope{MailboxID: "m1"}, PimCalendars, "work", "oe-sync:1:5", 100)
	if err != nil {
		t.Fatalf("PimCollectionChanges: %v", err)
	}
	if !strings.Contains(gotQuery, "since=oe-sync%3A1%3A5") || !strings.Contains(gotQuery, "limit=100") {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(ch.Changed) != 1 || ch.Changed[0].UID != "u1" || len(ch.Deleted) != 1 || !ch.Truncated {
		t.Fatalf("bad decode: %+v", ch)
	}
}

// RSVP posts the partstat and decodes the mutually-exclusive outcome pair.
func TestRespondPimObject(t *testing.T) {
	var gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotPath = string(b), r.URL.Path
		w.Write([]byte(`{"partstat":"ACCEPTED","etag":"e2","organizerUpdated":false,"replySent":true}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).RespondPimObject(context.Background(),
		PimScope{MailboxID: "m1"}, "work", "evt.ics", "ACCEPTED")
	if err != nil {
		t.Fatalf("RespondPimObject: %v", err)
	}
	if gotPath != "/api/v1/identities/m1/calendars/work/objects/evt.ics/respond" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody != `{"partstat":"ACCEPTED"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if res.OrganizerUpdated || !res.ReplySent {
		t.Fatalf("bad decode: %+v", res)
	}
}

// A 207 partial import is a success at the transport layer; FailedCount carries
// the tally and failed items keep their error.
func TestImportPimCollection207(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		w.Write([]byte(`{"items":[{"uid":"u1","href":"u1.ics","status":"created","etag":"e1"},{"uid":"u2","href":null,"status":"failed","error":"object_too_large"}],"createdCount":1,"replacedCount":0,"failedCount":1,"syncToken":"oe-sync:1:7"}`))
	}))
	defer srv.Close()
	res, err := m3Client(t, srv.URL).ImportPimCollection(context.Background(),
		PimScope{MailboxID: "m1"}, PimCalendars, "work", []byte("BEGIN:VCALENDAR"))
	if err != nil {
		t.Fatalf("ImportPimCollection: %v", err)
	}
	if res.FailedCount != 1 || res.Items[1].Href != nil || res.Items[1].Error != "object_too_large" {
		t.Fatalf("bad decode: %+v", res)
	}
}

// Token mint sends only the provided fields and decodes the one-time reveal.
func TestCreatePimToken(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"01T","collectionId":"01C","kind":"calendar","label":"team","expiresAt":null,"accessCount":0,"lastAccessedAt":null,"createdAt":1,"token":"oek_feed","url":"https://api.example/api/v1/pim/feeds/oek_feed"}`))
	}))
	defer srv.Close()
	tk, err := m3Client(t, srv.URL).CreatePimToken(context.Background(),
		PimScope{MailboxID: "m1"}, PimCalendars, "work", "team", 0)
	if err != nil {
		t.Fatalf("CreatePimToken: %v", err)
	}
	if gotBody != `{"label":"team"}` {
		t.Fatalf("body = %q (expiresAt must be omitted when 0)", gotBody)
	}
	if tk.Token != "oek_feed" || tk.URL == "" || tk.ID != "01T" {
		t.Fatalf("bad decode: %+v", tk)
	}
}

// The feed path needs no bearer and streams the raw body.
func TestGetPimFeedUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Write([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, hdr, err := c.GetPimFeed(context.Background(), "tok123")
	if err != nil {
		t.Fatalf("GetPimFeed: %v", err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if !strings.Contains(string(data), "BEGIN:VCALENDAR") || !strings.Contains(hdr.Get("Content-Type"), "text/calendar") {
		t.Fatalf("bad stream: %q %q", data, hdr.Get("Content-Type"))
	}
}

// Subscribe posts to the publicId path and decodes the directory row.
func TestSubscribePimPublic(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"id":"01P","ownerIdentityId":"m2","collectionId":"01C","kind":"calendar","displayName":"Team","description":null,"category":null,"createdAt":1}`))
	}))
	defer srv.Close()
	pub, err := m3Client(t, srv.URL).SubscribePimPublic(context.Background(), PimScope{MailboxID: "m1"}, "01P")
	if err != nil {
		t.Fatalf("SubscribePimPublic: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/api/v1/identities/m1/pim/subscriptions/01P" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if pub.OwnerIdentityID != "m2" || pub.DisplayName == nil {
		t.Fatalf("bad decode: %+v", pub)
	}
}
