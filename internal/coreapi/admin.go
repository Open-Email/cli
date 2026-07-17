package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// VerifyResult is POST /auth/verify — verifies a credential and re-resolves
// sendability. Mints nothing. accountId is nullable.
type VerifyResult struct {
	MailboxID     string   `json:"mailboxId"`
	AccountID     *string  `json:"accountId"`
	CredentialID  string   `json:"credentialId"`
	Kind          string   `json:"kind"`
	CanSend       bool     `json:"canSend"`
	PermittedFrom []string `json:"permittedFrom"`
}

// Reindex re-enqueues FTS index jobs for a mailbox (system-only). limit bounds the
// batch (0 = unbounded). Returns the count enqueued. Idempotent repair.
func (c *Client) Reindex(ctx context.Context, mailboxID string, limit int) (int64, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	var out struct {
		Enqueued int64 `json:"enqueued"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/reindex",
		query: q, idempotent: true,
	}, &out)
	return out.Enqueued, err
}

// VerifyLogin verifies IMAP/SMTP credentials (system-only). 401 invalid_credentials
// on a wrong password. Read-only; safe to retry.
func (c *Client) VerifyLogin(ctx context.Context, username, password string) (*VerifyResult, error) {
	var out VerifyResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/auth/verify",
		body:        mustJSON(map[string]string{"username": username, "password": password}),
		contentType: "application/json", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PickupIngest is the Poppy ingestion path (system-only): stream a fetched RFC822
// message for a pickup source. checksum (BLAKE3 hex, optional) becomes the
// delivery-key suffix. Returns a DeliverResult (delivered/filtered).
func (c *Client) PickupIngest(ctx context.Context, sourceID string, getBody func() (io.ReadCloser, error), bodyLen int64, checksum string) (*DeliverResult, []byte, error) {
	headers := map[string]string{}
	if checksum != "" {
		headers["X-Checksum"] = checksum
	}
	var out DeliverResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/deliver/pickup/" + escapeSegment(sourceID),
		getBody: getBody, bodyLen: bodyLen, contentType: "message/rfc822",
		headers: headers, idempotent: true,
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// PickupReportInput is the Poppy run-completion callback body. Only Status and
// Error are persisted by core; the counters are validated but discarded.
type PickupReportInput struct {
	Status   string  `json:"status"` // ok | partial | auth_failed | error
	Error    *string `json:"error,omitempty"`
	JobID    string  `json:"jobId,omitempty"`
	Uploaded *int64  `json:"uploaded,omitempty"`
	Deleted  *int64  `json:"deleted,omitempty"`
	Failed   *int64  `json:"failed,omitempty"`
}

// PickupReport records a pickup run's outcome (system-only). ok/partial clear the
// failure counter; auth_failed/error increment it and can auto-disable the source.
func (c *Client) PickupReport(ctx context.Context, sourceID string, in PickupReportInput) error {
	return c.doJSON(ctx, request{
		method: http.MethodPost, path: "/pickups/" + escapeSegment(sourceID) + "/report",
		body: mustJSON(in), contentType: "application/json",
	}, nil)
}
