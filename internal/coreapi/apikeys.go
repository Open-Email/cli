package coreapi

import (
	"context"
	"net/http"
)

type createKeyReq struct {
	Name      string `json:"name"`
	Role      string `json:"role,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

// CreateAPIKey mints an API key. role/accountID may be empty: an account
// principal always mints an account key for its own account (core forces this
// and rejects role="system" with 403 system_credentials_required). The plaintext
// Token in the result is shown exactly once.
func (c *Client) CreateAPIKey(ctx context.Context, name, role, accountID string) (*CreatedAPIKey, error) {
	var out CreatedAPIKey
	err := c.doJSON(ctx, request{
		method:      http.MethodPost,
		path:        "/api-keys",
		body:        mustJSON(createKeyReq{Name: name, Role: role, AccountID: accountID}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAPIKeys returns one page of API keys, newest-first (account callers see
// only their own).
func (c *Client) ListAPIKeys(ctx context.Context, limit int, cursor string) (Page[APIKey], error) {
	var out struct {
		APIKeys    []APIKey `json:"apiKeys"`
		NextCursor string   `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/api-keys",
		query:      pageValues(limit, cursor),
		idempotent: true,
	}, &out)
	if err != nil {
		return Page[APIKey]{}, err
	}
	return Page[APIKey]{Items: out.APIKeys, NextCursor: out.NextCursor}, nil
}

// RevokeAPIKey revokes a key (account callers only their own). Missing/foreign
// keys answer 404.
func (c *Client) RevokeAPIKey(ctx context.Context, apiKeyID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/api-keys/" + escapeSegment(apiKeyID),
	}, nil)
}
