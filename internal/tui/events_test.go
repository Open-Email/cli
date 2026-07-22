package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openemail/openemail-cli/internal/coreapi"
)

// eventsServer stubs GET /domains/:domain/events, recording the ?range and
// ?cursor it was asked for, and returns two events (newest first) plus a next
// cursor so load-more/keyset behavior is exercisable.
func eventsServer(t *testing.T, gotRange, gotCursor *string) (*coreapi.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotRange = r.URL.Query().Get("range")
		*gotCursor = r.URL.Query().Get("cursor")
		w.WriteHeader(200)
		w.Write([]byte(`{
			"domain":"example.test","range":"` + *gotRange + `","estimated":false,"cursor":"NEXT",
			"events":[
				{"eventTime":100,"source":"inbound","outcome":"delivered","envelopeFrom":"a@x.test",
				 "envelopeTo":"b@example.test","subject":"Hi there","detail":null,"deliveryId":"d2","attempt":null},
				{"eventTime":90,"source":"relay","outcome":"relayed","envelopeFrom":"c@example.test",
				 "envelopeTo":"d@remote.test","subject":null,"detail":"mta_250","deliveryId":"d1","attempt":2}
			]}`))
	}))
	c, err := coreapi.New(coreapi.Config{BaseURL: srv.URL, Token: "oek_test", RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv.Close
}

func TestTrafficEventsFetchOrderCursorAndCaption(t *testing.T) {
	var gotRange, gotCursor string
	c, done := eventsServer(t, &gotRange, &gotCursor)
	defer done()

	d := trafficEventsDesc("example.test")
	if !contains(d.name, "example.test") {
		t.Fatalf("title should carry the domain: %q", d.name)
	}
	// The freshness caveat must be present and must not imply real-time.
	if d.caption == "" || !contains(d.caption, "audit") || !contains(d.caption, "not the live") {
		t.Fatalf("events pane needs an honest freshness caption, got %q", d.caption)
	}

	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotRange != "24h" {
		t.Fatalf("default range should be 24h, server saw %q", gotRange)
	}
	if next != "NEXT" {
		t.Fatalf("server cursor should drive load-more, got %q", next)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 event rows, got %d", len(rows))
	}
	// Rows preserve server (newest-first) order; TIME/SOURCE/OUTCOME up front.
	if rows[0].cells[1] != "inbound" || rows[1].cells[1] != "relay" {
		t.Fatalf("row order not preserved: %v / %v", rows[0].cells, rows[1].cells)
	}
	// A null subject renders as a dash, not a blank cell.
	if rows[1].cells[5] != "—" {
		t.Fatalf("null subject should dash, got %q", rows[1].cells[5])
	}

	// Page 2: the returned cursor is threaded back into the query (real server
	// pagination, unlike the single-shot aggregate).
	if _, _, err := d.fetch(context.Background(), c, next); err != nil {
		t.Fatalf("fetch page 2: %v", err)
	}
	if gotCursor != "NEXT" {
		t.Fatalf("cursor should reach the query on load-more, server saw %q", gotCursor)
	}
}

func TestTrafficEventsRangeCycleReachesQuery(t *testing.T) {
	var gotRange, gotCursor string
	c, done := eventsServer(t, &gotRange, &gotCursor)
	defer done()

	d := trafficEventsDesc("example.test")
	var rangeAct *action
	for i := range d.actions {
		if d.actions[i].key == "R" {
			rangeAct = &d.actions[i]
		}
	}
	if rangeAct == nil || rangeAct.do == nil || rangeAct.needsRow {
		t.Fatal("events pane needs a rowless direct range action")
	}
	if rangeAct.key == "r" {
		t.Fatal("range key would shadow the built-in refresh")
	}
	flash, err := rangeAct.do(context.Background(), c, nil)
	if err != nil || flash != "range: 7d" {
		t.Fatalf("cycle flash = %q err %v; want range: 7d", flash, err)
	}
	if _, _, err := d.fetch(context.Background(), c, ""); err != nil {
		t.Fatalf("fetch after cycle: %v", err)
	}
	if gotRange != "7d" {
		t.Fatalf("cycled range should reach the query, server saw %q", gotRange)
	}
}

func TestTrafficEventsDetailRendersFields(t *testing.T) {
	var gotRange, gotCursor string
	c, done := eventsServer(t, &gotRange, &gotCursor)
	defer done()
	d := trafficEventsDesc("example.test")
	rows, _, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	find := func(item any, key string) string {
		for _, row := range d.detail(item) {
			if row.k == key {
				return row.v
			}
		}
		return "<missing>"
	}
	// The queued relay event carries a real attempt counter and an MTA detail.
	relay := rows[1].item
	if got := find(relay, "attempt"); got != "2" {
		t.Fatalf("attempt detail = %q, want 2", got)
	}
	if got := find(relay, "outcome"); got != "relayed" {
		t.Fatalf("outcome detail = %q", got)
	}
	if got := find(relay, "detail"); got != "mta_250" {
		t.Fatalf("detail line = %q", got)
	}
	// The endpoint (inbound) row has a null attempt → dash, never a panic/0.
	if got := find(rows[0].item, "attempt"); got != "—" {
		t.Fatalf("null attempt should dash, got %q", got)
	}
}

func TestTrafficEventsSummaryActionOpensAggregate(t *testing.T) {
	d := trafficEventsDesc("example.test")
	var summary *action
	for i := range d.actions {
		if d.actions[i].key == "s" {
			summary = &d.actions[i]
		}
	}
	if summary == nil || summary.run == nil {
		t.Fatal("events pane should offer an 's summary' action opening the aggregate")
	}
	p := summary.run(context.Background(), &Options{}, nil)
	if p == nil || !contains(p.title(), "example.test") {
		t.Fatalf("summary should open the domain's aggregate pane, got %v", p)
	}
}
