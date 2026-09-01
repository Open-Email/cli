package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// DNSStatus is a domain's DNS check state (present() renders it as an object or
// null). All fields optional.
type DNSStatus struct {
	MX    *bool `json:"mx,omitempty"`
	SPF   *bool `json:"spf,omitempty"`
	DKIM  *bool `json:"dkim,omitempty"`
	DMARC *bool `json:"dmarc,omitempty"`
	// Present only for a domain with JMAP autodiscovery enabled — the SRV
	// record is not offered otherwise, so there is no verdict to report.
	JMAP *bool `json:"jmap,omitempty"`
	// Same rule for the RFC 6764 CalDAV/CardDAV records (the dav flag).
	DAV *bool `json:"dav,omitempty"`
}

// Domain mirrors the endpoint's present() output: booleans are real bools (core
// converts the stored 0/1), dnsStatus is an object or null.
type Domain struct {
	Domain string `json:"domain"`
	// The four send-state fields each have ONE author (core's
	// docs/send-state-design.md): Enabled and SendOnly are the owner's,
	// SendVerified is the DNS check's, SendHold is the operator's.
	Enabled bool `json:"enabled"`
	// SendOnly is the owner's MODE: the tenant sends as the domain while its
	// inbound MX stays at another provider, so nothing on it is a local
	// recipient — the platform relays mail for it there and asks for no MX
	// record. Not an enforcement state.
	SendOnly bool `json:"sendOnly"`
	// SendVerified is the EARNED half of sendability: true once the bounce CNAME
	// and both DKIM CNAMEs were proven live by `POST /domains`. false means
	// "publish DNS", never "stopped". Only a system key may assert or revoke it.
	SendVerified bool `json:"sendVerified"`
	// SendHold is this domain's OWN operator enforcement: nil = none,
	// "paused" = reversible hold (429 sending_paused — 451 at smtp-in, the
	// sending MTA queues, the relay backlog is DEFERRED), "disabled" =
	// permanent stop (403 sending_disabled — 550, the backlog bounced). One
	// field rather than two booleans, so there is no precedence to know.
	// System-writable in both directions. Egress only. The account's hold is
	// reported on the Account, not folded in here.
	SendHold *string `json:"sendHold"`
	AliasOf  *string `json:"aliasOf"`
	FBL      bool    `json:"fbl"`
	DMARC    bool    `json:"dmarc"`
	JMAP     bool    `json:"jmap"`
	// DAV: RFC 6764 CalDAV/CardDAV autodiscovery opt-in (core migration 0018) —
	// the DNS checklist gains _caldavs._tcp/_carddavs._tcp SRV + TXT records
	// pointing at the DAV gateway.
	DAV bool `json:"dav"`
	// ITIP: inbound iTIP auto-apply (core migration 0015) — arriving
	// text/calendar invitations are filed into recipients' calendars.
	ITIP bool `json:"itip"`
	// SRS: this domain's SRS0=/SRS1= local-part NAMESPACE is the platform's
	// forwarding bounce-return path (core migration 0043). System-managed and
	// shape-gated — every local part outside the namespace routes normally, so
	// this is not an fbl-style whole-domain swallow. Decoded rather than
	// ignored because a client that drops it renders an incomplete picture of
	// what a domain is doing with mail addressed to it.
	SRS          bool       `json:"srs"`
	DNSStatus    *DNSStatus `json:"dnsStatus"`
	DNSCheckedAt *int64     `json:"dnsCheckedAt"`
	AccountID    *string    `json:"accountId"`
	CreatedAt    int64      `json:"createdAt"`

	// The derived answers, so a client rendering an onboarding checklist does
	// not have to know which combination of the four fields means what. There
	// is deliberately no "verified" here: proving control of the domain is the
	// precondition for core creating the row at all, so every domain that
	// exists is verified (SendVerified is a different fact — the SEND records).
	// Receiving is enabled && !sendOnly.
	Receiving bool `json:"receiving"`
	// Sending is enabled && sendVerified && sendHold == nil — this domain's own
	// switches; the account's hold is the Account's to report.
	Sending bool `json:"sending"`

	// MailboxCount is how many mailboxes have their primary address on this
	// domain, and is present ONLY when the listing asked for it
	// (GET /domains?mailboxCounts=true). A pointer, not a bare int64, because
	// the two states are different claims: nil is "we did not ask", 0 is "core
	// counted none". It exists so a client rendering the number does it with
	// ONE call for the page rather than one count call per domain — the shape
	// that made the console's domains table fail intermittently once a tenant
	// had enough domains to outrun its own deadline.
	MailboxCount *int64 `json:"mailboxCount,omitempty"`

	// Sampled storage on this domain, present ONLY when the listing asked for it
	// (GET /domains?usage=true) — from core's daily usage ledger, not a live
	// count. All four are pointers because "not asked", "asked but never
	// measured" and "measured, zero" are three different claims, and a bare
	// int64 collapses them into the one reading that understates a bill.
	//
	// UsageStale means the last sweep could not read the store and CARRIED the
	// previous level forward rather than recording a zero.
	UsedBytes  *int64 `json:"usedBytes,omitempty"`
	PimBytes   *int64 `json:"pimBytes,omitempty"`
	UsageAt    *int64 `json:"usageAt,omitempty"`
	UsageStale *bool  `json:"usageStale,omitempty"`
}

// DNSRecord is one record a domain needs, with the copy-pasteable value. Kind
// is ownership | mx | spf | dkim | dmarc | jmap; the ownership record appears
// only in a domain_not_verified refusal, never in a health check (it is a
// precondition to onboarding, not ongoing health).
type DNSRecord struct {
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Priority *int32 `json:"priority,omitempty"`
	Weight   *int32 `json:"weight,omitempty"`
	Port     *int32 `json:"port,omitempty"`
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
	// Accept lists other values core also counts as live for this record — it
	// appears where either of two of OUR hosts is correct (a domain that claimed
	// a vanity DAV hostname keeps the platform SRV target acceptable while it
	// migrates). Core scores liveness against value ∪ accept, so a client that
	// prints only Value shows a target the customer has NOT published beside a
	// LIVE verdict, which reads as the check lying.
	Accept []string `json:"accept,omitempty"`
}

// DNSRecordCheck is a DNSRecord plus what the resolver actually returned. OK is
// nil when liveness is UNKNOWN (the resolver could not be reached) — distinct
// from false, which means the record is genuinely absent.
type DNSRecordCheck struct {
	DNSRecord
	OK    *bool    `json:"ok,omitempty"`
	Found []string `json:"found"`
	// DMARC only: whether the published rua reports to this platform, and the
	// p= value. A record can be published (ok) and still collect nothing.
	ReportingToUs *bool   `json:"reportingToUs,omitempty"`
	Policy        *string `json:"policy,omitempty"`
}

// DomainOnboarding is POST /domains — the domain plus the records it still
// needs. Onboarding is a loop ("publish a record, POST again"), so the response
// that advances the domain also carries what is left to publish.
//
// Embedded rather than duplicated: encoding/json flattens it, matching core's
// flat body. Note the selector collision this creates (the embedded field is
// named Domain and so is its `domain` string) — CreateDomain hands the two
// parts back separately so no caller has to write d.Domain.Domain.
type DomainOnboarding struct {
	Domain
	Records []DNSRecordCheck `json:"records"`
}

// DomainDNSCheck is POST /domains/:domain/dns/check. ResolverUnavailable means
// DNS could not be queried at all: the records are still returned (with OK nil)
// and nothing was stored, so an outage never reads as misconfiguration.
type DomainDNSCheck struct {
	Domain              string           `json:"domain"`
	CheckedAt           *int64           `json:"checkedAt"`
	Cached              bool             `json:"cached"`
	ResolverUnavailable bool             `json:"resolverUnavailable,omitempty"`
	Status              DNSStatus        `json:"status"`
	Records             []DNSRecordCheck `json:"records"`
}

// DomainCreateInput is the POST /domains body; pointers so unset fields are
// omitted and core applies its defaults (enabled true, sendOnly/sendVerified/fbl
// false).
type DomainCreateInput struct {
	Domain  string `json:"domain"`
	Enabled *bool  `json:"enabled,omitempty"`
	// SendVerified is honoured for a SYSTEM key only (a platform domain that
	// never onboards); an account key's value is ignored, since sending is
	// earned by this very call's DNS check.
	SendVerified *bool   `json:"sendVerified,omitempty"`
	SendOnly     *bool   `json:"sendOnly,omitempty"`
	AliasOf      *string `json:"aliasOf,omitempty"`
	FBL          *bool   `json:"fbl,omitempty"`
	DMARC        *bool   `json:"dmarc,omitempty"`
	// The three per-domain capability opt-ins. JMAP and DAV add their RFC 6764 /
	// RFC 8620 SRV records to the DNS checklist; ITIP turns on inbound
	// auto-filing of calendar invitations, which is a WRITE path arriving mail
	// can reach, hence off by default.
	JMAP      *bool   `json:"jmap,omitempty"`
	DAV       *bool   `json:"dav,omitempty"`
	ITIP      *bool   `json:"itip,omitempty"`
	AccountID *string `json:"accountId,omitempty"`
	// Platform marks a platform-owned domain (no tenant): marshals as an
	// explicit `"accountId": null`, which core's system-caller contract
	// requires — an omitted accountId answers 400 account_required so an
	// operator-only domain can never be minted by accident. Mutually
	// exclusive with AccountID.
	Platform bool `json:"-"`
}

// MarshalJSON emits the explicit `"accountId": null` a Platform create needs
// (a nil *string with omitempty can only omit, never null).
func (in DomainCreateInput) MarshalJSON() ([]byte, error) {
	type plain DomainCreateInput
	b, err := json.Marshal(plain(in))
	if err != nil || !in.Platform {
		return b, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["accountId"] = nil
	return json.Marshal(m)
}

// TrafficRow is one (outcome, routeKind) aggregate.
type TrafficRow struct {
	Outcome   string `json:"outcome"`
	RouteKind string `json:"routeKind"`
	Events    int64  `json:"events"`
	Bytes     int64  `json:"bytes"`
}

// DomainTraffic is GET /domains/:domain/traffic — sampled estimates.
type DomainTraffic struct {
	Domain string `json:"domain"`
	Range  string `json:"range"`
	Totals struct {
		Events int64 `json:"events"`
		Bytes  int64 `json:"bytes"`
	} `json:"totals"`
	ByOutcome map[string]int64 `json:"byOutcome"`
	Rows      []TrafficRow     `json:"rows"`
	// Series is the same events bucketed over time (bucket width follows the
	// range: 5m for 1h … 1d for 30d). Optional on the wire — a summary built
	// before the field existed, or an empty result, carries none.
	Series        []TrafficSeriesPoint `json:"series,omitempty"`
	Estimated     bool                 `json:"estimated"`
	RetentionDays int                  `json:"retentionDays"`
}

// TrafficSeriesPoint is one time bucket of the traffic summary: totals plus the
// inbound/outbound split and the per-outcome breakdown inside that bucket.
type TrafficSeriesPoint struct {
	Bucket    string           `json:"bucket"`
	Events    int64            `json:"events"`
	Bytes     int64            `json:"bytes"`
	Inbound   int64            `json:"inbound"`
	Outbound  int64            `json:"outbound"`
	ByOutcome map[string]int64 `json:"byOutcome,omitempty"`
}

// TrafficEvent is one row of the per-attempt traffic log (GET
// /domains/:domain/events) — the authoritative Iceberg record, unlike the
// sampled TrafficRow aggregate. Nullable columns are pointers so a JSON null
// round-trips (the drift contract test enforces this). EventTime is epoch
// seconds; Attempt is null on endpoint (non-queue) rows.
type TrafficEvent struct {
	EventTime       int64   `json:"eventTime"`
	Source          string  `json:"source"`
	Outcome         string  `json:"outcome"`
	Detail          *string `json:"detail"`
	DeliveryID      *string `json:"deliveryId"`
	EnvelopeFrom    *string `json:"envelopeFrom"`
	EnvelopeTo      *string `json:"envelopeTo"`
	OriginalAddress *string `json:"originalAddress"`
	MatchedBy       *string `json:"matchedBy"`
	RouteKind       *string `json:"routeKind"`
	RouteTarget     *string `json:"routeTarget"`
	MailboxID       *string `json:"mailboxId"`
	MessageID       *string `json:"messageId"`
	MessageIDHeader *string `json:"messageIdHeader"`
	Subject         *string `json:"subject"`
	Size            *int64  `json:"size"`
	BlobHash        *string `json:"blobHash"`
	Attempt         *int32  `json:"attempt"`
	Response        *string `json:"response"`
}

// DomainEvents is GET /domains/:domain/events — a keyset-paginated page of the
// per-event traffic log. NextCursor is the API-wide continuation token,
// present only while more pages exist. Estimated is always false (this is the
// unsampled durable record, unlike DomainTraffic).
type DomainEvents struct {
	Domain     string         `json:"domain"`
	Range      string         `json:"range"`
	Events     []TrafficEvent `json:"events"`
	NextCursor *string        `json:"nextCursor,omitempty"`
	Estimated  bool           `json:"estimated"`
}

func (c *Client) ListDomains(ctx context.Context, limit int, cursor string) (Page[Domain], error) {
	var out struct {
		Domains    []Domain `json:"domains"`
		NextCursor string   `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains", query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[Domain]{}, err
	}
	return Page[Domain]{Items: out.Domains, NextCursor: out.NextCursor}, nil
}

// CreateDomain is core's create-or-advance call: it creates the domain when the
// account's verification TXT is live, and on a domain that already exists it
// re-resolves DNS and activates sending once the send records are published.
// Calling it repeatedly is the onboarding loop, not an error.
//
// A 400 domain_not_verified carries the record the customer must publish —
// pull it out with VerificationRecord rather than re-deriving the name or the
// token, which are core's to define.
func (c *Client) CreateDomain(ctx context.Context, in DomainCreateInput) (*Domain, []DNSRecordCheck, error) {
	var out DomainOnboarding
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/domains", body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out.Domain, out.Records, nil
}

// CheckDomainDNS resolves the records a domain needs and stores the verdict.
// force bypasses core's brief verdict cache. Unlike CreateDomain this only
// REPORTS — it never creates a domain and never activates sending.
func (c *Client) CheckDomainDNS(ctx context.Context, domain string, force bool) (*DomainDNSCheck, error) {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	var out DomainDNSCheck
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/domains/" + url.PathEscape(domain) + "/dns/check",
		query:  q,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// VerificationRecord returns the ownership record from a 400
// domain_not_verified refusal, so a client can tell the customer exactly what
// to publish without deriving the name or the account token itself.
func VerificationRecord(err error) (DNSRecord, bool) {
	ae, ok := asAPIError(err)
	if !ok || ae.Code != "domain_not_verified" {
		return DNSRecord{}, false
	}
	var body struct {
		Record *DNSRecord `json:"record"`
	}
	if json.Unmarshal(ae.Body, &body) != nil || body.Record == nil {
		return DNSRecord{}, false
	}
	return *body.Record, true
}

func (c *Client) GetDomain(ctx context.Context, domain string) (*Domain, error) {
	var out Domain
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateDomain applies a partial patch (map so aliasOf/dnsStatus null clears vs
// omit leaves unchanged).
func (c *Client) UpdateDomain(ctx context.Context, domain string, patch map[string]any) (*Domain, error) {
	var out Domain
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: "/domains/" + escapeSegment(domain),
		body: mustJSON(patch), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteDomain(ctx context.Context, domain string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/domains/" + escapeSegment(domain),
	}, nil)
}

// GetDomainTraffic returns per-domain aggregates. range is one of
// 1h|6h|24h|7d|30d (empty → core default 24h).
func (c *Client) GetDomainTraffic(ctx context.Context, domain, rng string) (*DomainTraffic, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	var out DomainTraffic
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain) + "/traffic",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDomainEvents returns one keyset page of the per-event traffic log. rng is
// one of 1h|6h|24h|7d|30d (empty → core default 24h); outcome/source are
// optional closed-enum filters (empty → omitted); cursor is the opaque token
// from a prior page's NextCursor (empty → first page). Follow
// *DomainEvents.NextCursor (absent on the last page) to paginate; --all
// callers thread it via Depaginate.
func (c *Client) GetDomainEvents(ctx context.Context, domain, rng, outcome, source string, limit int, cursor string) (*DomainEvents, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	if outcome != "" {
		q.Set("outcome", outcome)
	}
	if source != "" {
		q.Set("source", source)
	}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out DomainEvents
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/domains/" + escapeSegment(domain) + "/events",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// --- vanity hostnames (migration 0034) --------------------------------------

// HostnameCheckStatus is the per-aspect detail behind a vanity hostname's last
// check. Everything here is DISPLAY state: a certificate that has not appeared
// yet, or a Cloudflare state machine mid-flight, is normal seconds after a
// claim. Only the row's VerifiedAt says whether the hostname is servable.
type HostnameCheckStatus struct {
	// CNAME services only; absent for the NS-delegated mx slot.
	CNAME string   `json:"cname,omitempty"`
	Found []string `json:"found"`
	// mx only: the NS set-equality verdict. What it PROVES depends on
	// DelegationSource — read the two together, never NS alone.
	NS string `json:"ns,omitempty"`
	// mx only: which side of the zone cut the compared NS set came from.
	// "parent" is the delegation the customer published, read from their own
	// nameservers, so an NS verdict of ok means the set matches ours exactly and
	// a foreign nameserver beside ours reads as mismatch. "responder" means the
	// parent zone could not be read (a Cloudflare-hosted zone is the common
	// case) and our own responder answered instead: that proves the delegation
	// REACHES us, not that the set is complete or exclusive. Decoded because a
	// client that prints "NS records match" over a responder-sourced verdict is
	// claiming a check that was never made.
	DelegationSource string `json:"delegationSource,omitempty"`
	// mx only, display: whether an address query through the delegation
	// answers — proof the platform responder serves the name. Never gates
	// verification.
	Resolution string `json:"resolution,omitempty"`
	// mail/smtp only. CAA "blocked" is the one value that refuses verification —
	// an unknown answer never does.
	CAA         string `json:"caa,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	// webmail/dav only: Cloudflare's two independent state machines.
	CF *struct {
		Status     *string `json:"status"`
		SSLStatus  *string `json:"sslStatus"`
		Ready      bool    `json:"ready"`
		Configured bool    `json:"configured"`
		// False with Configured true: Cloudflare refused core, so no custom
		// hostname exists and none is being issued. Its own field because the
		// refusal otherwise reports the same empty statuses as a hostname still
		// being validated, and a broken deployment then reads as progress.
		Provisioned bool `json:"provisioned"`
	} `json:"cf,omitempty"`
}

// DomainHostname is one service's vanity-hostname slot. Reported for EVERY
// service, claimed or not, so a client renders the whole set (and what to
// publish) without hard-coding the service list.
type DomainHostname struct {
	Service  string  `json:"service"`
	Domain   string  `json:"domain"`
	Hostname *string `json:"hostname"`
	Target   *string `json:"target"`
	// NsTargets is the mx slot's counterpart of Target: the nameserver pair to
	// publish as NS records at the claimed name (an MX target cannot be a
	// CNAME, RFC 2181, so that slot is delegated instead).
	NsTargets []string `json:"nsTargets,omitempty"`
	// DirectOnly says the customer's CNAME must be a PLAIN record nothing
	// intercepts. True for mail/smtp: our own fleets over ports no HTTP proxy
	// carries, so a resolver must see the target — a customer on Cloudflare must
	// set the record to DNS only, or Cloudflare answers 1014. False for
	// webmail/dav: a plain CNAME at any provider works; the proxying that
	// matters is on OUR side of the target.
	DirectOnly bool                 `json:"directOnly"`
	VerifiedAt *int64               `json:"verifiedAt"`
	CheckedAt  *int64               `json:"checkedAt"`
	Status     *HostnameCheckStatus `json:"status"`
	CreatedAt  *int64               `json:"createdAt"`
}

type DomainHostnameList struct {
	Domain string `json:"domain"`
	// CanClaim reports whether this account may claim a hostname at all
	// (`accounts.vanity_hosts`). Advisory — a claim re-checks it server-side —
	// but it is what lets a client say "not enabled" instead of walking someone
	// into a 403, or worse into publishing a CNAME that will never be served.
	CanClaim  bool             `json:"canClaim"`
	Hostnames []DomainHostname `json:"hostnames"`
}

func (c *Client) ListDomainHostnames(ctx context.Context, domain string) (*DomainHostnameList, error) {
	var out DomainHostnameList
	err := c.doJSON(ctx, request{
		method: http.MethodGet,
		path:   "/domains/" + escapeSegment(domain) + "/hostnames",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SetDomainHostname(ctx context.Context, domain, service, hostname string) (*DomainHostnameList, error) {
	var out DomainHostnameList
	err := c.doJSON(ctx, request{
		method:      http.MethodPut,
		path:        "/domains/" + escapeSegment(domain) + "/hostnames/" + escapeSegment(service),
		body:        mustJSON(map[string]string{"hostname": hostname}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteDomainHostname(ctx context.Context, domain, service string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/domains/" + escapeSegment(domain) + "/hostnames/" + escapeSegment(service),
	}, nil)
}

// CheckDomainHostnames re-resolves every claimed hostname and stores the
// verdict. It is the ONLY writer of VerifiedAt, in both directions — a customer
// who publishes the CNAME becomes servable, and one who removes it stops being
// renewed.
func (c *Client) CheckDomainHostnames(ctx context.Context, domain string, force bool) (*DomainHostnameList, error) {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	var out DomainHostnameList
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/domains/" + escapeSegment(domain) + "/hostnames/check",
		query:  q,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
