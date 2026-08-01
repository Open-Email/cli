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

// LiftSuppression clears an address to send again (system-only). 404 when the
// address was not suppressed.
func (c *Client) LiftSuppression(ctx context.Context, address string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: "/suppressions/" + escapeSegment(address),
	}, &out)
	return out.Deleted, err
}
