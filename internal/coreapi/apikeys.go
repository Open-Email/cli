package coreapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type createKeyReq struct {
	Name      string   `json:"name"`
	Role      string   `json:"role,omitempty"`
	AccountID string   `json:"accountId,omitempty"`
	Domains   []string `json:"domains,omitempty"`
}

// CreateKeyOptions is one mint. It is a struct rather than more positional
// arguments because `role`/`accountID` were already two empty strings at most
// call sites, and `Domains` is the kind of field that is wrong to pass
// positionally: a caller that meant "unscoped" and a caller that forgot the
// argument would look identical.
type CreateKeyOptions struct {
	Name string
	// Role and AccountID are for SYSTEM callers. An account principal always
	// mints an account key for its own account (core forces this and rejects
	// role="system" with 403 system_credentials_required).
	Role      string
	AccountID string
	// Domains is the DOMAIN SCOPE (core migration 0078): the key reaches only
	// these domains' directory and the mailboxes with an address on one of
	// them. Empty means unscoped, which is the whole account — so this is
	// omitted from the request rather than sent as `[]`, which core refuses.
	//
	// A scope NARROWS and never grants: it cannot reach past the account the
	// key already belongs to, and a system key may not carry one at all
	// (400 system_key_unscopable).
	Domains []string
}

// ScopeNotRecordedError is a mint that asked for a domain scope and was handed
// a key WITHOUT one — a key over the whole account. CreateAPIKey has already
// revoked it (RevokeErr is nil), or tried to and could not (RevokeErr says
// why), in which case KeyID names the key a human now has to revoke by hand.
type ScopeNotRecordedError struct {
	KeyID     string
	RevokeErr error
}

func (e *ScopeNotRecordedError) Error() string {
	if e.RevokeErr != nil {
		return fmt.Sprintf("core minted key %s UNSCOPED (it does not support a domain scope) and revoking it failed: %v — revoke %s by hand", e.KeyID, e.RevokeErr, e.KeyID)
	}
	return "core minted the key unscoped: it does not support a domain scope, so the key was revoked"
}

func (e *ScopeNotRecordedError) Unwrap() error { return e.RevokeErr }

// CreateAPIKey mints an API key. The plaintext Token in the result is shown
// exactly once.
//
// THE ECHO CHECK lives here, in the client, and it undoes the mint rather than
// warn about it. Core's request schema strips unknown keys, so a core that
// predates migration 0078 accepts `domains`, ignores it, and answers 200 with
// an UNSCOPED key — a silent privilege widening that nothing later in the
// key's life will notice: there is no backfill and no scope to read back. A
// caller that asked for a scope and would have been handed a key without one
// gets a ScopeNotRecordedError instead, and the key is revoked before it
// returns. It is enforced here rather than at each call site so that every
// minter — the flag command and the TUI form alike — fails closed without
// having to remember to.
func (c *Client) CreateAPIKey(ctx context.Context, opts CreateKeyOptions) (*CreatedAPIKey, error) {
	var out CreatedAPIKey
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/api-keys",
		body: mustJSON(createKeyReq{
			Name:      opts.Name,
			Role:      opts.Role,
			AccountID: opts.AccountID,
			Domains:   opts.Domains,
		}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(opts.Domains) > 0 && len(out.Domains) == 0 {
		return nil, &ScopeNotRecordedError{KeyID: out.ID, RevokeErr: c.RevokeAPIKey(ctx, out.ID)}
	}
	return &out, nil
}

// ScopeLabel is the key's domain scope as one table cell.
//
// Three cases, because the wire has three. nil — the field absent — is an
// UNSCOPED key, and reads "all" rather than a dash because the absence is the
// meaning: that key reaches the whole account, the widest a key gets, not a
// missing value. An EMPTY list is a stored scope core could not read (a
// hand-edited row, a build skew): core refuses to authenticate such a key and
// its listing says so with `[]`, rendered "none" and never folded into "all",
// since a scope shown wider than it is would be the one misreading with
// consequences. Otherwise the domains, truncated with a count past `shown`:
// a table is for telling rows apart, and the full list is one detail view or
// `--json` away.
func (k APIKey) ScopeLabel(shown int) string {
	switch {
	case k.Domains == nil:
		return "all"
	case len(k.Domains) == 0:
		return "none"
	case len(k.Domains) <= shown:
		return strings.Join(k.Domains, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(k.Domains[:shown], ", "), len(k.Domains)-shown)
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
