package coreapi

import (
	"context"
	"net/http"
)

// Suppression is one address the platform's own evidence says not to mail.
// Rows are written by the FBL consumer from DSN/ARF reports and by the relay
// from an in-session hard bounce. Their SCOPE follows the evidence (core #884,
// "fix(fbl): scope complaints to sender accounts"): a hard bounce is
// deployment-global, refused for every account, while a spam complaint is
// filed against the account whose mail drew it and blocks that account alone.
// AccountID names that account; it is absent on a global row. Complaints that
// predate the scoping stay global until an operator moves them.
//
// The unscoped reads (ListSuppressions, GetSuppression) see the GLOBAL rows
// only: a 404 from GetSuppression is no verdict on any account's complaints.
// Account complaints are read with `?accountId=` (`openemail api GET
// /suppressions --query accountId=<id>`), which this client does not wrap.
//
// Detail is the diagnostic snippet or ARF feedback type verbatim from the
// report, so it is the only field that says WHY beyond the two-value Reason.
//
// Reason says what KIND of signal suppressed the address, never who wrote the
// row: core's two writers (the FBL consumer and POST /suppressions) share one
// two-value vocabulary. Provenance reads off the other fields — a hand-entered
// row has EventCount 0, because that field counts distinct REPORTS and an
// operator write has none, and a Detail behind a "manual" marker.
type Suppression struct {
	Address string  `json:"address"`
	Reason  string  `json:"reason"` // complaint | hard_bounce
	Detail  *string `json:"detail"`
	// SourceDeliveryID is the X-Delivery-Id of the most recent report that wrote
	// this row — the thread back to the traffic log.
	SourceDeliveryID *string `json:"sourceDeliveryId"`
	EventCount       int64   `json:"eventCount"`
	FirstEventAt     int64   `json:"firstEventAt"`
	LastEventAt      int64   `json:"lastEventAt"`
	// AccountID is the sending account whose complaint blocks this recipient,
	// on an account-scoped row; absent (empty) on a deployment-global one.
	// Optional on the wire rather than nullable, so "" and absent are one
	// state here.
	AccountID string `json:"accountId,omitempty"`
}

// ListSuppressions returns one page of the deployment-global suppression list
// (system-only). Account-scoped complaints are not in it; see Suppression.
func (c *Client) ListSuppressions(ctx context.Context, limit int, cursor string) (Page[Suppression], error) {
	var out struct {
		Suppressions []Suppression `json:"suppressions"`
		NextCursor   string        `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/suppressions",
		query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[Suppression]{}, err
	}
	return Page[Suppression]{Items: out.Suppressions, NextCursor: out.NextCursor}, nil
}

// GetSuppression looks up one address on the GLOBAL list (system keys; an
// account key sees hard_bounce rows only). A 404 is the ANSWER, not a failure:
// the address has no global row. It is no verdict on an account's complaints
// or address lists (core's own words), so it does not mean "clear to send".
// Callers should test with IsNotFound rather than treating it as an error.
func (c *Client) GetSuppression(ctx context.Context, address string) (*Suppression, error) {
	var out Suppression
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/suppressions/" + escapeSegment(address), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SuppressionCreate is a hand-entered suppression. Reason is optional and
// defaults to "complaint" server-side — the case this verb exists for is a real
// complaint core's FBL proof gate refused to act on, and it is the safer of the
// two defaults because core's reason precedence never downgrades a complaint to
// a bounce.
type SuppressionCreate struct {
	Address string `json:"address"`
	Reason  string `json:"reason,omitempty"` // complaint | hard_bounce
	Note    string `json:"note,omitempty"`
}

// AddSuppression suppresses an address by hand (system-only).
//
// This exists because core's FBL consumer only acts on a complaint it can PROVE
// we sent: a VERP-decoded envelope recipient, or one a recorded submission
// correlates to. Mail sent through REST /send, group expansion, Sieve redirect
// or forwarding records no correlatable row, so a genuine complaint about it is
// refused with an `fbl_suppression_refused` log line and nothing is acted on.
// This call is the operator standing in for the proof the pipeline could not
// produce — which is why it is system-only and logged loudly. It sends no
// accountId, so the row it writes is GLOBAL, refused for every account: wider
// than a complaint the consumer proves, which core files against the sending
// account alone.
//
// Idempotent by construction: repeat calls converge on the same row rather than
// erroring or inflating the tally.
func (c *Client) AddSuppression(ctx context.Context, in SuppressionCreate) (*Suppression, error) {
	var out Suppression
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/suppressions",
		body: mustJSON(in), contentType: "application/json", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LiftSuppression removes an address's GLOBAL row; an account complaint for it,
// if there is one, stays. 404 when there was no global row. System keys may
// lift any global row; an ACCOUNT key may lift hard_bounce rows only — a
// complaint row answers it 404 exactly like an absent one (deliberately
// indistinguishable, so the verb is no oracle over who complained), and only
// an operator can lift those.
func (c *Client) LiftSuppression(ctx context.Context, address string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/suppressions/" + escapeSegment(address),
	}, &out)
	return out.Deleted, err
}
