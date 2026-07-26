package tui

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Open-Email/cli/internal/coreapi"
)

// DMARC screens: one domain's aggregate-report picture, read-only like the rest
// of the console.
//
// "DMARC ingestion" (Domain.DMARC) is the flag marking a domain that swallows
// every local part and parses arriving RUA reports. The `dmarc` row under a
// domain's DNS heading is a different fact — whether it publishes a _dmarc TXT
// record. Neither is rendered as a bare "dmarc" outside its heading.

// dmarcCaption is the standing caveat on everything these screens show: the
// rows are third-party claims core stores for display only. Nothing here feeds
// a send-path decision, because nothing authenticates an aggregate report.
const dmarcCaption = "third-party aggregate reports · display only · never a send-path input"

// dmarcState carries the active window and the readiness lines for one DMARC
// screen. Like trafficState it is mutated in place by the window-cycle action
// and read by fetch/view, hence the atomics: the action and the fetch run in
// goroutines while view reads concurrently.
type dmarcState struct {
	i       atomic.Int64
	summary atomic.Pointer[[]string]
}

func newDmarcState() *dmarcState {
	st := &dmarcState{}
	for i, w := range coreapi.DmarcWindows {
		if w == coreapi.DmarcWindowDefault {
			st.i.Store(int64(i))
			break
		}
	}
	return st
}

func (d *dmarcState) window() string {
	return coreapi.DmarcWindows[int(d.i.Load())%len(coreapi.DmarcWindows)]
}

func (d *dmarcState) cycle() string { d.i.Add(1); return d.window() }

func (d *dmarcState) setSummary(lines []string) { d.summary.Store(&lines) }

func (d *dmarcState) lines() []string {
	if p := d.summary.Load(); p != nil {
		return *p
	}
	return nil
}

// dmarcBlockersShown caps the blocker lines in the summary block so the source
// table keeps most of the pane. Anything dropped is counted in a trailing line
// — a silently truncated blocker list would read as "nothing else is wrong".
const dmarcBlockersShown = 3

// dmarcDesc is one domain's DMARC screen: the readiness verdict as a summary
// block, every classified source as the table below, and the reporting window
// cycling in place on R. The verdict and the source list come from two
// endpoints issued together — the summary inlines only its top ten sources, and
// the whole point of the table is the long tail an unaligned sender hides in.
func dmarcDesc(domain string) resourceDesc {
	st := newDmarcState()
	return resourceDesc{
		key:     "dmarc:" + domain,
		name:    "DMARC — " + domain,
		caption: dmarcCaption,
		summary: func(width int) []string {
			lines := st.lines()
			out := make([]string, len(lines))
			for i, l := range lines {
				out[i] = truncate(l, width)
			}
			return out
		},
		columns: []column{
			{title: "SOURCE IP", width: 18},
			{title: "CLASS", width: 19},
			{title: "MESSAGES", width: 10},
			{title: "SHARE", width: 7},
			{title: "PASS RATE", width: 9},
			{title: "SPF", width: 8},
			{title: "DKIM", width: 8},
			{title: "LAST SEEN", flex: true},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, _ string) ([]rowData, string, error) {
			window := st.window() // snapshot: the verdict and the rows must describe one window
			var (
				wg       sync.WaitGroup
				sum      *coreapi.DomainDmarc
				sumErr   error
				srcs     *coreapi.DomainDmarcSources
				srcsErr  error
				fullList = 500
			)
			wg.Add(2)
			go func() { defer wg.Done(); sum, sumErr = c.GetDomainDmarc(ctx, domain, window) }()
			go func() {
				defer wg.Done()
				srcs, srcsErr = c.GetDomainDmarcSources(ctx, domain, window, fullList)
			}()
			wg.Wait()
			if sumErr != nil {
				return nil, "", sumErr
			}
			if srcsErr != nil {
				return nil, "", srcsErr
			}
			st.setSummary(dmarcSummaryLines(sum))

			sorted := append([]coreapi.DmarcSource(nil), srcs.Sources...)
			sort.SliceStable(sorted, func(i, j int) bool {
				if ri, rj := dmarcClassRank(sorted[i].Class), dmarcClassRank(sorted[j].Class); ri != rj {
					return ri < rj // unaligned first: they are why enforcement is blocked
				}
				return sorted[i].Messages > sorted[j].Messages
			})
			rows := make([]rowData, len(sorted))
			for i, s := range sorted {
				rows[i] = rowData{
					cells: []string{
						s.SourceIP, dmarcClassCell(s.Class), fmtInt(s.Messages), fmtShare(s.Share),
						fmtRate(s.PassRate), fmtInt(s.SPFPass), fmtInt(s.DKIMPass), fmtEpoch(s.LastSeen),
					},
					item: s,
				}
			}
			return rows, "", nil // limit-bounded, not cursor-paginated
		},
		detail: func(item any) []kv {
			s := item.(coreapi.DmarcSource)
			return []kv{
				{k: "source ip", v: s.SourceIP},
				{k: "class", v: dmarcClassCell(s.Class)},
				{k: "meaning", v: dmarcClassExplainer(s.Class)},
				{},
				{k: "messages", v: fmtInt(s.Messages)},
				{k: "passing", v: fmtInt(s.Passing)},
				{k: "pass rate", v: fmtRate(s.PassRate)},
				{k: "share", v: fmtShare(s.Share)},
				{k: "quarantined", v: fmtInt(s.Quarantined)},
				{k: "rejected", v: fmtInt(s.Rejected)},
				{},
				{v: "Authentication"},
				{k: "spf pass", v: fmtInt(s.SPFPass)},
				{k: "spf domain", v: strOr(s.SPFDomain, "—")},
				{k: "dkim pass", v: fmtInt(s.DKIMPass)},
				{k: "dkim domain", v: strOr(s.DKIMDomain, "—")},
				{k: "dkim selector", v: strOr(s.DKIMSelector, "—")},
				{k: "header from", v: strOr(s.HeaderFrom, "—")},
				{},
				{k: "first seen", v: fmtEpoch(s.FirstSeen)},
				{k: "last seen", v: fmtEpoch(s.LastSeen)},
				{},
				{v: dmarcCaption},
			}
		},
		actions: []action{
			{key: "R", label: "R window", do: func(ctx context.Context, c *coreapi.Client, _ any) (string, error) {
				return "window: " + st.cycle(), nil
			}},
			{key: "i", label: "i reports", run: func(ctx context.Context, ui *Options, _ any) pane {
				return newScreenPane(ctx, ui, dmarcReportsDesc(domain))
			}},
		},
	}
}

// dmarcSummaryLines renders the readiness verdict as the screen's header block.
// A domain with no reports gets the "why" instead of a wall of zeroes — that
// state is almost always a rua= DNS problem, not a mail problem.
func dmarcSummaryLines(d *coreapi.DomainDmarc) []string {
	r := d.Readiness
	verdict := fmt.Sprintf("%s  %s", stDim.Render("verdict"), dmarcVerdictCell(r.Verdict))

	if d.Totals.Reports == 0 {
		lines := []string{verdict, fmt.Sprintf("%s  no aggregate reports ingested over %d days", stDim.Render("data   "), d.WindowDays)}
		for _, b := range r.Blockers {
			lines = append(lines, fmt.Sprintf("%s  %s", stDim.Render("why    "), b.Detail))
		}
		return lines
	}

	lines := []string{
		verdict + stDim.Render("  ·  ") + dmarcPolicyCell(r) + stDim.Render("  ·  aligned ") + fmtRate(r.AlignedRate),
		fmt.Sprintf("%s  %s msgs · %s reports · %s reporters · %s sources · %d/%d days observed",
			stDim.Render("data   "), fmtInt(d.Totals.Messages), fmtInt(d.Totals.Reports),
			fmtInt(r.Reporters), fmtInt(d.Totals.Sources), r.ObservedDays, r.WindowDays),
	}
	if r.Sampled {
		// pct<100: reporters applied the policy to a sample, so every count
		// above is a fraction of what enforcing would really touch.
		lines = append(lines, fmt.Sprintf("%s  %s", stDim.Render("sampled"),
			stWarn.Render("a reporter observed pct<100 — these counts UNDERSTATE the impact of enforcing")))
	}
	switch {
	case len(r.Blockers) == 0:
		lines = append(lines, fmt.Sprintf("%s  %s", stDim.Render("blocked"), stLive.Render("none — nothing is blocking enforcement")))
	default:
		shown := r.Blockers
		if len(shown) > dmarcBlockersShown {
			shown = shown[:dmarcBlockersShown]
		}
		for _, b := range shown {
			lines = append(lines, fmt.Sprintf("%s  %s", stDim.Render("blocked"), b.Detail))
		}
		if n := len(r.Blockers) - len(shown); n > 0 {
			lines = append(lines, fmt.Sprintf("%s  %s", stDim.Render("       "),
				stDim.Render(fmt.Sprintf("+%d more blocker(s) — run `openemail domains dmarc %s`", n, d.Domain))))
		}
	}
	return lines
}

// dmarcReportsDesc is the raw ingest log for a domain — one row per aggregate
// report, newest reporting window first, with REAL server-side keyset
// pagination. It is not window-scoped: it spans everything within retention.
func dmarcReportsDesc(domain string) resourceDesc {
	return resourceDesc{
		key:     "dmarc-reports:" + domain,
		name:    "DMARC reports — " + domain,
		caption: dmarcCaption,
		columns: []column{
			{title: "RECEIVED", width: 16},
			{title: "ORG", flex: true},
			{title: "WINDOW", width: 29},
			{title: "MESSAGES", width: 10},
			{title: "PASSING", width: 10},
			{title: "POLICY", width: 22},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListDomainDmarcReports(ctx, domain, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Reports))
			for i, r := range pg.Reports {
				rows[i] = rowData{
					cells: []string{
						fmtEpoch(r.ReceivedAt), r.OrgName, fmtDmarcWindow(r.RangeBegin, r.RangeEnd),
						fmtInt(r.Messages), fmtInt(r.Passing), dmarcReportPolicy(r),
					},
					item: r,
				}
			}
			return rows, strOr(pg.Cursor, ""), nil
		},
		detail: func(item any) []kv {
			r := item.(coreapi.DmarcReport)
			return []kv{
				{k: "org", v: r.OrgName},
				{k: "org email", v: strOr(r.OrgEmail, "—")},
				{k: "report id", v: r.ReportID},
				{k: "received", v: fmtEpoch(r.ReceivedAt)},
				{},
				{v: "Reporting window"},
				{k: "begin", v: fmtEpoch(r.RangeBegin)},
				{k: "end", v: fmtEpoch(r.RangeEnd)},
				{k: "messages", v: fmtInt(r.Messages)},
				{k: "passing", v: fmtInt(r.Passing)},
				{k: "truncated", v: yn(r.Truncated)},
				{},
				{v: "Policy this reporter observed"},
				{k: "policy domain", v: strOr(r.PolicyDomain, "—")},
				{k: "p", v: strOr(r.PolicyP, "—")},
				{k: "sp", v: strOr(r.PolicySp, "—")},
				{k: "pct", v: int64Or(r.PolicyPct, "—")},
				{},
				{v: dmarcCaption},
			}
		},
	}
}

// fmtDmarcWindow renders a report's reporting window in one cell. Reporters
// send daily, so the end almost always repeats the begin's year — dropping it
// buys the ~11 columns that keep the range from being truncated to "…".
func fmtDmarcWindow(begin, end int64) string {
	b, e := fmtEpoch(begin), fmtEpoch(end)
	if len(b) >= 4 && len(e) >= 4 && b[:4] == e[:4] {
		e = e[5:] // strip "YYYY-"
	}
	return b + "→" + e
}

// dmarcReportPolicy compacts one reporter's observed policy: p, then sp when it
// diverges (the parent/child split DMARC allows), then pct when sampled.
func dmarcReportPolicy(r coreapi.DmarcReport) string {
	out := "p=" + strOr(r.PolicyP, "?")
	if r.PolicySp != nil && *r.PolicySp != "" && (r.PolicyP == nil || *r.PolicySp != *r.PolicyP) {
		out += " sp=" + *r.PolicySp
	}
	if r.PolicyPct != nil && *r.PolicyPct != 100 {
		out += fmt.Sprintf(" pct=%d", *r.PolicyPct)
	}
	return out
}

// dmarcVerdictCell colors the verdict by what it asks of the operator: green =
// enforce (or already enforcing), amber = wait or investigate, plain = nothing
// has arrived to judge.
func dmarcVerdictCell(verdict string) string {
	switch verdict {
	case "ready_for_quarantine", "ready_for_reject", "at_enforcement":
		return stLive.Render(verdict)
	case "not_ready", "insufficient_data":
		return stWarn.Render(verdict)
	default: // no_data
		return verdict
	}
}

// dmarcPolicyCell renders "observed → recommended". currentPolicy is null until
// a report names one; recommendedPolicy is null whenever nothing is advised —
// which covers both "blocked" and "already at reject".
func dmarcPolicyCell(r coreapi.DmarcReadiness) string {
	cur := strOr(r.CurrentPolicy, "unknown")
	if r.RecommendedPolicy == nil {
		return "p=" + cur
	}
	return "p=" + cur + " → " + stLive.Render(*r.RecommendedPolicy)
}

// dmarcClassRank orders sources so the ones blocking enforcement come first,
// even when a platform relay dwarfs them in volume.
func dmarcClassRank(class string) int {
	switch class {
	case "unaligned":
		return 0
	case "aligned_third_party":
		return 1
	default: // platform
		return 2
	}
}

func dmarcClassCell(class string) string {
	switch class {
	case "unaligned":
		return stErr.Render(class)
	case "platform":
		return stLive.Render(class)
	}
	return class
}

func dmarcClassExplainer(class string) string {
	switch class {
	case "platform":
		return "signed with one of this platform's DKIM selectors — mail we relayed"
	case "aligned_third_party":
		return "not ours, but passing DMARC — a correctly configured third party"
	case "unaligned":
		return "not ours and failing — a forgotten sender or a spoofer; identify before enforcing"
	}
	return ""
}

// fmtRate renders a nullable 0..1 rate as a percentage (null → dash).
func fmtRate(r *float64) string {
	if r == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", *r*100)
}

// fmtShare renders a source's 0..1 share of window volume.
func fmtShare(s float64) string { return fmt.Sprintf("%.1f%%", s*100) }
