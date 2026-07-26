package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// windowRecorder collects the ?window each stubbed request carried. The screen
// issues its two reads concurrently, so the handler runs on two goroutines and
// the recorder must be synchronized — an unguarded slice here trips -race.
type windowRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (w *windowRecorder) add(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, s)
}

func (w *windowRecorder) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.seen
	w.seen = nil
	return out
}

// dmarcServer stubs the two endpoints the DMARC screen issues together,
// recording the ?window each was asked for so the snapshot-per-fetch discipline
// is testable (the verdict and the rows must describe ONE window).
func dmarcServer(t *testing.T, windows *windowRecorder, empty bool) (*coreapi.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		windows.add(r.URL.Query().Get("window"))
		w.WriteHeader(200)
		switch {
		case strings.HasSuffix(r.URL.Path, "/dmarc/sources"):
			if empty {
				w.Write([]byte(`{"domain":"example.test","windowDays":30,"messages":0,"sources":[]}`))
				return
			}
			// Platform source first on the wire so the unaligned-first sort is
			// what puts the blocking one on top, not the server order.
			w.Write([]byte(`{"domain":"example.test","windowDays":30,"messages":1000,"sources":[
				{"sourceIp":"9.9.9.9","messages":900,"passing":900,"passRate":1,"share":0.9,
				 "dkimPass":900,"spfPass":900,"quarantined":0,"rejected":0,"class":"platform",
				 "dkimDomain":"example.test","dkimSelector":"oe1","spfDomain":"example.test",
				 "headerFrom":"example.test","firstSeen":1750000000,"lastSeen":1752500000},
				{"sourceIp":"1.2.3.4","messages":100,"passing":0,"passRate":0,"share":0.1,
				 "dkimPass":0,"spfPass":0,"quarantined":5,"rejected":2,"class":"unaligned",
				 "dkimDomain":null,"dkimSelector":null,"spfDomain":null,
				 "headerFrom":"example.test","firstSeen":1750000000,"lastSeen":1752500000}
			]}`))
		default:
			if empty {
				w.Write([]byte(`{"domain":"example.test","windowDays":30,
					"totals":{"reports":0,"reporters":0,"sources":0,"messages":0,"passing":0,
					 "dkimAligned":0,"spfAligned":0,"quarantined":0,"rejected":0,"firstSeen":null,"lastSeen":null},
					"readiness":{"verdict":"no_data","currentPolicy":null,"recommendedPolicy":null,
					 "alignedRate":null,"windowDays":30,"observedDays":0,"messages":0,"reporters":0,
					 "sampled":false,"blockers":[{"code":"no_reports","detail":"No aggregate reports received."}]},
					"topSources":[]}`))
				return
			}
			w.Write([]byte(`{"domain":"example.test","windowDays":30,
				"totals":{"reports":42,"reporters":4,"sources":2,"messages":1000,"passing":900,
				 "dkimAligned":900,"spfAligned":900,"quarantined":5,"rejected":2,
				 "firstSeen":1750000000,"lastSeen":1752500000},
				"readiness":{"verdict":"not_ready","currentPolicy":"none","recommendedPolicy":null,
				 "alignedRate":0.9,"windowDays":30,"observedDays":29,"messages":1000,"reporters":4,
				 "sampled":true,"blockers":[
					{"code":"unaligned_source","detail":"1.2.3.4 sent 100 failing messages","messages":100},
					{"code":"pass_rate_below_threshold","detail":"Aligned pass rate is 90.00%","messages":100}]},
				"topSources":[]}`))
		}
	}))
	c, err := coreapi.New(coreapi.Config{BaseURL: srv.URL, Token: "oek_test", RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv.Close
}

func TestDmarcDescSortsUnalignedFirst(t *testing.T) {
	var windows windowRecorder
	c, done := dmarcServer(t, &windows, false)
	defer done()

	d := dmarcDesc("example.test")
	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" {
		t.Fatalf("the source list is limit-bounded, not paginated; got cursor %q", next)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 source rows, got %d", len(rows))
	}
	// The unaligned source is 1/9th the volume of the platform one — it must
	// still lead, because it is the reason enforcement is blocked.
	if !strings.Contains(rows[0].cells[0], "1.2.3.4") {
		t.Errorf("unaligned source must sort first, got %q", rows[0].cells[0])
	}
	if !strings.Contains(rows[0].cells[1], "unaligned") {
		t.Errorf("class cell = %q", rows[0].cells[1])
	}
	// Both endpoints must have been asked for the same window.
	seen := windows.take()
	if len(seen) != 2 || seen[0] != seen[1] || seen[0] != coreapi.DmarcWindowDefault {
		t.Errorf("both reads should snapshot one window (%s), server saw %v", coreapi.DmarcWindowDefault, seen)
	}
}

func TestDmarcDescSummaryAndWindowCycle(t *testing.T) {
	var windows windowRecorder
	c, done := dmarcServer(t, &windows, false)
	defer done()

	d := dmarcDesc("example.test")
	if _, _, err := d.fetch(context.Background(), c, ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	lines := strings.Join(d.summary(200), "\n")
	for _, want := range []string{"not_ready", "p=none", "90.00%", "29/30 days", "UNDERSTATE", "1.2.3.4 sent 100"} {
		if !strings.Contains(lines, want) {
			t.Errorf("summary should contain %q:\n%s", want, lines)
		}
	}

	// R cycles the window; the next fetch must use the new one.
	act := d.actions[0]
	if act.key != "R" {
		t.Fatalf("first action should be the window cycle, got %q", act.key)
	}
	flash, err := act.do(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !strings.Contains(flash, "window: ") {
		t.Errorf("cycle flash = %q", flash)
	}
	windows.take() // drop the first fetch's records
	if _, _, err := d.fetch(context.Background(), c, ""); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	seen := windows.take()
	if len(seen) != 2 || seen[0] == coreapi.DmarcWindowDefault || seen[0] != seen[1] {
		t.Errorf("cycled window should be queried by both reads, server saw %v", seen)
	}
}

// A domain nobody reports on must render a legible "why", not a wall of dashes:
// alignedRate/firstSeen/lastSeen are all null in this state.
func TestDmarcDescNoReports(t *testing.T) {
	var windows windowRecorder
	c, done := dmarcServer(t, &windows, true)
	defer done()

	d := dmarcDesc("example.test")
	rows, _, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no source rows, got %d", len(rows))
	}
	lines := strings.Join(d.summary(200), "\n")
	for _, want := range []string{"no_data", "no aggregate reports ingested", "No aggregate reports received."} {
		if !strings.Contains(lines, want) {
			t.Errorf("empty summary should contain %q:\n%s", want, lines)
		}
	}
}

// The summary must never emit a line wider than the pane, or the table below it
// wraps and the row count the layout reserved goes wrong.
func TestDmarcSummaryRespectsWidth(t *testing.T) {
	var windows windowRecorder
	c, done := dmarcServer(t, &windows, false)
	defer done()

	d := dmarcDesc("example.test")
	if _, _, err := d.fetch(context.Background(), c, ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	const w = 40
	for _, line := range d.summary(w) {
		if got := width(line); got > w {
			t.Errorf("summary line is %d wide, want ≤ %d: %q", got, w, line)
		}
	}
}

// The summary block is a shared screenPane feature: its lines must reach the
// view AND the table must shrink to make room. A stale layout would let the
// last table row fall off the bottom of the pane.
func TestScreenSummaryBlockRendersAndReservesHeight(t *testing.T) {
	// Mirrors the real sequence: the block is empty until the fetch that
	// computes it lands, so the layout must be re-run when the page arrives.
	lines := []string{"verdict  not_ready", "data     1,000 msgs"}
	var filled bool
	s := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}},
		summary: func(width int) []string {
			if !filled {
				return nil
			}
			return lines
		},
	})
	s.setSize(80, 20)
	tall := s.tbl.Height()

	filled = true // the fetch computed the verdict
	p, _ := s.update(pageMsg{paneID: s.id, seq: s.seq, rows: []rowData{{cells: []string{"row"}}}, replace: true})
	s = p.(*screenPane)
	if got := s.tbl.Height(); got != tall-len(lines) {
		t.Errorf("table height = %d, want %d (summary must reserve %d lines)", got, tall-len(lines), len(lines))
	}
	view := s.view()
	for _, l := range lines {
		if !strings.Contains(view, l) {
			t.Errorf("view is missing summary line %q:\n%s", l, view)
		}
	}
	// A screen without a summary must be unaffected by the new code path.
	plain := newScreenPane(t.Context(), &Options{}, resourceDesc{
		name: "Things", columns: []column{{title: "NAME", flex: true}},
	})
	plain.setSize(80, 20)
	if plain.summaryLines() != 0 || plain.tbl.Height() != tall {
		t.Errorf("a summary-less screen changed height: %d vs %d", plain.tbl.Height(), tall)
	}
	if strings.Contains(plain.view(), "verdict") {
		t.Error("a summary-less screen must render no summary block")
	}
}
