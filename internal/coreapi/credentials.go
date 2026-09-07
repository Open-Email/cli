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
	// ExpiresAt is unix seconds; nil = never expires.
	ExpiresAt *int64 `json:"expiresAt"`
	// Scope is the credential's RUNG (core migration 0077, design D50): a
	// ladder — "read" < "write" < "send" < "full". What this credential may do
	// of what its mailbox may do; a call above the rung answers 403
	// credential_scope naming the rung it needs. Modelled here so the wire
	// shape is complete; the CLI does not yet mint below "full".
	Scope string `json:"scope"`
	// PermittedRecipients is the credential's OWN outbound allowlist (exact
	// addresses or `*@domain`), intersected with the identity's — BOTH must
	// permit. Null means it carries none of its own, which is not the same as
	// an empty list (nobody), so this stays a nil-able slice.
	PermittedRecipients []string `json:"permittedRecipients"`
}

// CreatedCredential is the create response; Token is the one-time plaintext,
// present only for kind=app_password.
type CreatedCredential struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Username string  `json:"username"`
	Name     *string `json:"name"`
	Token    string  `json:"token,omitempty"`
	// ExpiresAt is unix seconds; nil = never expires. app_password arm only —
	// the password arm of the union carries no expiry.
	ExpiresAt *int64 `json:"expiresAt,omitempty"`
	// Scope is the credential's RUNG (core migration 0077, design D50): a
	// ladder — "read" < "write" < "send" < "full". What this credential may do
	// of what its mailbox may do; a call above the rung answers 403
	// credential_scope naming the rung it needs. Modelled here so the wire
	// shape is complete; the CLI does not yet mint below "full".
	Scope string `json:"scope"`
	// PermittedRecipients is the credential's OWN outbound allowlist (exact
	// addresses or `*@domain`), intersected with the identity's — BOTH must
	// permit. Null means it carries none of its own, which is not the same as
	// an empty list (nobody), so this stays a nil-able slice.
	PermittedRecipients []string `json:"permittedRecipients"`
}

// CredentialCreateInput is the POST body. Kind is "password" (requires Password)
// or "app_password" (generated). Username defaults to the mailbox primary
// address; an @-free username is allowed and skips the address-ownership check —
// how a mail-less (calendar-only) identity gets a login. An address-shaped
// username must route to this mailbox.
type CredentialCreateInput struct {
	Kind     string  `json:"kind"`
	Username string  `json:"username,omitempty"`
	Password string  `json:"password,omitempty"`
	Name     *string `json:"name,omitempty"`
	// ExpiresInSeconds bounds an app_password's lifetime (60..31536000);
	// 0 = never expires. The password kind carries no expiry.
	ExpiresInSeconds int64 `json:"expiresInSeconds,omitempty"`
}

func (c *Client) credentialsPath(mailboxID string) string {
	return "/identities/" + escapeSegment(mailboxID) + "/credentials"
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
