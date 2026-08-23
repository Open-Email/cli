package coreapi

import (
	"context"
	"fmt"
)

// Principal types, mirroring core's src/types.ts Principal union.
const (
	PrincipalSystem  = "system"
	PrincipalAccount = "account"
	PrincipalMailbox = "mailbox"
)

// Principal is what the current token resolves to. AccountID is set for
// account principals; MailboxID and CredentialID for mailbox ones. Every field
// comes straight from core's /auth/whoami.
type Principal struct {
	Type         string
	AccountID    string
	MailboxID    string
	CredentialID string
	KeyID        string
	// Kind is what core says this key IS ("cli" for one the browser login
	// minted), empty when it says nothing. Display only.
	Kind string
	// IdleExpiresAt is when the credential lapses from disuse, epoch seconds;
	// zero when it never does (or when core predates the field).
	IdleExpiresAt int64
}

// Resolve classifies the current bearer from core's /auth/whoami — the exact,
// single-call answer. Any failure is surfaced to the caller rather than
// degraded into a guess: a 401 is a bad or revoked key, and anything else means
// core could not be asked, which must not be reported as a principal.
func (c *Client) Resolve(ctx context.Context) (Principal, error) {
	w, err := c.Whoami(ctx)
	if err != nil {
		return Principal{}, err
	}
	if w.Type == "" {
		return Principal{}, fmt.Errorf("coreapi: /auth/whoami returned no principal type")
	}
	p := Principal{
		Type:         w.Type,
		AccountID:    derefStr(w.AccountID),
		MailboxID:    derefStr(w.MailboxID),
		CredentialID: derefStr(w.CredentialID),
		KeyID:        derefStr(w.KeyID),
		Kind:         derefStr(w.Kind),
	}
	if w.IdleExpiresAt != nil {
		p.IdleExpiresAt = *w.IdleExpiresAt
	}
	return p, nil
}
