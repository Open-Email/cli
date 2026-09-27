package coreapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// SchedulingJob is one calendar scheduling job in a mailbox's store, as core's
// support read shows it (GET /system/mailboxes/{id}/scheduling). Times are
// unix seconds. A finished job's plan is gone, so DueAt, Recipients, Completed
// and Refused are nil on it.
type SchedulingJob struct {
	ID         int64   `json:"id"`
	UID        string  `json:"uid"`
	State      string  `json:"state"`
	Attempts   int64   `json:"attempts"`
	CreatedAt  int64   `json:"createdAt"`
	DueAt      *int64  `json:"dueAt"`
	ExpiresAt  int64   `json:"expiresAt"`
	LastError  *string `json:"lastError"`
	Recipients *int64  `json:"recipients"`
	Completed  *int64  `json:"completed"`
	Refused    *int64  `json:"refused"`
}

// SchedulingFailure is one outbound scheduling log row whose status is a
// failure (3.x or 5.x): who was not reached, and why.
type SchedulingFailure struct {
	ID          string  `json:"id"`
	UID         string  `json:"uid"`
	Method      string  `json:"method"`
	Counterpart string  `json:"counterpart"`
	Outcome     *string `json:"outcome"`
	Reason      *string `json:"reason"`
	Status      *string `json:"status"`
	CreatedAt   int64   `json:"createdAt"`
}

// MailboxScheduling is the support read of one mailbox's calendar scheduling.
type MailboxScheduling struct {
	MailboxID string              `json:"mailboxId"`
	Pending   []SchedulingJob     `json:"pending"`
	Recent    []SchedulingJob     `json:"recent"`
	Failures  []SchedulingFailure `json:"failures"`
}

// GetMailboxScheduling reads one mailbox's scheduling jobs and latest failures
// (system-only). uid narrows every list to one event; limit <= 0 leaves core's
// default.
func (c *Client) GetMailboxScheduling(ctx context.Context, mailboxID, uid string, limit int) (*MailboxScheduling, error) {
	query := url.Values{}
	if uid != "" {
		query.Set("uid", uid)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out MailboxScheduling
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/system/mailboxes/" + escapeSegment(mailboxID) + "/scheduling",
		query: query, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
