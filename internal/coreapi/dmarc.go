package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// DMARC aggregate-report views (GET /domains/:domain/dmarc[/sources|/reports]).
//
// These describe mail OTHER receivers saw claiming a domain, reconstructed from
// the RUA reports core ingests. Nothing here is a send-path input: core treats
// the rows as display-only because nothing authenticates a report, and this
// client must not lend them any more authority than that.
//
// Note the collision with Domain.DNSStatus.DMARC, which is a different fact
// entirely — whether the domain publishes a _dmarc TXT record. Domain.DMARC (a
// sibling of Domain.FBL) is the INGESTION flag: the domain that swallows every
// local part and whose arriving mail is parsed as aggregate reports. Renderers
// must never label either one a bare "dmarc".

// DmarcRanges is the `?range=` vocabulary these endpoints accept, in cycle
// order. Deliberately NOT the traffic/events range set (1h|6h|24h|7d|30d):
// readiness thresholds are calibrated in days against a fixed enum so a caller
// cannot cherry-pick a window that manufactures a "ready" verdict. Core answers
// 400 validation_failed for anything else.
var DmarcRanges = []string{"7d", "30d", "90d"}

// DmarcRangeDefault mirrors core's own default. 7d is a poor default despite
// being the shortest: readiness needs 7 observed days, so a 7d request nearly
// always trips the short_window blocker.
const DmarcRangeDefault = "30d"

// ValidDmarcRange reports whether s is one of DmarcRanges.
func ValidDmarcRange(s string) bool {
	for _, w := range DmarcRanges {
		if w == s {
			return true
		}
	}
	return false
}

// DmarcTotals is the window's authentication tally. FirstSeen/LastSeen are the
// bounds of the reporting windows actually covered — both null when no report
// has ever landed.
type DmarcTotals struct {
	Reports     int64  `json:"reports"`
	Reporters   int64  `json:"reporters"`
	Sources     int64  `json:"sources"`
	Messages    int64  `json:"messages"`
	Passing     int64  `json:"passing"`
	Quarantined int64  `json:"quarantined"`
	Rejected    int64  `json:"rejected"`
	SPFAligned  int64  `json:"spfAligned"`
	DKIMAligned int64  `json:"dkimAligned"`
	FirstSeen   *int64 `json:"firstSeen"`
	LastSeen    *int64 `json:"lastSeen"`
}

// DmarcBlocker is one reason the domain is not safe to enforce yet. Messages is
// omitted for blockers that are not volume-shaped (short_window, sampled_policy).
type DmarcBlocker struct {
	Code     string `json:"code"` // no_reports | low_volume | short_window | sampled_policy | platform_source_failing | unaligned_source | pass_rate_below_threshold
	Detail   string `json:"detail"`
	Messages int64  `json:"messages,omitempty"`
}

// DmarcReadiness is core's conservative "is it safe to move off p=none?"
// verdict. Every ambiguity resolves toward not-ready, so a blocker list is the
// actionable part — the verdict alone says only whether to wait or investigate.
//
// Sampled is the trap: it means a reporter observed pct<100, so every count
// here is a fraction of what enforcement would really touch.
type DmarcReadiness struct {
	Verdict           string         `json:"verdict"`           // no_data | insufficient_data | not_ready | ready_for_quarantine | ready_for_reject | at_enforcement
	CurrentPolicy     *string        `json:"currentPolicy"`     // none | quarantine | reject (null until a report names one)
	RecommendedPolicy *string        `json:"recommendedPolicy"` // quarantine | reject (null when nothing is advised)
	AlignedRate       *float64       `json:"alignedRate"`       // 0..1, null with no messages
	Messages          int64          `json:"messages"`
	Reporters         int64          `json:"reporters"`
	ObservedDays      int64          `json:"observedDays"` // days reports actually cover; ≤ WindowDays
	WindowDays        int64          `json:"windowDays"`
	Sampled           bool           `json:"sampled"`
	Blockers          []DmarcBlocker `json:"blockers"`
}

// DmarcSource is one sending IP seen claiming the domain over the window.
// Class is core's attribution: platform (signed with one of our own DKIM
// selectors — mail we relayed), aligned_third_party (not ours but passing), or
// unaligned (not ours and failing — a forgotten sender or a spoofer).
type DmarcSource struct {
	SourceIP     string   `json:"sourceIp"`
	Class        string   `json:"class"`
	Messages     int64    `json:"messages"`
	Passing      int64    `json:"passing"`
	Quarantined  int64    `json:"quarantined"`
	Rejected     int64    `json:"rejected"`
	PassRate     *float64 `json:"passRate"` // 0..1, null with no messages
	Share        float64  `json:"share"`    // 0..1 of the window's total volume
	SPFPass      int64    `json:"spfPass"`
	DKIMPass     int64    `json:"dkimPass"`
	SPFDomain    *string  `json:"spfDomain"`
	DKIMDomain   *string  `json:"dkimDomain"`
	DKIMSelector *string  `json:"dkimSelector"`
	HeaderFrom   *string  `json:"headerFrom"`
	FirstSeen    int64    `json:"firstSeen"`
	LastSeen     int64    `json:"lastSeen"`
}

// DomainDmarc is GET /domains/:domain/dmarc — the one-screen summary. TopSources
// is a truncated slice (core inlines ~10); Readiness is judged against every
// source, not just these, so a blocker can name an IP absent from the list.
type DomainDmarc struct {
	Domain     string         `json:"domain"`
	WindowDays int64          `json:"windowDays"`
	Totals     DmarcTotals    `json:"totals"`
	Readiness  DmarcReadiness `json:"readiness"`
	TopSources []DmarcSource  `json:"topSources"`
}

// DomainDmarcSources is GET /domains/:domain/dmarc/sources — every source,
// busiest first, single-shot (limit-bounded, not cursor-paginated). Messages is
// the window total, so Share stays meaningful when the list is truncated.
type DomainDmarcSources struct {
	Domain     string        `json:"domain"`
	WindowDays int64         `json:"windowDays"`
	Messages   int64         `json:"messages"`
	Sources    []DmarcSource `json:"sources"`
}

// DmarcReport is one ingested aggregate report. RangeBegin/RangeEnd bound the
// reporter's window (not ours); ReceivedAt is when we ingested it.
type DmarcReport struct {
	ID           string  `json:"id"`
	Domain       string  `json:"domain"`
	OrgName      string  `json:"orgName"`
	OrgEmail     *string `json:"orgEmail"`
	ReportID     string  `json:"reportId"`
	RangeBegin   int64   `json:"rangeBegin"`
	RangeEnd     int64   `json:"rangeEnd"`
	PolicyDomain *string `json:"policyDomain"` // the domain the XML itself claimed
	PolicyP      *string `json:"policyP"`
	PolicySp     *string `json:"policySp"`
	PolicyPct    *int64  `json:"policyPct"`
	Messages     int64   `json:"messages"`
	Passing      int64   `json:"passing"`
	Truncated    bool    `json:"truncated"`
	ReceivedAt   int64   `json:"receivedAt"`
}

// DomainDmarcReports is GET /domains/:domain/dmarc/reports — the ingest log,
// newest reporting window first. NextCursor is the API-wide continuation
// token, present only while more pages exist. Page[T] does not apply here;
// --all callers adapt it (see Depaginate).
type DomainDmarcReports struct {
	Domain     string        `json:"domain"`
	Reports    []DmarcReport `json:"reports"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

// GetDomainDmarc returns the summary + readiness verdict. rng is one of
// DmarcRanges (empty → core's default).
func (c *Client) GetDomainDmarc(ctx context.Context, domain, rng string) (*DomainDmarc, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	var out DomainDmarc
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain) + "/dmarc",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDomainDmarcSources returns every classified source over the window.
// rng is one of DmarcRanges (empty → core's default); limit ≤ 500
// (0 → core's default 100).
func (c *Client) GetDomainDmarcSources(ctx context.Context, domain, rng string, limit int) (*DomainDmarcSources, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	var out DomainDmarcSources
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain) + "/dmarc/sources",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDomainDmarcReports returns one keyset page of the ingest log. limit ≤ 200
// (0 → core's default); cursor is the prior page's Cursor (empty → first page).
// This list is not window-scoped — it spans everything still within retention.
func (c *Client) ListDomainDmarcReports(ctx context.Context, domain string, limit int, cursor string) (*DomainDmarcReports, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out DomainDmarcReports
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain) + "/dmarc/reports",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
