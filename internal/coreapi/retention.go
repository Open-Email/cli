package coreapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Time-based retention (core's docs/store-capacity-design.md Part B): a
// per-mailbox AGE WINDOW in days. Live mail older than it is expunged into the
// restorable trash tier on the mailbox's own schedule (14 days there, then
// purged), aged by the stored arrival time — never the message's Date header.
// Two levels: the mailbox's OWN window, else the ACCOUNT default, else off, and
// off is the default everywhere. Writes are the owning account key's (or
// system's); an app password may read the policy but never set it.

// RetentionPreview is what one window WOULD expunge — the count and bytes of
// live messages older than it, measured by core. The preview is the rail that
// makes enabling retention a decision rather than a guess.
type RetentionPreview struct {
	Days     int64 `json:"days"`
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

// Retention is the mailbox view: the window in force, where it came from, both
// levels' raw values, the platform floor, and the previews asked for.
type Retention struct {
	// RetentionDays is the window in force: the mailbox's own, else the account
	// default, else nil (off).
	RetentionDays *int64 `json:"retentionDays"`
	// Source is "own", "account" or "none".
	Source               string             `json:"source"`
	OwnRetentionDays     *int64             `json:"ownRetentionDays"`
	AccountRetentionDays *int64             `json:"accountRetentionDays"`
	MinDays              int64              `json:"minDays"`
	OldestReceivedAt     *int64             `json:"oldestReceivedAt"`
	NextRunAt            *int64             `json:"nextRunAt"`
	Previews             []RetentionPreview `json:"previews"`
}

// AccountRetentionRow is one mailbox in the account-scope view.
type AccountRetentionRow struct {
	ID             string            `json:"id"`
	PrimaryAddress *string           `json:"primaryAddress"`
	RetentionDays  *int64            `json:"retentionDays"`
	EffectiveDays  *int64            `json:"effectiveDays"`
	Source         string            `json:"source"`
	Preview        *RetentionPreview `json:"preview"`
}

// AccountRetention is the account view: the default in force and one page of
// the account's mailboxes with their own window, the window in force, and a
// preview of what a window would expunge. Unreadable lists stores that could
// not be read — their rows are omitted, never reported as zero.
type AccountRetention struct {
	AccountID     string                `json:"accountId"`
	RetentionDays *int64                `json:"retentionDays"`
	MinDays       int64                 `json:"minDays"`
	Mailboxes     []AccountRetentionRow `json:"mailboxes"`
	Unreadable    []string              `json:"unreadable"`
	NextCursor    string                `json:"nextCursor,omitempty"`
}

type retentionBody struct {
	RetentionDays int `json:"retentionDays"`
}

func mailboxRetentionPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/retention"
}

func accountRetentionPath(accountID string) string {
	return "/accounts/" + escapeSegment(accountID) + "/retention"
}

func daysQuery(days []int) string {
	if len(days) == 0 {
		return ""
	}
	parts := make([]string, 0, len(days))
	for _, d := range days {
		parts = append(parts, strconv.Itoa(d))
	}
	return strings.Join(parts, ",")
}

// GetMailboxRetention reads the window in force and previews each window in
// `days` (at most 8; empty previews the window in force, if any).
func (c *Client) GetMailboxRetention(ctx context.Context, mailboxID string, days []int) (*Retention, error) {
	q := url.Values{}
	if ladder := daysQuery(days); ladder != "" {
		q.Set("days", ladder)
	}
	var out Retention
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: mailboxRetentionPath(mailboxID), query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetMailboxRetention sets the mailbox's OWN window. Core refuses a window
// below the platform floor with `retention_too_short` (the body carries
// minDays); an app password is refused `insufficient_scope`. The answer is the
// GET view, its preview being what the window will expunge on the next wake.
func (c *Client) SetMailboxRetention(ctx context.Context, mailboxID string, days int) (*Retention, error) {
	var out Retention
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: mailboxRetentionPath(mailboxID),
		body: mustJSON(retentionBody{RetentionDays: days}), contentType: "application/json",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearMailboxRetention clears the mailbox's own window; the account default,
// if any, applies again — the answer says which window is now in force.
func (c *Client) ClearMailboxRetention(ctx context.Context, mailboxID string) (*Retention, error) {
	var out Retention
	err := c.doJSON(ctx, request{method: http.MethodDelete, path: mailboxRetentionPath(mailboxID), idempotent: true}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAccountRetention pages the account's mailboxes with their retention state.
// A non-nil `days` previews THAT window on every mailbox (what a default would
// do before it is set); nil previews each mailbox's effective window.
func (c *Client) GetAccountRetention(ctx context.Context, accountID string, days *int, limit int, cursor string) (*AccountRetention, error) {
	q := url.Values{}
	if days != nil {
		q.Set("days", strconv.Itoa(*days))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out AccountRetention
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: accountRetentionPath(accountID), query: q, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetAccountRetention sets the account DEFAULT: every mailbox without a window
// of its own enforces it, EXISTING mailboxes included, pushed to each store by
// a background walk. Answers the account view with the first page of previews.
func (c *Client) SetAccountRetention(ctx context.Context, accountID string, days int) (*AccountRetention, error) {
	var out AccountRetention
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: accountRetentionPath(accountID),
		body: mustJSON(retentionBody{RetentionDays: days}), contentType: "application/json",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearAccountRetention clears the account default; mailboxes with their own
// window keep it.
func (c *Client) ClearAccountRetention(ctx context.Context, accountID string) (*AccountRetention, error) {
	var out AccountRetention
	err := c.doJSON(ctx, request{method: http.MethodDelete, path: accountRetentionPath(accountID), idempotent: true}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
