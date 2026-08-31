package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// CreateMailbox creates a mailbox (POST /identities — the identity is the
// subject; the mail store rides along). Account callers are capped by their account's
// maxMailboxes (403 mailbox_limit_reached, with maxMailboxes in Extra).
func (c *Client) CreateMailbox(ctx context.Context, in MailboxCreateInput) (*Mailbox, error) {
	var out Mailbox
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/identities",
		body:        mustJSON(in),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMailboxes returns one page of live identities (GET /identities; account
// callers see only their own).
func (c *Client) ListMailboxes(ctx context.Context, limit int, cursor string) (Page[Mailbox], error) {
	var out struct {
		Identities []Mailbox `json:"identities"`
		NextCursor string    `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/identities",
		query:      pageValues(limit, cursor),
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[Mailbox]{}, err
	}
	return Page[Mailbox]{Items: out.Identities, NextCursor: out.NextCursor}, nil
}

// ListDeletedMailboxes returns one page of restorable tombstones
// (GET /identities?state=deleted).
func (c *Client) ListDeletedMailboxes(ctx context.Context, limit int, cursor string) (Page[DeletedMailbox], error) {
	q := pageValues(limit, cursor)
	q.Set("state", "deleted")
	var out struct {
		Identities []DeletedMailbox `json:"identities"`
		NextCursor string           `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/identities",
		query:      q,
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[DeletedMailbox]{}, err
	}
	return Page[DeletedMailbox]{Items: out.Identities, NextCursor: out.NextCursor}, nil
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

// GetSendUsage returns what this mailbox has spent of its outbound send
// allowance in the current rolling window, plus the limits in force.
//
// Non-adopting on core's side: asking about a mailbox that has never sent
// reports an empty window rather than provisioning its store, so this is safe
// to poll.
func (c *Client) GetSendUsage(ctx context.Context, mailboxID string) (*SendUsage, error) {
	var out SendUsage
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/mailboxes/" + escapeSegment(mailboxID) + "/send-usage",
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
		path:        "/identities/" + escapeSegment(mailboxID),
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
		path:   "/identities/" + escapeSegment(mailboxID),
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
		path:   "/identities/" + escapeSegment(mailboxID) + "/restore",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PurgeMailbox expedites an already-soft-deleted mailbox: it waives the rest of
// the undo window (tombstone → non-restorable, wipe at the ~25 min floor).
// Idempotent. 404 no accessible tombstone, 409 not_deleted (the mailbox is live).
func (c *Client) PurgeMailbox(ctx context.Context, mailboxID string) (*MailboxPurgeResult, error) {
	var out MailboxPurgeResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/identities/" + escapeSegment(mailboxID) + "/purge",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
