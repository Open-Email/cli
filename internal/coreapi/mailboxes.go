package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// CreateMailbox creates a mailbox. Account callers are capped by their account's
// maxMailboxes (403 mailbox_limit_reached, with maxMailboxes in Extra).
func (c *Client) CreateMailbox(ctx context.Context, in MailboxCreateInput) (*Mailbox, error) {
	var out Mailbox
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/mailboxes",
		body:        mustJSON(in),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMailboxes returns one page of live mailboxes (account callers see only
// their own).
func (c *Client) ListMailboxes(ctx context.Context, limit int, cursor string) (Page[Mailbox], error) {
	var out struct {
		Mailboxes  []Mailbox `json:"mailboxes"`
		NextCursor string    `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/mailboxes",
		query:      pageValues(limit, cursor),
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[Mailbox]{}, err
	}
	return Page[Mailbox]{Items: out.Mailboxes, NextCursor: out.NextCursor}, nil
}

// ListDeletedMailboxes returns one page of restorable tombstones
// (GET /mailboxes?state=deleted).
func (c *Client) ListDeletedMailboxes(ctx context.Context, limit int, cursor string) (Page[DeletedMailbox], error) {
	q := pageValues(limit, cursor)
	q.Set("state", "deleted")
	var out struct {
		Mailboxes  []DeletedMailbox `json:"mailboxes"`
		NextCursor string           `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/mailboxes",
		query:      q,
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[DeletedMailbox]{}, err
	}
	return Page[DeletedMailbox]{Items: out.Mailboxes, NextCursor: out.NextCursor}, nil
}

// GetMailbox returns a mailbox with usage stats.
func (c *Client) GetMailbox(ctx context.Context, mailboxID string) (*Mailbox, error) {
	var out Mailbox
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/mailboxes/" + escapeSegment(mailboxID),
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateMailbox applies a partial patch (quotaBytes and/or primaryAddress). The
// patch is passed as a map so an explicit null quotaBytes (set unlimited) is
// distinguishable from an omitted one (leave unchanged), matching core's
// "field !== undefined" semantics.
func (c *Client) UpdateMailbox(ctx context.Context, mailboxID string, patch map[string]any) (*Mailbox, error) {
	var out Mailbox
	err := c.doJSON(ctx, request{
		method:      http.MethodPatch,
		path:        "/mailboxes/" + escapeSegment(mailboxID),
		body:        mustJSON(patch),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMailbox soft-deletes a mailbox (7-day undo) unless purge=true, which
// waives the undo window and refuses restore.
func (c *Client) DeleteMailbox(ctx context.Context, mailboxID string, purge bool) (*MailboxDeleteResult, error) {
	q := url.Values{}
	if purge {
		q.Set("purge", "true")
	}
	var out MailboxDeleteResult
	err := c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/mailboxes/" + escapeSegment(mailboxID),
		query:  q,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreMailbox undoes a soft delete within the window. 409 not_deleted (live),
// 410 gone (window elapsed / purge delete / store wiped), 404 no tombstone.
func (c *Client) RestoreMailbox(ctx context.Context, mailboxID string) (*MailboxRestoreResult, error) {
	var out MailboxRestoreResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/restore",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
