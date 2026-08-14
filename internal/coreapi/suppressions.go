package coreapi

import (
	"context"
	"net/http"
)

// Suppression is one address on the deployment-global do-not-send list. Rows
// are written by the FBL consumer from DSN/ARF reports — a hard bounce or a
// spam complaint — and they suppress sending platform-wide, not per account.
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
}

// ListSuppressions returns one page of the do-not-send list (system-only).
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

// GetSuppression looks up one address (system-only). A 404 is the ANSWER, not a
// failure: it means the address is clear to send. Callers should test with
// IsNotFound rather than treating it as an error.
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
// produce — which is why it is system-only and logged loudly.
//
// Idempotent by construction: repeat calls converge on the same row rather than
// erroring or inflating the tally.
func (c *Client) AddSuppression(ctx context.Context, in SuppressionCreate) (*Suppression, error) {
	var out Suppression
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/suppressions",
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LiftSuppression clears an address to send again. 404 when the address was
// not suppressed. System keys may lift any row; an ACCOUNT key may lift
// hard_bounce rows only — a complaint row answers it 404 exactly like an
// absent one (deliberately indistinguishable, so the verb is no oracle over
// who complained), and only an operator can lift those.
func (c *Client) LiftSuppression(ctx context.Context, address string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/suppressions/" + escapeSegment(address),
	}, &out)
	return out.Deleted, err
}

// AccountSuppression is one address on an ACCOUNT's own do-not-send list —
// the tenant-owned tier beside the deployment-global list above (core
// migration 0033). These rows are POLICY where the global rows are EVIDENCE:
// the account said "never mail this address" (an unsubscribe, a legal
// request, an import from a previous provider), and they bind only mail
// attributable to the owning account (the sending domain's owner). Enforced
// at the same relay checkpoint; the global row wins ties.
type AccountSuppression struct {
	Address string  `json:"address"`
	Reason  string  `json:"reason"` // manual | complaint | hard_bounce
	Note    *string `json:"note"`
	// CreatedAt is when the address FIRST went on the list — a repeat add
	// refreshes reason/note but keeps this.
	CreatedAt int64 `json:"createdAt"`
}

// ListAccountSuppressions returns one page of an account's own do-not-send
// list. Account keys read their own account; system keys any.
func (c *Client) ListAccountSuppressions(ctx context.Context, accountID string, limit int, cursor string) (Page[AccountSuppression], error) {
	var out struct {
		Suppressions []AccountSuppression `json:"suppressions"`
		NextCursor   string               `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/accounts/" + escapeSegment(accountID) + "/suppressions",
		query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[AccountSuppression]{}, err
	}
	return Page[AccountSuppression]{Items: out.Suppressions, NextCursor: out.NextCursor}, nil
}

// GetAccountSuppression looks up one address on an account's list. Like
// GetSuppression, a 404 is the ANSWER (not listed), test with IsNotFound.
func (c *Client) GetAccountSuppression(ctx context.Context, accountID, address string) (*AccountSuppression, error) {
	var out AccountSuppression
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/accounts/" + escapeSegment(accountID) + "/suppressions/" + escapeSegment(address),
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AccountSuppressionCreate adds an address to an account's own list. Reason
// defaults to "manual" server-side; complaint/hard_bounce exist so an import
// from a previous provider keeps what its rows meant.
type AccountSuppressionCreate struct {
	Address string `json:"address"`
	Reason  string `json:"reason,omitempty"` // manual | complaint | hard_bounce
	Note    string `json:"note,omitempty"`
}

// AddAccountSuppression puts an address on the account's do-not-send list.
// Idempotent: a repeat converges on the same row (reason/note refreshed, the
// original createdAt kept).
func (c *Client) AddAccountSuppression(ctx context.Context, accountID string, in AccountSuppressionCreate) (*AccountSuppression, error) {
	var out AccountSuppression
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/accounts/" + escapeSegment(accountID) + "/suppressions",
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveAccountSuppression takes an address off the account's own list. 404
// when it was not listed. This clears only the account's POLICY row — an
// address the platform suppressed on evidence stays refused; a hard-bounce
// row there can additionally be lifted with LiftSuppression, which core
// permits account keys for exactly that reason class.
func (c *Client) RemoveAccountSuppression(ctx context.Context, accountID, address string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/accounts/" + escapeSegment(accountID) + "/suppressions/" + escapeSegment(address),
	}, &out)
	return out.Deleted, err
}
