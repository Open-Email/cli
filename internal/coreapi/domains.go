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
}

// Domain mirrors the endpoint's present() output: booleans are real bools (core
// converts the stored 0/1), dnsStatus is an object or null.
type Domain struct {
	Domain       string     `json:"domain"`
	Enabled      bool       `json:"enabled"`
	CanSend      bool       `json:"canSend"`
	CanReceive   bool       `json:"canReceive"`
	AliasOf      *string    `json:"aliasOf"`
	FBL          bool       `json:"fbl"`
	DMARC        bool       `json:"dmarc"`
	DNSStatus    *DNSStatus `json:"dnsStatus"`
	DNSCheckedAt *int64     `json:"dnsCheckedAt"`
	AccountID    *string    `json:"accountId"`
	CreatedAt    int64      `json:"createdAt"`
}

// DomainCreateInput is the POST /domains body; pointers so unset fields are
// omitted and core applies its defaults (enabled/canReceive true, canSend/fbl
// false).
type DomainCreateInput struct {
	Domain     string  `json:"domain"`
	Enabled    *bool   `json:"enabled,omitempty"`
	CanSend    *bool   `json:"canSend,omitempty"`
	CanReceive *bool   `json:"canReceive,omitempty"`
	AliasOf    *string `json:"aliasOf,omitempty"`
	FBL        *bool   `json:"fbl,omitempty"`
	DMARC      *bool   `json:"dmarc,omitempty"`
	AccountID  *string `json:"accountId,omitempty"`
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
	ByOutcome     map[string]int64 `json:"byOutcome"`
	Rows          []TrafficRow     `json:"rows"`
	Estimated     bool             `json:"estimated"`
	RetentionDays int              `json:"retentionDays"`
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
	SizeBytes       *int64  `json:"sizeBytes"`
	BlobHash        *string `json:"blobHash"`
	Attempt         *int32  `json:"attempt"`
	Response        *string `json:"response"`
}

// DomainEvents is GET /domains/:domain/events — a keyset-paginated page of the
// per-event traffic log. Cursor rides the response's `cursor` field (string or
// null; null on the last page). Estimated is always false (this is the
// unsampled durable record, unlike DomainTraffic).
type DomainEvents struct {
	Domain    string         `json:"domain"`
	Range     string         `json:"range"`
	Events    []TrafficEvent `json:"events"`
	Cursor    *string        `json:"cursor"`
	Estimated bool           `json:"estimated"`
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

func (c *Client) CreateDomain(ctx context.Context, in DomainCreateInput) (*Domain, error) {
	var out Domain
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/domains", body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
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
// from a prior page's Cursor (empty → first page). Follow *DomainEvents.Cursor
// (null on the last page) to paginate; --all callers thread it via Depaginate.
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
