package coreapi

import (
	"context"
	"strings"
)

// Principal types, mirroring core's src/types.ts Principal union.
const (
	PrincipalSystem  = "system"
	PrincipalAccount = "account"
	PrincipalMailbox = "mailbox"
	PrincipalUnknown = "unknown"
)

// Identity is what the current token resolves to. AccountID is set for account
// principals (core has no whoami endpoint, so it is inferred from the api-keys
// listing, which an account principal sees filtered to its own tenant).
type Identity struct {
	Type      string
	AccountID string
}

// Resolve classifies the current bearer by probing core. It validates the token
// (a bad/revoked key surfaces its 401) and, for account keys, learns the tenant
// id. Mailbox app passwords (oemp_) can only be confirmed, not introspected —
// core exposes no route that reveals a mailbox principal's own mailbox id.
func (c *Client) Resolve(ctx context.Context) (Identity, error) {
	if strings.HasPrefix(c.cfg.Token, "oemp_") {
		// Fenced from /api-keys: a valid mailbox token yields 403
		// insufficient_scope, a bad one yields 401.
		_, err := c.ListAPIKeys(ctx, 1, "")
		if err == nil || IsInsufficientScope(err) || IsForbidden(err) {
			return Identity{Type: PrincipalMailbox}, nil
		}
		return Identity{}, err
	}

	page, err := c.ListAPIKeys(ctx, 25, "")
	if err != nil {
		if IsInsufficientScope(err) || IsForbidden(err) {
			// A mailbox token lacking the oemp_ prefix — unusual, but classify it.
			return Identity{Type: PrincipalMailbox}, nil
		}
		return Identity{}, err
	}

	// Only a system principal may list accounts; an account principal is refused.
	if _, aerr := c.ListAccounts(ctx, 1, ""); aerr == nil {
		return Identity{Type: PrincipalSystem}, nil
	} else if !IsForbidden(aerr) && !IsSystemCredentialsRequired(aerr) {
		return Identity{}, aerr
	}

	acct := ""
	for _, k := range page.Items {
		if k.AccountID != nil && *k.AccountID != "" {
			acct = *k.AccountID
			break
		}
	}
	return Identity{Type: PrincipalAccount, AccountID: acct}, nil
}
