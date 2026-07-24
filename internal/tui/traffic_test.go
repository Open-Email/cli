package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// trafficServer returns a stub core that answers GET
// /domains/:domain/traffic, recording the ?range it was asked for. Rows are
// returned deliberately out of volume order so the descriptor's sort is tested.
func trafficServer(t *testing.T, gotRange *string) (*coreapi.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotRange = r.URL.Query().Get("range")
		w.WriteHeader(200)
		w.Write([]byte(`{
			"domain":"example.test","range":"` + *gotRange + `",
			"totals":{"events":1297,"bytes":50281820},
			"byOutcome":{"delivered":1204,"filtered":88,"bounced":5},
			"rows":[
				{"outcome":"filtered","routeKind":"mailbox","events":88,"bytes":2100000},
				{"outcome":"delivered","routeKind":"mailbox","events":1204,"bytes":48001820},
				{"outcome":"bounced","routeKind":"","events":5,"bytes":180000}
			],
			"estimated":true,"retentionDays":90}`))
	}))
	c, err := coreapi.New(coreapi.Config{BaseURL: srv.URL, Token: "oek_test", RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv.Close
}

func TestTrafficDescSortAndTotals(t *testing.T) {
	var gotRange string
	c, done := trafficServer(t, &gotRange)
	defer done()

	d := trafficDesc("example.test")
	if !contains(d.name, "example.test") {
		t.Fatalf("title should carry the domain: %q", d.name)
	}
	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" {
		t.Fatalf("aggregate must not paginate, got cursor %q", next)
	}
	if gotRange != "24h" {
		t.Fatalf("default range should be 24h, server saw %q", gotRange)
	}
	// Data rows sorted by events desc, then the TOTALS summary row last.
	if len(rows) != 4 {
		t.Fatalf("want 3 data rows + TOTALS, got %d", len(rows))
	}
	wantOutcome := []string{"delivered", "filtered", "bounced"}
	for i, w := range wantOutcome {
		if rows[i].cells[0] != w {
			t.Fatalf("row %d outcome = %q, want %q (unsorted?)", i, rows[i].cells[0], w)
		}
	}
	// Empty route kind renders as a dash, not a blank cell.
	if rows[2].cells[1] != "—" {
		t.Fatalf("empty routeKind should dash, got %q", rows[2].cells[1])
	}
	// Thousands-separated event count.
	if rows[0].cells[2] != "1,204" {
		t.Fatalf("event count formatting = %q, want 1,204", rows[0].cells[2])
	}
	totals := rows[3]
	if totals.cells[0] != "TOTALS (24h)" {
		t.Fatalf("summary row should carry the active range, got %q", totals.cells[0])
	}
	if totals.cells[2] != "1,297" {
		t.Fatalf("totals events = %q, want 1,297", totals.cells[2])
	}
	// The summary row's item is a TrafficRow so `enter` detail never panics.
	if tr, ok := totals.item.(coreapi.TrafficRow); !ok || tr.Outcome != "TOTALS" {
		t.Fatalf("totals item should be a TrafficRow marker, got %#v", totals.item)
	}
}

func TestTrafficDescRangeCycle(t *testing.T) {
	var gotRange string
	c, done := trafficServer(t, &gotRange)
	defer done()

	d := trafficDesc("example.test")
	var rangeAct *action
	for i := range d.actions {
		if d.actions[i].key == "R" {
			rangeAct = &d.actions[i]
		}
	}
	if rangeAct == nil || rangeAct.do == nil || rangeAct.needsRow {
		t.Fatalf("traffic needs a rowless direct range action")
	}
	// R must not collide with the built-in refresh key.
	if rangeAct.key == "r" {
		t.Fatal("range key would shadow refresh")
	}

	// One cycle: 24h → 7d (the entry after 24h in trafficRanges).
	flash, err := rangeAct.do(context.Background(), c, nil)
	if err != nil || flash != "range: 7d" {
		t.Fatalf("cycle flash = %q err %v; want range: 7d", flash, err)
	}
	rows, _, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch after cycle: %v", err)
	}
	if gotRange != "7d" {
		t.Fatalf("cycled range should reach the query, server saw %q", gotRange)
	}
	if rows[len(rows)-1].cells[0] != "TOTALS (7d)" {
		t.Fatalf("summary row should track the cycled range, got %q", rows[len(rows)-1].cells[0])
	}
}

func TestTrafficPickerOpensDomain(t *testing.T) {
	pick := trafficPickerDesc()
	if pick.key != "traffic" || pick.open == nil {
		t.Fatalf("picker should be keyed 'traffic' with a drill-down open")
	}
	aliasOf := "canonical.test"
	p := pick.open(context.Background(), &Options{}, coreapi.Domain{Domain: "alias.test", AliasOf: &aliasOf})
	if p == nil || !contains(p.title(), "alias.test") {
		t.Fatalf("open should drill into the domain's traffic pane, got %v", p)
	}
}

func TestTrafficWiredIntoSidebarAndDescriptors(t *testing.T) {
	if _, ok := allDescriptors()["traffic"]; !ok {
		t.Fatal("allDescriptors missing the traffic screen")
	}
	// Traffic is not system-gated: an account principal must see it too.
	m := newRoot(context.Background(), Options{Role: coreapi.PrincipalAccount})
	found := false
	for _, it := range m.side.items {
		if it.key == "traffic" {
			found = true
			if it.label != "Traffic" {
				t.Fatalf("traffic sidebar label = %q", it.label)
			}
		}
	}
	if !found {
		t.Fatal("Traffic missing from the sidebar for an account principal")
	}
}

func TestFmtInt(t *testing.T) {
	cases := map[int64]string{0: "0", 5: "5", 88: "88", 1204: "1,204", 1000000: "1,000,000", -1204: "-1,204"}
	for in, want := range cases {
		if got := fmtInt(in); got != want {
			t.Fatalf("fmtInt(%d) = %q, want %q", in, got, want)
		}
	}
}
