package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// ListAccounts returns one page of accounts. SYSTEM-ONLY: an account principal
// gets 403 system_credentials_required — the CLI uses exactly that to tell a
// system key from an account key.
func (c *Client) ListAccounts(ctx context.Context, limit int, cursor string) (Page[Account], error) {
	var out struct {
		Accounts   []Account `json:"accounts"`
		NextCursor string    `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/accounts",
		query:      pageValues(limit, cursor),
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[Account]{}, err
	}
	return Page[Account]{Items: out.Accounts, NextCursor: out.NextCursor}, nil
}

// GetAccount returns one account (system, or the account itself). Foreign/missing
// answer 404.
func (c *Client) GetAccount(ctx context.Context, accountID string) (*Account, error) {
	var out Account
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/accounts/" + escapeSegment(accountID),
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAccount soft-deletes an account (core migration 0038). Reachable by a
// SYSTEM key or the account's OWN key — self-service deletion is the feature,
// and the grace window is the protection.
//
// Destroys nothing: it stamps the lifecycle and schedules the purge. `purge`
// waives the window and is SYSTEM-ONLY (403 otherwise) — the irreversible form
// must not be reachable with a stolen tenant key.
//
// Idempotent. A repeat answers the first call's instants rather than a fresh
// window, so a retried command cannot extend the customer's deadline.
func (c *Client) DeleteAccount(ctx context.Context, accountID string, purge bool) (*AccountDeleteResult, error) {
	query := url.Values{}
	if purge {
		query.Set("purge", "true")
	}
	var out AccountDeleteResult
	err := c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/accounts/" + escapeSegment(accountID),
		query:  query,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreAccount cancels a pending deletion. 409 not_deleted when there is
// nothing to undo, 409 purge_in_progress once the teardown has claimed the
// account, 410 account_purged once it has finished.
func (c *Client) RestoreAccount(ctx context.Context, accountID string) (*Account, error) {
	var out Account
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/accounts/" + escapeSegment(accountID) + "/restore",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type createAccountReq struct {
	Name         string `json:"name"`
	MaxMailboxes *int64 `json:"maxMailboxes,omitempty"`
}

// CreateAccount creates a tenant account. SYSTEM-ONLY.
func (c *Client) CreateAccount(ctx context.Context, name string, maxMailboxes *int64) (*Account, error) {
	var out Account
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/accounts",
		body:        mustJSON(createAccountReq{Name: name, MaxMailboxes: maxMailboxes}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateAccount applies a partial patch (name, maxMailboxes, sendDisabled).
//
// SYSTEM-ONLY in full — every field on it is an operator control over a tenant,
// so unlike the mailbox patch there is no per-field guard: an account able to
// clear its own freeze does not have one.
//
// The patch is a map for the same reason UpdateMailbox's is: an explicit null
// maxMailboxes (clear the cap) must stay distinguishable from an omitted one
// (leave unchanged), and a struct with `omitempty` collapses both to "absent".
func (c *Client) UpdateAccount(ctx context.Context, accountID string, patch map[string]any) (*Account, error) {
	var out Account
	err := c.doJSON(ctx, request{
		method:      http.MethodPatch,
		path:        "/accounts/" + escapeSegment(accountID),
		body:        mustJSON(patch),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AccountTraffic is GET /accounts/:accountId/traffic — the same sampled
// aggregates as DomainTraffic, summed across every domain the account owns.
//
// The view that makes cross-mailbox abuse visible: per-domain and per-mailbox
// surfaces cannot see a tenant spreading volume, where fifty mailboxes each
// under their cap look healthy fifty times over. DomainsTruncated reports that
// the account holds more domains than one query covers — a silently partial
// total reads as reassurance.
type AccountTraffic struct {
	AccountID string `json:"accountId"`
	// MailboxID is present only when the aggregate was NARROWED to one mailbox;
	// absent means the figures cover every domain the account owns. Optional in
	// the wire contract, hence omitempty rather than a pointer: "" and absent
	// mean the same thing here, which is not true of the nullable fields above.
	MailboxID        string   `json:"mailboxId,omitempty"`
	Range            string   `json:"range"`
	Domains          []string `json:"domains"`
	DomainsTruncated bool     `json:"domainsTruncated"`
	Totals           struct {
		Events int64 `json:"events"`
		Bytes  int64 `json:"bytes"`
	} `json:"totals"`
	ByOutcome map[string]int64 `json:"byOutcome"`
	Rows      []TrafficRow     `json:"rows"`
	// Same time-bucketed series as DomainTraffic, over every domain the account
	// owns (see TrafficSeriesPoint).
	Series        []TrafficSeriesPoint `json:"series,omitempty"`
	Estimated     bool                 `json:"estimated"`
	RetentionDays int                  `json:"retentionDays"`
}

// GetAccountTraffic returns the account-wide rollup. rng is one of
// 1h|6h|24h|7d|30d (empty → core default 24h).
func (c *Client) GetAccountTraffic(ctx context.Context, accountID, rng string) (*AccountTraffic, error) {
	q := url.Values{}
	if rng != "" {
		q.Set("range", rng)
	}
	var out AccountTraffic
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/accounts/" + escapeSegment(accountID) + "/traffic",
		query:      q,
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAccountSendUsage returns the account-tier allowance window: both ledgers
// (sends and mailbox creates) plus the freeze, in one call.
func (c *Client) GetAccountSendUsage(ctx context.Context, accountID string) (*AccountSendUsage, error) {
	var out AccountSendUsage
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/accounts/" + escapeSegment(accountID) + "/send-usage",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
