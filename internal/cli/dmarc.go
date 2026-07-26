package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// DMARC aggregate-report commands, under `domains`.
//
// Naming: `--dmarc` on create/update and the "DMARC ingestion" field are the
// INGESTION flag (a domain that swallows every local part and parses arriving
// RUA reports — the sibling of --fbl). The `dmarc=` entry in a domain's DNS row
// is a different fact: whether the domain publishes a _dmarc TXT record.
// Nothing here may say a bare "dmarc" where the two could be confused.
//
// These are the read side. `domains dmarc <domain>` is the headline: it exists
// to answer "can I move to p=reject yet?" in one screen. The other two drill
// into the evidence behind that verdict.

// dmarcWindowFlag registers --window with the DMARC vocabulary. It is
// deliberately NOT the traffic/events --range set: core calibrates readiness
// thresholds in days and rejects anything outside 7d|30d|90d, so sharing the
// flag would just produce 400s from muscle memory.
func dmarcWindowFlag(cmd *cobra.Command, window *string) {
	cmd.Flags().StringVar(window, "window", coreapi.DmarcWindowDefault,
		"reporting window: "+strings.Join(coreapi.DmarcWindows, "|"))
}

// checkDmarcWindow rejects an unknown window locally so the user gets the
// accepted set rather than a bare validation_failed from core — the values
// differ from every other time-window flag in the CLI.
func checkDmarcWindow(window string) error {
	if coreapi.ValidDmarcWindow(window) {
		return nil
	}
	return usageError(fmt.Errorf("--window %q is not one of %s (note: NOT the traffic/events ranges)",
		window, strings.Join(coreapi.DmarcWindows, "|")))
}

func newDomainDmarcCmd(a *app) *cobra.Command {
	var window string
	cmd := &cobra.Command{
		Use:   "dmarc <domain>",
		Short: "DMARC readiness for a domain: can it move to p=quarantine or p=reject?",
		Long: "Summarize the DMARC aggregate (RUA) reports other receivers sent about this domain: " +
			"authentication totals, a conservative enforcement-readiness verdict with the blockers behind it, " +
			"and the busiest sending sources.\n\n" +
			"Readiness is deliberately pessimistic — a false \"ready\" costs real mail — so thin data, a short " +
			"observed window, or any material volume of unattributable failure all block. Sources are classified " +
			"using this platform's own DKIM selectors, so mail we relayed is recognized as yours.\n\n" +
			"This reads ingested reports; it does not check the domain's _dmarc DNS record.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkDmarcWindow(window); err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			d, err := client.GetDomainDmarc(cmd.Context(), args[0], window)
			if err != nil {
				return err
			}
			a.out.Emit(d, func(w io.Writer) { printDmarcSummary(w, a.out, d) })
			return nil
		},
	}
	dmarcWindowFlag(cmd, &window)
	return cmd
}

func newDomainDmarcSourcesCmd(a *app) *cobra.Command {
	var (
		window string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "dmarc-sources <domain>",
		Short: "Every source IP seen sending as a domain, classified platform / aligned / unaligned",
		Long: "List every sending source other receivers reported for this domain over the window, busiest first, " +
			"with its DMARC pass rate and SPF/DKIM detail.\n\n" +
			"Unaligned sources are listed first and called out: they are neither ours nor passing, and they are the " +
			"reason a domain cannot reach enforcement. Each one is either a legitimate sender nobody remembered " +
			"(a CRM, a ticketing tool) or a spoofer — identify it before tightening the policy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkDmarcWindow(window); err != nil {
				return err
			}
			if limit < 0 || limit > 500 {
				return usageError(errors.New("--limit must be between 1 and 500"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			src, err := client.GetDomainDmarcSources(cmd.Context(), args[0], window, limit)
			if err != nil {
				return err
			}
			a.out.Emit(src, func(w io.Writer) {
				a.out.Msgf("%s — DMARC sources over %d days", a.out.Bold(src.Domain), src.WindowDays)
				if len(src.Sources) == 0 {
					a.out.Msgf("  no reports yet for this window — nothing to classify")
					return
				}
				// Unaligned first, then by volume: the blocking sources must be
				// impossible to miss even when a platform relay dwarfs them.
				sorted := append([]coreapi.DmarcSource(nil), src.Sources...)
				sort.SliceStable(sorted, func(i, j int) bool {
					if ri, rj := dmarcClassRank(sorted[i].Class), dmarcClassRank(sorted[j].Class); ri != rj {
						return ri < rj
					}
					return sorted[i].Messages > sorted[j].Messages
				})
				rows := make([][]string, 0, len(sorted))
				unaligned := int64(0)
				for _, s := range sorted {
					if s.Class == "unaligned" {
						unaligned += s.Messages
					}
					rows = append(rows, []string{
						s.SourceIP,
						dmarcClassLabel(a.out, s.Class),
						fmtCount(s.Messages),
						fmtShare(s.Share),
						fmtRate(s.PassRate),
						fmtCount(s.SPFPass),
						fmtCount(s.DKIMPass),
						fmtEpoch(s.LastSeen),
					})
				}
				printTable(w, a.out, []string{
					"SOURCE IP", "CLASS", "MESSAGES", "SHARE", "PASS RATE", "SPF", "DKIM", "LAST SEEN",
				}, rows)
				a.out.Msgf("  %d source(s), %s messages in window", len(sorted), fmtCount(src.Messages))
				if unaligned > 0 {
					a.out.Warnf("%s message(s) from unaligned sources — identify them before enforcing", fmtCount(unaligned))
				}
			})
			return nil
		},
	}
	dmarcWindowFlag(cmd, &window)
	cmd.Flags().IntVar(&limit, "limit", 0, "max sources to return (1–500; default 100)")
	return cmd
}

func newDomainDmarcReportsCmd(a *app) *cobra.Command {
	var (
		limit  int
		cursor string
		all    bool
	)
	cmd := &cobra.Command{
		Use:   "dmarc-reports <domain>",
		Short: "Aggregate (RUA) reports ingested for a domain, newest reporting window first",
		Long: "List the raw ingest log: one row per aggregate report, with the reporting organization, " +
			"the window it covers, its message counts, and the DMARC policy that reporter observed published.\n\n" +
			"Useful for confirming reporters are actually sending (a domain with no rows almost always has a rua= " +
			"problem, not a mail problem) and for spotting a reporter whose view of the policy disagrees with the rest.\n\n" +
			"This list is not window-scoped: it spans everything still within core's retention.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 || limit > 200 {
				return usageError(errors.New("--limit must be between 1 and 200"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			domain := args[0]
			var reports []coreapi.DmarcReport
			next := ""
			if all {
				// This response paginates on `cursor`, not the directory's
				// `nextCursor`, so it is adapted into Page[T] rather than
				// returned as one.
				reports, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.DmarcReport], error) {
					pg, e := client.ListDomainDmarcReports(ctx, domain, limit, cur)
					if e != nil {
						return coreapi.Page[coreapi.DmarcReport]{}, e
					}
					return coreapi.Page[coreapi.DmarcReport]{Items: pg.Reports, NextCursor: strOr(pg.Cursor, "")}, nil
				})
			} else {
				var pg *coreapi.DomainDmarcReports
				pg, err = client.ListDomainDmarcReports(ctx, domain, limit, cursor)
				if err == nil {
					reports, next = pg.Reports, strOr(pg.Cursor, "")
				}
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"domain": domain, "reports": reports, "cursor": next}, func(w io.Writer) {
				if len(reports) == 0 {
					a.out.Msgf("%s — no reports yet", a.out.Bold(domain))
					a.out.Msgf("  reporters send aggregate reports daily; check the domain publishes a rua= pointing at this platform")
					return
				}
				rows := make([][]string, 0, len(reports))
				for _, r := range reports {
					rows = append(rows, []string{
						truncate(r.OrgName, 24),
						truncate(r.ReportID, 28),
						fmtEpoch(r.RangeBegin) + " → " + fmtEpoch(r.RangeEnd),
						fmtCount(r.Messages),
						fmtCount(r.Passing),
						fmtReportPolicy(r),
						fmtEpoch(r.ReceivedAt),
					})
				}
				printTable(w, a.out, []string{
					"ORG", "REPORT ID", "WINDOW", "MESSAGES", "PASSING", "POLICY", "RECEIVED",
				}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	return cmd
}

// printDmarcSummary renders the readiness banner, the blockers behind it, and
// the busiest sources. Prose goes to stderr (the Printer's convention); the
// blocker and source tables are data and go to stdout.
func printDmarcSummary(w io.Writer, p *Printer, d *coreapi.DomainDmarc) {
	r := d.Readiness
	p.Msgf("%s — DMARC readiness over %d days", p.Bold(d.Domain), d.WindowDays)
	p.Msgf("  verdict:  %s", dmarcVerdictLabel(p, r.Verdict))
	p.Msgf("  policy:   %s", dmarcPolicyTransition(r))

	if d.Totals.Reports == 0 {
		// The empty case is the common one on a freshly configured domain, and
		// it is a DNS problem far more often than a mail problem. Say so and
		// stop — every figure below would be a zero or a dash.
		p.Msgf("  no reports yet — no aggregate reports have been ingested for this domain")
		for _, b := range r.Blockers {
			p.Msgf("  %s", b.Detail)
		}
		return
	}

	p.Msgf("  aligned:  %s (%s of %s messages passed)",
		fmtRate(r.AlignedRate), fmtCount(d.Totals.Passing), fmtCount(d.Totals.Messages))
	p.Msgf("  data:     %s reports · %s reporter(s) · %s source(s) · %d of %d days observed",
		fmtCount(d.Totals.Reports), fmtCount(r.Reporters), fmtCount(d.Totals.Sources), r.ObservedDays, r.WindowDays)
	p.Msgf("  seen:     %s → %s", fmtEpochPtr(d.Totals.FirstSeen), fmtEpochPtr(d.Totals.LastSeen))
	p.Msgf("  handled:  %s quarantined · %s rejected · %s SPF-aligned · %s DKIM-aligned",
		fmtCount(d.Totals.Quarantined), fmtCount(d.Totals.Rejected),
		fmtCount(d.Totals.SPFAligned), fmtCount(d.Totals.DKIMAligned))
	if r.Sampled {
		// pct<100 means reporters applied the policy to a fraction of the mail,
		// so every count above understates what enforcing would really touch —
		// the verdict is weaker than it looks.
		p.Warnf("sampled: a reporter observed pct<100, so these counts UNDERSTATE the impact of enforcing; set pct=100 for a true reading")
	}

	if len(r.Blockers) == 0 {
		p.Msgf("  blockers: none")
	} else {
		p.Msgf("  blockers: %d", len(r.Blockers))
		rows := make([][]string, 0, len(r.Blockers))
		for _, b := range r.Blockers {
			msgs := "—"
			if b.Messages > 0 {
				msgs = fmtCount(b.Messages)
			}
			rows = append(rows, []string{b.Code, b.Detail, msgs})
		}
		printTable(w, p, []string{"BLOCKER", "DETAIL", "MESSAGES"}, rows)
	}

	if len(d.TopSources) == 0 {
		return
	}
	p.Msgf("  top sources (see `openemail domains dmarc-sources %s` for all):", d.Domain)
	rows := make([][]string, 0, len(d.TopSources))
	for _, s := range d.TopSources {
		rows = append(rows, []string{
			s.SourceIP, dmarcClassLabel(p, s.Class), fmtCount(s.Messages), fmtShare(s.Share), fmtRate(s.PassRate),
		})
	}
	printTable(w, p, []string{"SOURCE", "CLASS", "MESSAGES", "SHARE", "PASS RATE"}, rows)
}

// dmarcVerdictLabel colors the verdict by what it asks the operator to do:
// green = enforce (or already enforcing), yellow = wait or investigate, plain =
// nothing has arrived to judge.
func dmarcVerdictLabel(p *Printer, verdict string) string {
	switch verdict {
	case "ready_for_quarantine", "ready_for_reject", "at_enforcement":
		return p.Green(verdict)
	case "not_ready", "insufficient_data":
		return p.Yellow(verdict)
	default: // no_data
		return verdict
	}
}

// dmarcPolicyTransition renders "observed → recommended". currentPolicy is null
// until a report names one; recommendedPolicy is null whenever nothing is
// advised, which includes both "blocked" and "already at reject".
func dmarcPolicyTransition(r coreapi.DmarcReadiness) string {
	cur := strOr(r.CurrentPolicy, "unknown")
	if r.RecommendedPolicy == nil {
		return cur + " (no change recommended yet)"
	}
	return cur + " → " + *r.RecommendedPolicy + " (recommended)"
}

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

// dmarcClassLabel colors an attribution: unaligned is the one that blocks
// enforcement, platform is mail this deployment relayed and signed.
func dmarcClassLabel(p *Printer, class string) string {
	switch class {
	case "unaligned":
		return p.Red(class)
	case "aligned_third_party":
		return p.Cyan(class)
	case "platform":
		return p.Green(class)
	}
	return class
}

// fmtRate renders a nullable 0..1 rate as a percentage (null → dash).
func fmtRate(r *float64) string {
	if r == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", *r*100)
}

// fmtShare renders a source's 0..1 share of window volume; one decimal is
// enough to rank sources and matches core's own blocker wording.
func fmtShare(s float64) string {
	return fmt.Sprintf("%.1f%%", s*100)
}

// fmtCount renders a tally with thousands separators — these are message
// counts, not sizes.
func fmtCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return neg + b.String()
}

// fmtReportPolicy compacts one reporter's observed policy: p, then sp when it
// diverges (the parent/child split DMARC allows), then pct when sampled.
func fmtReportPolicy(r coreapi.DmarcReport) string {
	parts := []string{"p=" + strOr(r.PolicyP, "?")}
	if r.PolicySp != nil && *r.PolicySp != "" && (r.PolicyP == nil || *r.PolicySp != *r.PolicyP) {
		parts = append(parts, "sp="+*r.PolicySp)
	}
	if r.PolicyPct != nil && *r.PolicyPct != 100 {
		parts = append(parts, fmt.Sprintf("pct=%d", *r.PolicyPct))
	}
	if r.Truncated {
		parts = append(parts, "truncated")
	}
	return strings.Join(parts, " ")
}
