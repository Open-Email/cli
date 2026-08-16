package coreapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// GetDomainEvents builds ?range=&outcome=&source=&limit=&cursor= (omitting
// empties), hits /domains/:domain/events, and decodes the keyset page —
// including nullable columns (attempt null, size present) and the
// nextCursor (present on a middle page).
func TestGetDomainEventsBuildsQueryAndDecodes(t *testing.T) {
	var gotPath string
	var q url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, q = r.URL.Path, r.URL.Query()
		w.Write([]byte(`{"domain":"x.example","range":"7d","estimated":false,"nextCursor":"CUR2","events":[
			{"eventTime":1784,"source":"inbound","outcome":"delivered","detail":null,"deliveryId":"dlv1",
			 "envelopeFrom":"a@x.test","envelopeTo":"b@x.example","originalAddress":null,"matchedBy":"exact",
			 "routeKind":"mailbox","routeTarget":"mbx:m1","mailboxId":"m1","messageId":"01M",
			 "messageIdHeader":"<id@x>","subject":"Hi","size":2048,"blobHash":"sha","attempt":null,"response":null}
		]}`))
	}))
	defer srv.Close()

	de, err := testClient(t, srv.URL).GetDomainEvents(context.Background(), "x.example", "7d", "delivered", "inbound", 25, "CUR1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/domains/x.example/events" {
		t.Fatalf("path = %q", gotPath)
	}
	for k, want := range map[string]string{"range": "7d", "outcome": "delivered", "source": "inbound", "limit": "25", "cursor": "CUR1"} {
		if got := q.Get(k); got != want {
			t.Fatalf("query %s = %q, want %q", k, got, want)
		}
	}
	if de.NextCursor == nil || *de.NextCursor != "CUR2" {
		t.Fatalf("nextCursor = %v, want CUR2", de.NextCursor)
	}
	if len(de.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(de.Events))
	}
	e := de.Events[0]
	if e.EventTime != 1784 || e.Source != "inbound" || e.Outcome != "delivered" {
		t.Fatalf("bad scalars: %+v", e)
	}
	if e.Attempt != nil {
		t.Fatalf("attempt should decode null → nil, got %v", *e.Attempt)
	}
	if e.Size == nil || *e.Size != 2048 {
		t.Fatalf("size = %v, want 2048", e.Size)
	}
	if e.Subject == nil || *e.Subject != "Hi" || e.Detail != nil {
		t.Fatalf("bad nullable strings: subject=%v detail=%v", e.Subject, e.Detail)
	}
}

// An absent nextCursor is the last-page sentinel — it must decode to a nil
// pointer so --all / Depaginate stops.
func TestGetDomainEventsNullCursorEndsPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"domain":"x.example","range":"24h","estimated":false,"events":[]}`))
	}))
	defer srv.Close()
	de, err := testClient(t, srv.URL).GetDomainEvents(context.Background(), "x.example", "", "", "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if de.NextCursor != nil {
		t.Fatalf("an absent nextCursor should decode to nil, got %q", *de.NextCursor)
	}
}

// Empty range/outcome/source/cursor and a zero limit are omitted from the query
// entirely (core applies its defaults).
func TestGetDomainEventsOmitsEmptyParams(t *testing.T) {
	var q url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write([]byte(`{"domain":"x.example","range":"24h","estimated":false,"events":[]}`))
	}))
	defer srv.Close()
	if _, err := testClient(t, srv.URL).GetDomainEvents(context.Background(), "x.example", "", "", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"range", "outcome", "source", "limit", "cursor"} {
		if q.Has(k) {
			t.Fatalf("empty %s should be omitted, got %q", k, q.Get(k))
		}
	}
}
