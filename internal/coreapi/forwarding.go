package coreapi

import (
	"context"
	"net/http"
)

// Verified forwarding: the mailbox owner names a destination, core mails that
// destination an eight-character code, and nothing is forwarded until the code
// comes back. The ceremony exists because the party who bears the consequence
// of a forward is the RECIPIENT, not the person configuring it — so consent is
// proved by whoever holds the destination mailbox, not asserted by the tenant.
//
// The state machine is the whole contract: `pending` forwards nothing,
// `verified` forwards, `revoked_by_recipient` is a destination whose owner used
// the disable link in a forwarded message and stopped it from their side. Only
// a verified destination may be pointed at, which is why every write below can
// answer 403 destination_not_verified.

// ForwardingDestination is one destination and where it stands in the ceremony.
//
// CodeExpiresAt is non-null only while a code is outstanding, and VerifiedAt
// only once one was spent — so the pair reads the state machine without
// consulting State, and neither is a countdown a client should re-derive.
type ForwardingDestination struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	// State is pending | verified | revoked_by_recipient. Only `verified`
	// forwards.
	State         string `json:"state"`
	CodeExpiresAt *int64 `json:"codeExpiresAt"`
	VerifiedAt    *int64 `json:"verifiedAt"`
	CreatedAt     int64  `json:"createdAt"`
}

// ForwardingSetting is the forward-everything switch. DestinationID and Address
// are both null when it is off, and Paused is a HOLD that keeps the destination
// verified — resuming needs no second ceremony, which is the whole reason pause
// exists beside clear.
type ForwardingSetting struct {
	DestinationID *string `json:"destinationId"`
	Address       *string `json:"address"`
	Paused        bool    `json:"paused"`
}

// Forwarding is the mailbox's whole forwarding picture in one read.
type Forwarding struct {
	Destinations []ForwardingDestination `json:"destinations"`
	ForwardAll   ForwardingSetting       `json:"forwardAll"`
}

// ForwardingDestinationPending is the answer to naming an address: the code is
// in the mail, not in this response. Address comes back MASKED (j••@ex•••.com)
// — core will not echo an address back to a caller who has not yet proved they
// can read its mail, since doing so would make this endpoint an address oracle.
type ForwardingDestinationPending struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	State   string `json:"state"`
	// ExpiresAt is when the mailed code lapses, in epoch seconds (24h).
	ExpiresAt int64 `json:"expiresAt"`
}

// ForwardingDestinationVerified is the answer to spending a code. Address is
// masked here too, for the same reason.
type ForwardingDestinationVerified struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	State   string `json:"state"`
}

func forwardingPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/forwarding"
}

func forwardingDestPath(mailboxID, destID string) string {
	return forwardingPath(mailboxID) + "/destinations/" + escapeSegment(destID)
}

// GetForwarding reads every destination plus the forward-everything setting.
func (c *Client) GetForwarding(ctx context.Context, mailboxID string) (*Forwarding, error) {
	var out Forwarding
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: forwardingPath(mailboxID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AddForwardingDestination names an address and mails it a confirmation code.
// Nothing forwards until VerifyForwardingDestination spends that code.
//
// Re-posting an address that already exists re-mails a FRESH code rather than
// refusing, which is also how a destination the recipient revoked is re-proved
// — so this is the resend verb too, and a caller need not tell the two apart.
//
// 400 when the address cannot be a destination: destination_is_self /
// destination_loops / destination_chain_too_long (it routes back here),
// destination_not_permitted (the mailbox's allowlist refuses it), or
// destination_fans_out — an address we host that reaches MORE than one party
// (a group, an alias, a webhook, a route that forwards on). The last is a
// CONSENT refusal, not a routing one: the code proves nothing on the other
// members' behalf, and the disable link is a capability that stops this
// mailbox's whole forwarding stream for whoever spends it first.
//
// 409 too_many_destinations at the per-mailbox cap, 429 either under the resend
// cooldown or once the mailbox's daily budget of confirmation mails is spent —
// deliberately not distinguishable, since part of that budget is per destination
// address across the deployment and telling them apart would make this verb an
// oracle for whether an address was recently solicited. 503 when the code could
// not be sent — the one to retry, since nothing was recorded.
func (c *Client) AddForwardingDestination(ctx context.Context, mailboxID, address string) (*ForwardingDestinationPending, error) {
	var out ForwardingDestinationPending
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: forwardingPath(mailboxID) + "/destinations",
		body:        mustJSON(map[string]string{"address": address}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyForwardingDestination spends the code mailed to the destination.
//
// Five wrong codes end the ceremony — the destination is left needing a fresh
// code from AddForwardingDestination, so a client should not loop on 403.
// Confirming invalidates every disable link previously mailed for it.
func (c *Client) VerifyForwardingDestination(ctx context.Context, mailboxID, destID, code string) (*ForwardingDestinationVerified, error) {
	var out ForwardingDestinationVerified
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: forwardingDestPath(mailboxID, destID) + "/verify",
		body:        mustJSON(map[string]string{"code": code}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteForwardingDestination removes a destination. Removing the one that
// forward-everything points at also turns that off — the setting cannot outlive
// its target.
func (c *Client) DeleteForwardingDestination(ctx context.Context, mailboxID, destID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete, path: forwardingDestPath(mailboxID, destID),
	}, nil)
}

// SetForwardAll points forward-everything at a VERIFIED destination.
// 403 destination_not_verified when it has not proved a code.
//
// The five 400s AddForwardingDestination can answer are re-taken HERE too, on
// an already-verified destination: this is the write that arms the forward, and
// a route proved as a single mailbox may have been retargeted to a group, or
// back at this mailbox, since the code was spent. So a client must not treat
// "verified" as meaning this call cannot be refused.
func (c *Client) SetForwardAll(ctx context.Context, mailboxID, destinationID string) (*Forwarding, error) {
	var out Forwarding
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: forwardingPath(mailboxID) + "/all",
		body:        mustJSON(map[string]string{"destinationId": destinationID}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PauseForwardAll holds or resumes forwarding without giving up the
// destination. It never re-verifies, so resuming is free — which is what makes
// this the right verb for a temporary stop, and ClearForwardAll the right one
// for "not any more".
func (c *Client) PauseForwardAll(ctx context.Context, mailboxID string, paused bool) (*Forwarding, error) {
	var out Forwarding
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: forwardingPath(mailboxID) + "/all",
		body:        mustJSON(map[string]bool{"paused": paused}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearForwardAll turns forward-everything off. The destination stays verified,
// so pointing at it again needs no new ceremony.
func (c *Client) ClearForwardAll(ctx context.Context, mailboxID string) (*Forwarding, error) {
	var out Forwarding
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: forwardingPath(mailboxID) + "/all",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
