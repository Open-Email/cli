package coreapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

// ListSuspendedMailboxes returns one page of the live mailboxes the account
// owner has suspended, pending suspensions included (GET
// /identities?state=suspended). It is one account's listing: an account key
// sees its own, and a system key must name accountId (400 account_required
// otherwise).
func (c *Client) ListSuspendedMailboxes(ctx context.Context, accountID string, limit int, cursor string) (Page[Mailbox], error) {
	q := pageValues(limit, cursor)
	q.Set("state", "suspended")
	if accountID != "" {
		q.Set("accountId", accountID)
	}
	var out struct {
		Identities []Mailbox `json:"identities"`
		NextCursor string    `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/identities",
		query:      q,
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[Mailbox]{}, err
	}
	return Page[Mailbox]{Items: out.Identities, NextCursor: out.NextCursor}, nil
}

// SuspensionResult is a suspend or resume PATCH that core committed. Pending
// is the 202: the change is written and still being applied, so it is NOT yet
// enforced everywhere; RetryAfter is when core suggests polling GET for
// suspensionPending to clear. A 200 has Pending false.
type SuspensionResult struct {
	Mailbox    Mailbox
	Pending    bool
	RetryAfter time.Duration
	Raw        []byte
}

// SetMailboxSuspended suspends (true) or resumes (false) a mailbox: PATCH
// /identities/:id with `suspended` as the ONLY field, since core refuses it
// beside anything else (400 control_exclusive).
//
// Not retried. Repeating the same PATCH is safe at core, but an automatic
// repeat after a newer opposite change would reassert a stale intent, which
// the design forbids; the caller polls GET instead. A newer opposite change
// that superseded this one is the error 409 suspension_changed, whose current
// state SupersededMailbox reads back.
func (c *Client) SetMailboxSuspended(ctx context.Context, mailboxID string, suspended bool) (*SuspensionResult, error) {
	r := request{
		method:      http.MethodPatch,
		path:        "/identities/" + escapeSegment(mailboxID),
		body:        mustJSON(map[string]bool{"suspended": suspended}),
		contentType: "application/json",
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, fmt.Errorf("coreapi: read %s %s: %w", r.method, r.path, err)
	}
	out := &SuspensionResult{Raw: raw}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Mailbox); err != nil {
			return nil, fmt.Errorf("coreapi: decode %s %s: %w", r.method, r.path, err)
		}
	}
	if resp.StatusCode == http.StatusAccepted {
		out.Pending = true
		out.RetryAfter = parseRetryAfterSeconds(resp.Header.Get("Retry-After"))
	}
	return out, nil
}

// SupersededMailbox reads the current mailbox out of a 409
// suspension_changed: the state a newer opposite suspend or resume left, which
// is what the caller must report instead of its own request.
func SupersededMailbox(err error) (*Mailbox, bool) {
	ae, ok := asAPIError(err)
	if !ok || ae.Status != http.StatusConflict || ae.Code != "suspension_changed" {
		return nil, false
	}
	var body struct {
		Mailbox *Mailbox `json:"mailbox"`
	}
	if json.Unmarshal(ae.Body, &body) != nil || body.Mailbox == nil {
		return nil, true
	}
	return body.Mailbox, true
}

// parseRetryAfterSeconds reads a delta-seconds Retry-After; anything else
// (absent, an HTTP date, garbage) is 0 and the caller picks its own interval.
func parseRetryAfterSeconds(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
