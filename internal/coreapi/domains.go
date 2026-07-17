package coreapi

import (
	"context"
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
	AccountID  *string `json:"accountId,omitempty"`
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
