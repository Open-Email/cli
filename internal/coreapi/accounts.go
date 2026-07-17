package coreapi

import (
	"context"
	"net/http"
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
