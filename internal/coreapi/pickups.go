package coreapi

import (
	"context"
	"net/http"
)

// PickupSource is a POP3 pickup source (publicSource projection). The password is
// write-only and never appears in any response. deleteAfterFetch and enabled are
// real JSON booleans.
type PickupSource struct {
	ID                  string          `json:"id"`
	MailboxID           string          `json:"mailboxId"`
	Name                *string         `json:"name"`
	Host                string          `json:"host"`
	Port                int64           `json:"port"`
	TLS                 string          `json:"tls"` // tls | starttls
	Username            string          `json:"username"`
	IntervalMinutes     int64           `json:"intervalMinutes"`
	DeleteAfterFetch    bool            `json:"deleteAfterFetch"`
	LabelName           *string         `json:"labelName"` // destination folder; nil = INBOX
	Enabled             bool            `json:"enabled"`
	LastRunAt           *int64          `json:"lastRunAt"`
	LastStatus          *string         `json:"lastStatus"` // ok | partial | auth_failed | error
	LastError           *string         `json:"lastError"`
	ConsecutiveFailures int64           `json:"consecutiveFailures"`
	LastImportedCount   *int64          `json:"lastImportedCount"` // last completed run's uploadedCount; nil = none reported
	CreatedAt           int64           `json:"createdAt"`
	Dispatch            *PickupDispatch `json:"dispatch,omitempty"`
}

// PickupDispatch is the scheduler's own record of its last hand-off to the
// fetcher — distinct from LastStatus, which the fetcher reports only when a run
// FINISHES. A source whose job is never accepted has no run to report, so this
// is the only field that can say why nothing is arriving. Nil when the read was
// not made (the operator listing, or a scheduler briefly unreachable).
type PickupDispatch struct {
	LastAttemptAt       *int64  `json:"lastAttemptAt"`
	Outcome             *string `json:"outcome"` // dispatched | busy | rejected | unreachable | decrypt_failed | unconfigured
	Detail              *string `json:"detail"`
	ConsecutiveFailures int64   `json:"consecutiveFailures"`
	NextRunAt           *int64  `json:"nextRunAt"`
}

// PickupCreateInput is the POST body. Password is required and write-only. TLS
// defaults to "tls", IntervalMinutes to 15, DeleteAfterFetch to true when
// omitted (server-side).
type PickupCreateInput struct {
	Host             string  `json:"host"`
	Port             int64   `json:"port"`
	Username         string  `json:"username"`
	Password         string  `json:"password"`
	Name             *string `json:"name,omitempty"`
	TLS              string  `json:"tls,omitempty"`
	IntervalMinutes  *int64  `json:"intervalMinutes,omitempty"`
	DeleteAfterFetch *bool   `json:"deleteAfterFetch,omitempty"`
	LabelName        *string `json:"labelName,omitempty"` // destination folder, created on first use
}

func (c *Client) pickupsPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/pickups"
}

// ListPickups returns one page of pickup sources.
func (c *Client) ListPickups(ctx context.Context, mailboxID string, limit int, cursor string) (Page[PickupSource], error) {
	var out struct {
		Pickups    []PickupSource `json:"pickups"`
		NextCursor string         `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.pickupsPath(mailboxID),
		query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[PickupSource]{}, err
	}
	return Page[PickupSource]{Items: out.Pickups, NextCursor: out.NextCursor}, nil
}

// GetPickup returns one pickup source (no secret).
func (c *Client) GetPickup(ctx context.Context, mailboxID, pickupID string) (*PickupSource, error) {
	var out PickupSource
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.pickupsPath(mailboxID) + "/" + escapeSegment(pickupID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePickup creates a pickup source. 409 duplicate_source when (host,
// username) collide for this mailbox.
func (c *Client) CreatePickup(ctx context.Context, mailboxID string, in PickupCreateInput) (*PickupSource, error) {
	var out PickupSource
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.pickupsPath(mailboxID),
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePickup applies a partial patch. The patch is a map so an omitted password
// is preserved (never sent) and an explicit null name clears the display name. An
// empty patch (no updatable fields) returns 404.
func (c *Client) UpdatePickup(ctx context.Context, mailboxID, pickupID string, patch map[string]any) (*PickupSource, error) {
	var out PickupSource
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: c.pickupsPath(mailboxID) + "/" + escapeSegment(pickupID),
		body: mustJSON(patch), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePickup hard-deletes a pickup source.
func (c *Client) DeletePickup(ctx context.Context, mailboxID, pickupID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.pickupsPath(mailboxID) + "/" + escapeSegment(pickupID),
	}, nil)
}

// RunPickup schedules an out-of-band run (202 scheduled). 409 disabled if the
// source is disabled.
func (c *Client) RunPickup(ctx context.Context, mailboxID, pickupID string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.pickupsPath(mailboxID) + "/" + escapeSegment(pickupID) + "/run",
	}, &out)
	return out.Status, err
}
