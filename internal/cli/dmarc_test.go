package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func testPrinter() (*Printer, *bytes.Buffer) {
	var buf bytes.Buffer
	// color off so assertions match on plain text; out and err share the buffer
	// because the summary deliberately splits across both.
	return &Printer{color: false, out: &buf, err: &buf}, &buf
}

// The empty case is the one that bites: a domain nobody has reported on yet has
// null alignedRate/firstSeen/lastSeen, so every figure would be a dash and a
// naive renderer would deref nil. It must say "no reports yet" and stop.
func TestPrintDmarcSummaryNoReports(t *testing.T) {
	p, buf := testPrinter()
	d := &coreapi.DomainDmarc{
		Domain:     "example.test",
		WindowDays: 30,
		Totals:     coreapi.DmarcTotals{}, // all zero, FirstSeen/LastSeen nil
		Readiness: coreapi.DmarcReadiness{
			Verdict:    "no_data",
			WindowDays: 30,
			Blockers: []coreapi.DmarcBlocker{
				{Code: "no_reports", Detail: "No aggregate reports received. Check the rua= record."},
			},
		},
		TopSources: nil,
	}
	printDmarcSummary(p.out, p, d)
	out := buf.String()
	for _, want := range []string{"example.test", "no_data", "no reports yet", "Check the rua= record."} {
		if !strings.Contains(out, want) {
			t.Errorf("empty summary should mention %q:\n%s", want, out)
		}
	}
	// No table headers: an empty grid reads as "we looked and found rows".
	if strings.Contains(out, "SOURCE") || strings.Contains(out, "BLOCKER") {
		t.Errorf("empty summary must not print a table:\n%s", out)
	}
}

func TestPrintDmarcSummaryPopulated(t *testing.T) {
	p, buf := testPrinter()
	rate := 0.9731
	srcRate := 0.5
	cur, rec := "none", "quarantine"
	first, last := int64(1750000000), int64(1752500000)
	d := &coreapi.DomainDmarc{
		Domain:     "example.test",
		WindowDays: 30,
		Totals: coreapi.DmarcTotals{
			Reports: 42, Reporters: 4, Sources: 7,
			Messages: 12690, Passing: 12349, Quarantined: 12, Rejected: 3,
			SPFAligned: 11000, DKIMAligned: 12300,
			FirstSeen: &first, LastSeen: &last,
		},
		Readiness: coreapi.DmarcReadiness{
			Verdict: "not_ready", CurrentPolicy: &cur, RecommendedPolicy: &rec,
			AlignedRate: &rate, Messages: 12690, Reporters: 4,
			ObservedDays: 29, WindowDays: 30, Sampled: true,
			Blockers: []coreapi.DmarcBlocker{
				{Code: "unaligned_source", Detail: "1.2.3.4 sent 40 failing messages", Messages: 40},
				{Code: "short_window", Detail: "Reports cover 3 day(s)"},
			},
		},
		TopSources: []coreapi.DmarcSource{
			{SourceIP: "1.2.3.4", Class: "unaligned", Messages: 80, Passing: 40, PassRate: &srcRate, Share: 0.0063},
		},
	}
	printDmarcSummary(p.out, p, d)
	out := buf.String()
	for _, want := range []string{
		"not_ready",
		"none → quarantine",
		"97.31%",       // aligned rate as a percentage
		"12,349",       // thousands separators on counts
		"29 of 30",     // observed vs requested days
		"UNDERSTATE",   // the sampled caveat must be shouted, not buried
		"BLOCKER",      // blocker table
		"short_window", // the blocker with no message count
		"1.2.3.4",      // top source
		"0.6%",         // share
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary should contain %q:\n%s", want, out)
		}
	}
	// A blocker carrying no message count renders a dash, never a bare 0.
	if strings.Contains(out, "short_window  Reports cover 3 day(s)  0") {
		t.Errorf("a count-less blocker must render — not 0:\n%s", out)
	}
}

// at_enforcement and the ready_* verdicts recommend nothing, so the transition
// must not read as "→ <nil>".
func TestDmarcPolicyTransitionNoRecommendation(t *testing.T) {
	at := "reject"
	got := dmarcPolicyTransition(coreapi.DmarcReadiness{Verdict: "at_enforcement", CurrentPolicy: &at})
	if !strings.Contains(got, "reject") || !strings.Contains(got, "no change recommended") {
		t.Errorf("at_enforcement transition = %q", got)
	}
	// currentPolicy is null until a report names one.
	if got := dmarcPolicyTransition(coreapi.DmarcReadiness{Verdict: "no_data"}); !strings.HasPrefix(got, "unknown") {
		t.Errorf("null currentPolicy should render as unknown, got %q", got)
	}
}

func TestDmarcRangeValidation(t *testing.T) {
	// The traffic/events ranges are the trap: they are a different set, and
	// typing one out of habit must be answered locally with the right one.
	if err := checkDmarcRange("24h"); err == nil {
		t.Fatal("24h is a traffic range, not a DMARC window — should be rejected")
	} else if !strings.Contains(err.Error(), "7d|30d|90d") {
		t.Errorf("rejection should name the accepted set, got %v", err)
	}
	for _, w := range coreapi.DmarcRanges {
		if err := checkDmarcRange(w); err != nil {
			t.Errorf("checkDmarcRange(%q) = %v", w, err)
		}
	}
}

func TestFmtReportPolicy(t *testing.T) {
	p, sp := "quarantine", "none"
	pct := int64(20)
	// sp is shown only when it diverges from p (the parent/child split).
	if got := fmtReportPolicy(coreapi.DmarcReport{PolicyP: &p, PolicySp: &sp, PolicyPct: &pct}); got != "p=quarantine sp=none pct=20" {
		t.Errorf("full policy = %q", got)
	}
	same := "quarantine"
	full := int64(100)
	if got := fmtReportPolicy(coreapi.DmarcReport{PolicyP: &p, PolicySp: &same, PolicyPct: &full}); got != "p=quarantine" {
		t.Errorf("a matching sp and pct=100 add nothing, got %q", got)
	}
	if got := fmtReportPolicy(coreapi.DmarcReport{Truncated: true}); got != "p=? truncated" {
		t.Errorf("missing policy + truncated = %q", got)
	}
}

func TestFmtRateAndCount(t *testing.T) {
	if got := fmtRate(nil); got != "—" {
		t.Errorf("nil rate = %q, want a dash", got)
	}
	r := 1.0
	if got := fmtRate(&r); got != "100.00%" {
		t.Errorf("fmtRate(1.0) = %q", got)
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 12690: "12,690", 1234567: "1,234,567"} {
		if got := fmtCount(in); got != want {
			t.Errorf("fmtCount(%d) = %q, want %q", in, got, want)
		}
	}
}
