package coreapi

import (
	"context"
	"net/http"
)

// Credential is a mailbox credential (secrets never returned).
type Credential struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"` // password | app_password
	Username   string  `json:"username"`
	Name       *string `json:"name"`
	CreatedAt  int64   `json:"createdAt"`
	LastUsedAt *int64  `json:"lastUsedAt"`
	RevokedAt  *int64  `json:"revokedAt"`
}

// CreatedCredential is the create response; Token is the one-time plaintext,
// present only for kind=app_password.
type CreatedCredential struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Username string  `json:"username"`
	Name     *string `json:"name"`
	Token    string  `json:"token,omitempty"`
}

// CredentialCreateInput is the POST body. Kind is "password" (requires Password)
// or "app_password" (generated). Username defaults to the mailbox primary address.
type CredentialCreateInput struct {
	Kind     string  `json:"kind"`
	Username string  `json:"username,omitempty"`
	Password string  `json:"password,omitempty"`
	Name     *string `json:"name,omitempty"`
}

func (c *Client) credentialsPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/credentials"
}

// CreateCredential creates a password or app_password credential. All management
// is admin-gated (account owner or system).
func (c *Client) CreateCredential(ctx context.Context, mailboxID string, in CredentialCreateInput) (*CreatedCredential, error) {
	var out CreatedCredential
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.credentialsPath(mailboxID),
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListCredentials lists a mailbox's credentials (unpaginated; no secrets).
func (c *Client) ListCredentials(ctx context.Context, mailboxID string) ([]Credential, error) {
	var out struct {
		Credentials []Credential `json:"credentials"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.credentialsPath(mailboxID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Credentials, nil
}

// RevokeCredential revokes a credential.
func (c *Client) RevokeCredential(ctx context.Context, mailboxID, credentialID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   c.credentialsPath(mailboxID) + "/" + escapeSegment(credentialID),
	}, nil)
}
