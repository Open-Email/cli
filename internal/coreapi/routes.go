package coreapi

import (
	"context"
	"net/http"
)

// Route mirrors the client-facing route shape (accountId stripped; webhookSecret
// never returned). paced is a real JSON boolean (core normalizes it at the
// endpoint boundary).
type Route struct {
	Address         string  `json:"address"`
	Domain          string  `json:"domain"`
	DestinationType string  `json:"destinationType"`
	MailboxID       *string `json:"mailboxId"`
	WebhookURL      *string `json:"webhookUrl"`
	RemoteAddress   *string `json:"remoteAddress"`
	Paced           bool    `json:"paced"`
	CreatedAt       int64   `json:"createdAt"`
	// Posting ("open" | "members") and MemberCount are group-only and present
	// only on single-route answers (create/get/update) — absent on the list and
	// for other destination kinds.
	Posting     string `json:"posting,omitempty"`
	MemberCount *int64 `json:"memberCount,omitempty"`
	// PersonaMailboxID is the group's PERSONA — the address-less mailbox that
	// keeps the group's archive and may send as the group address (core's
	// docs/group-persona-design.md). Group-only, single-route answers and the
	// type=group listing; nil while the group's delete is in flight.
	PersonaMailboxID *string `json:"personaMailboxId,omitempty"`
}

// RouteMember is one group member.
type RouteMember struct {
	Address       string `json:"address"`
	MemberAddress string `json:"memberAddress"`
	CreatedAt     int64  `json:"createdAt"`
}

// RouteCreateInput is the POST /routes body. destinationType is required; the
// matching destination field must be set (core enforces).
type RouteCreateInput struct {
	Address         string `json:"address"`
	DestinationType string `json:"destinationType"`
	MailboxID       string `json:"mailboxId,omitempty"`
	WebhookURL      string `json:"webhookUrl,omitempty"`
	WebhookSecret   string `json:"webhookSecret,omitempty"`
	RemoteAddress   string `json:"remoteAddress,omitempty"`
	Paced           bool   `json:"paced"`
	// Posting is the group posting policy ("open" | "members"); group routes
	// only (core answers 400 otherwise). Empty = core's default.
	Posting string `json:"posting,omitempty"`
}

// ListRoutes lists routes, optionally filtered by domain, mailbox, and/or
// destination type (mailbox|webhook|remote|group; empty = all). A type=group
// listing is ENRICHED: core adds posting + memberCount to every row precisely
// so a groups console needs no per-row GetRoute — other listings stay bare.
func (c *Client) ListRoutes(ctx context.Context, domain, mailboxID, routeType string, limit int, cursor string) (Page[Route], error) {
	q := pageValues(limit, cursor)
	if domain != "" {
		q.Set("domain", domain)
	}
	if mailboxID != "" {
		q.Set("mailboxId", mailboxID)
	}
	if routeType != "" {
		q.Set("type", routeType)
	}
	var out struct {
		Routes     []Route `json:"routes"`
		NextCursor string  `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/routes", query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[Route]{}, err
	}
	return Page[Route]{Items: out.Routes, NextCursor: out.NextCursor}, nil
}

func (c *Client) CreateRoute(ctx context.Context, in RouteCreateInput) (*Route, error) {
	var out Route
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/routes", body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetRoute(ctx context.Context, address string) (*Route, error) {
	var out Route
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/routes/" + escapeSegment(address), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRoute applies a partial patch. paced alone is partial; a destinationType
// with its fields atomically replaces the destination (destination fields
// without a type are rejected by core with destination_requires_type).
func (c *Client) UpdateRoute(ctx context.Context, address string, patch map[string]any) (*Route, error) {
	var out Route
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: "/routes/" + escapeSegment(address),
		body: mustJSON(patch), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteRoute(ctx context.Context, address string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/routes/" + escapeSegment(address),
	}, nil)
}

// --- group members ---

func (c *Client) routeMembersPath(address string) string {
	return "/routes/" + escapeSegment(address) + "/members"
}

func (c *Client) ListMembers(ctx context.Context, address string, limit int, cursor string) (Page[RouteMember], error) {
	var out struct {
		Members    []RouteMember `json:"members"`
		NextCursor string        `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.routeMembersPath(address),
		query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[RouteMember]{}, err
	}
	return Page[RouteMember]{Items: out.Members, NextCursor: out.NextCursor}, nil
}

// ReplaceMembers replaces the whole member list; returns the full normalized set.
func (c *Client) ReplaceMembers(ctx context.Context, address string, members []string) ([]RouteMember, error) {
	if members == nil {
		members = []string{}
	}
	var out struct {
		Members []RouteMember `json:"members"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: c.routeMembersPath(address),
		body: mustJSON(map[string]any{"members": members}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Members, nil
}

// RouteMemberBatchResult reports one batch add's insertion counts:
// already-present members count as duplicates, never errors.
type RouteMemberBatchResult struct {
	AddedCount     int64 `json:"addedCount"`
	DuplicateCount int64 `json:"duplicateCount"`
}

// BatchAddMembers adds up to 1000 members in one atomic call. Idempotent by
// core's contract (a re-run reports the overlap as duplicates and converges),
// so the request retries on transport errors like a GET.
func (c *Client) BatchAddMembers(ctx context.Context, address string, members []string) (*RouteMemberBatchResult, error) {
	var out RouteMemberBatchResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.routeMembersPath(address) + "/batch",
		body: mustJSON(map[string]any{"members": members}), contentType: "application/json",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AddMember adds one member. 409 member_exists on collision.
func (c *Client) AddMember(ctx context.Context, address, memberAddress string) error {
	return c.doJSON(ctx, request{
		method: http.MethodPost, path: c.routeMembersPath(address),
		body: mustJSON(map[string]string{"memberAddress": memberAddress}), contentType: "application/json",
	}, nil)
}

// RemoveMember removes one member.
func (c *Client) RemoveMember(ctx context.Context, address, memberAddress string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   c.routeMembersPath(address) + "/" + escapeSegment(memberAddress),
	}, nil)
}
