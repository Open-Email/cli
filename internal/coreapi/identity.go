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

// Principal is what the current token resolves to. AccountID is set for
// account principals; MailboxID and CredentialID are known only when core's
// /auth/whoami answered (older deployments cannot introspect a mailbox
// principal's own mailbox id).
type Principal struct {
	Type         string
	AccountID    string
	MailboxID    string
	CredentialID string
	KeyID        string
}

// Resolve classifies the current bearer. It asks /auth/whoami first — the
// exact, single-call answer — and falls back to probing whenever whoami cannot
// answer (a deployment predating the endpoint, or any non-401 refusal):
// /api-keys distinguishes mailbox tokens (403), /accounts distinguishes system
// from account keys, and the api-keys page reveals the tenant id. Either way a
// bad/revoked key surfaces its 401.
func (c *Client) Resolve(ctx context.Context) (Principal, error) {
	if w, werr := c.Whoami(ctx); werr == nil && w.Type != "" {
		return Principal{
			Type:         w.Type,
			AccountID:    derefStr(w.AccountID),
			MailboxID:    derefStr(w.MailboxID),
			CredentialID: derefStr(w.CredentialID),
			KeyID:        derefStr(w.KeyID),
		}, nil
	} else if werr != nil && IsUnauthorized(werr) {
		// A bad/revoked key: the probes would only repeat the same 401.
		return Principal{}, werr
	}

	if strings.HasPrefix(c.cfg.Token, "oemp_") {
		// Fenced from /api-keys: a valid mailbox token yields 403
		// insufficient_scope, a bad one yields 401.
		_, err := c.ListAPIKeys(ctx, 1, "")
		if err == nil || IsInsufficientScope(err) || IsForbidden(err) {
			return Principal{Type: PrincipalMailbox}, nil
		}
		return Principal{}, err
	}

	page, err := c.ListAPIKeys(ctx, 25, "")
	if err != nil {
		if IsInsufficientScope(err) || IsForbidden(err) {
			// A mailbox token lacking the oemp_ prefix — unusual, but classify it.
			return Principal{Type: PrincipalMailbox}, nil
		}
		return Principal{}, err
	}

	// Only a system principal may list accounts; an account principal is refused.
	if _, aerr := c.ListAccounts(ctx, 1, ""); aerr == nil {
		return Principal{Type: PrincipalSystem}, nil
	} else if !IsForbidden(aerr) && !IsSystemCredentialsRequired(aerr) {
		return Principal{}, aerr
	}

	acct := ""
	for _, k := range page.Items {
		if k.AccountID != nil && *k.AccountID != "" {
			acct = *k.AccountID
			break
		}
	}
	return Principal{Type: PrincipalAccount, AccountID: acct}, nil
}
