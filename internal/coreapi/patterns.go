package coreapi

import (
	"context"
	"net/http"
	"strconv"
)

// Pattern is a pattern route (local-part glob). paced is a real JSON boolean
// (core normalizes it at the endpoint boundary); id is a numeric row id.
type Pattern struct {
	ID              int64   `json:"id"`
	Domain          string  `json:"domain"`
	Pattern         string  `json:"pattern"`
	Priority        int     `json:"priority"`
	DestinationType string  `json:"destinationType"`
	MailboxID       *string `json:"mailboxId"`
	WebhookURL      *string `json:"webhookUrl"`
	RemoteAddress   *string `json:"remoteAddress"`
	AliasAddress    *string `json:"aliasAddress"`
	Paced           bool    `json:"paced"`
	CreatedAt       int64   `json:"createdAt"`
}

// PatternCreateInput is the POST /patterns body.
type PatternCreateInput struct {
	Domain          string `json:"domain"`
	Pattern         string `json:"pattern"`
	Priority        *int   `json:"priority,omitempty"`
	DestinationType string `json:"destinationType"`
	MailboxID       string `json:"mailboxId,omitempty"`
	WebhookURL      string `json:"webhookUrl,omitempty"`
	WebhookSecret   string `json:"webhookSecret,omitempty"`
	RemoteAddress   string `json:"remoteAddress,omitempty"`
	AliasAddress    string `json:"aliasAddress,omitempty"`
	Paced           bool   `json:"paced"`
}

// ListPatterns returns one page (numeric cursor; core answers 400 invalid_cursor
// on garbage).
func (c *Client) ListPatterns(ctx context.Context, domain string, limit int, cursor string) (Page[Pattern], error) {
	q := pageValues(limit, cursor)
	if domain != "" {
		q.Set("domain", domain)
	}
	var out struct {
		Patterns   []Pattern `json:"patterns"`
		NextCursor string    `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/patterns", query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[Pattern]{}, err
	}
	return Page[Pattern]{Items: out.Patterns, NextCursor: out.NextCursor}, nil
}

func (c *Client) CreatePattern(ctx context.Context, in PatternCreateInput) (*Pattern, error) {
	var out Pattern
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/patterns", body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePattern applies a partial patch (priority/paced partial; destinationType
// with its fields atomically replaces the destination).
func (c *Client) UpdatePattern(ctx context.Context, patternID int64, patch map[string]any) (*Pattern, error) {
	var out Pattern
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: "/patterns/" + strconv.FormatInt(patternID, 10),
		body: mustJSON(patch), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeletePattern(ctx context.Context, patternID int64) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/patterns/" + strconv.FormatInt(patternID, 10),
	}, nil)
}
