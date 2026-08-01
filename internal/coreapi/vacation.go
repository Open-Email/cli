package coreapi

import (
	"context"
	"net/http"
)

// Vacation is the RFC 8621 §8 out-of-office document plus its CAS token.
//
// A mailbox that has never configured one reads as disabled with null fields —
// there is no "not found" state, so a client never has to tell an unset document
// apart from a missing mailbox.
//
// State is opaque: feed it back as the If-Match guard on a write.
type Vacation struct {
	IsEnabled bool `json:"isEnabled"`
	// FromDate/ToDate bound the absence, as `2006-01-02T15:04:05Z` UTC instants.
	// Null means unbounded on that side.
	FromDate *string `json:"fromDate"`
	ToDate   *string `json:"toDate"`
	Subject  *string `json:"subject"`
	TextBody *string `json:"textBody"`
	HtmlBody *string `json:"htmlBody"`
	State    string  `json:"state"`
}

// VacationInput is the replacement document. IsEnabled is required; every other
// field is nullable and omitting it clears that field, since the PUT replaces
// the whole document rather than patching it.
type VacationInput struct {
	IsEnabled bool    `json:"isEnabled"`
	FromDate  *string `json:"fromDate,omitempty"`
	ToDate    *string `json:"toDate,omitempty"`
	Subject   *string `json:"subject,omitempty"`
	TextBody  *string `json:"textBody,omitempty"`
	HtmlBody  *string `json:"htmlBody,omitempty"`
}

func vacationPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/vacation"
}

// GetVacation reads the auto-reply document and its state.
func (c *Client) GetVacation(ctx context.Context, mailboxID string) (*Vacation, error) {
	var out Vacation
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: vacationPath(mailboxID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutVacation replaces the whole document. ifMatch is the compare-and-swap
// guard: pass the State a read returned and a stale write is refused
// 412 state_conflict; "" (or "*") writes unconditionally.
//
// The timing rule is the surprising part, and it lives in the server:
// ENABLING starts a new absence, which makes every correspondent eligible for
// one auto-reply again, while editing the text of an already-enabled document
// deliberately re-notifies nobody. A write that changes nothing is a no-op and
// resets no one's reply period. So toggling off and on is NOT the same as
// editing, and a client should not offer them as if they were.
//
// 422 invalid_window when FromDate is not before ToDate.
func (c *Client) PutVacation(ctx context.Context, mailboxID string, in VacationInput, ifMatch string) (*Vacation, error) {
	var headers map[string]string
	if ifMatch != "" {
		headers = map[string]string{"If-Match": ifMatch}
	}
	var out Vacation
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: vacationPath(mailboxID),
		body: mustJSON(in), contentType: "application/json", headers: headers,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
