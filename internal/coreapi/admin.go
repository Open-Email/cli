package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// VerifyResult is POST /auth/verify — verifies a credential and re-resolves
// sendability. Mints nothing. accountId is nullable, and so is mailboxId: a
// calendar-only identity (binding model, core's identity-design §IX) has no
// MAIL store, while identityId is always present and is the durable key.
type VerifyResult struct {
	IdentityID   string  `json:"identityId"`
	MailboxID    *string `json:"mailboxId"`
	AccountID    *string `json:"accountId"`
	CredentialID string  `json:"credentialId"`
	Kind         string  `json:"kind"`
	CanSend      bool    `json:"canSend"`
	// SendHold says WHICH kind of no, and is present only when CanSend is false:
	// "unverified" is a domain whose send records were never published (the
	// fix is DNS), "paused" is a hold that will be lifted, "disabled" is
	// permanent as far as the user is concerned (a stop, or a login that cannot
	// send at all). A UI needs the difference — "publish DNS", "sending is
	// paused" and "sending is not available" are different messages.
	// Advisory, like CanSend; the submission gate is the authority.
	SendHold      string   `json:"sendHold,omitempty"`
	PermittedFrom []string `json:"permittedFrom"`
	// Semantic reports whether this identity's mailbox has meaning-based search
	// enabled — i.e. whether a client should offer the semantic search box at
	// all. Advisory, like CanSend.
	Semantic bool `json:"semantic"`
	// Facets lists the identity's bound stores ("mail", "pim").
	Facets []string `json:"facets"`
	// ItipAuto is true when this login's domain auto-processes arriving
	// scheduling messages (`domains.itip`). RFC 6638 §4 makes auto-processing
	// what `calendar-auto-schedule` MEANS, so a gateway claims the class PER
	// PRINCIPAL off this field rather than server-wide — a constant would
	// promise §4 to principals for whom it is false.
	ItipAuto bool `json:"itipAuto"`
	// SecondFactor ("webauthn") is present when the matched credential is a
	// password and the identity has WebAuthn keys enrolled: the frontend this
	// login came through should demand the second factor. Advisory, like
	// CanSend; app-password logins never carry it.
	SecondFactor *string `json:"secondFactor,omitempty"`
	// Scope is the matched credential's rung (design D50) — see
	// Credential.Scope. Reported below "full" only to a caller that promised to
	// enforce it (`enforcesScope: true` on the request); any other caller is
	// answered 401 for such a credential.
	Scope string `json:"scope"`
}

// ReindexPage is one answer of POST /mailboxes/:id/reindex.
type ReindexPage struct {
	// Enqueued is how many messages THIS call queued for indexing.
	Enqueued int64 `json:"enqueued"`
	// Last is the id of the last message queued, the `after` of the next page:
	// nil when the call queued none, and from a core older than core #888,
	// which answers no cursor.
	Last *string `json:"last"`
}

// Reindex queues full-text index jobs for ONE PAGE of a mailbox's unindexed
// messages, in message id order (system-only). limit bounds the page (0 =
// core's default, 500; at most 5000), and after starts it past that message
// id, the previous page's Last ("" = from the first).
//
// One call never covers a mailbox larger than a page, and calling again
// without after does not either: a queued message stays unindexed until its
// job runs, so the same first page is queued again. Core #888 added the cursor
// for exactly that (core#886). Idempotent repair: a page queued twice costs a
// second index job per message, nothing else.
func (c *Client) Reindex(ctx context.Context, mailboxID string, limit int, after string) (*ReindexPage, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if after != "" {
		q.Set("after", after)
	}
	var out ReindexPage
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/reindex",
		query: q, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyLogin verifies IMAP/SMTP credentials (system-only). 401 invalid_credentials
// on a wrong password; 429 rate_limited once the per-username (or forwarded
// client-IP) attempt throttle trips. clientIP, when non-empty, is forwarded as
// X-Client-Ip — the END CLIENT's address, adding the per-client throttle
// dimension. Read-only; safe to retry.
func (c *Client) VerifyLogin(ctx context.Context, username, password, clientIP string) (*VerifyResult, error) {
	var headers map[string]string
	if clientIP != "" {
		headers = map[string]string{"X-Client-Ip": clientIP}
	}
	var out VerifyResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/auth/verify",
		body:        mustJSON(map[string]string{"username": username, "password": password}),
		contentType: "application/json", headers: headers, idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PickupIngest is the pop3-fetch ingestion path (system-only): stream a fetched RFC822
// message for a pickup source. checksum (BLAKE3 hex, optional) becomes the
// delivery-key suffix. Returns a DeliverResult (delivered/filtered).
func (c *Client) PickupIngest(ctx context.Context, pickupID string, getBody func() (io.ReadCloser, error), bodyLen int64, checksum string) (*DeliverResult, []byte, error) {
	headers := map[string]string{}
	if checksum != "" {
		headers["X-Checksum"] = checksum
	}
	var out DeliverResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/deliver/pickup/" + escapeSegment(pickupID),
		getBody: getBody, bodyLen: bodyLen, contentType: "message/rfc822",
		// Idempotent with or without X-Checksum: core keys the delivery on the
		// header when present and on the staged content hash when not, so a
		// replay of the same bytes converges as a duplicate.
		headers: headers, idempotent: true,
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// PickupReportInput is the pop3-fetch run-completion callback body. Only Status and
// Error are persisted by core; the counters are validated but discarded.
type PickupReportInput struct {
	Status        string  `json:"status"` // ok | partial | auth_failed | error
	Error         *string `json:"error,omitempty"`
	JobID         string  `json:"jobId,omitempty"`
	UploadedCount *int64  `json:"uploadedCount,omitempty"`
	DeletedCount  *int64  `json:"deletedCount,omitempty"`
	FailedCount   *int64  `json:"failedCount,omitempty"`
}

// PickupReport records a pickup run's outcome (system-only). ok/partial clear the
// failure counter; auth_failed/error increment it and can auto-disable the source.
func (c *Client) PickupReport(ctx context.Context, pickupID string, in PickupReportInput) error {
	return c.doJSON(ctx, request{
		method: http.MethodPost, path: "/pickups/" + escapeSegment(pickupID) + "/report",
		body: mustJSON(in), contentType: "application/json",
	}, nil)
}
