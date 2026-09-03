package coreapi

import (
	"context"
	"net/http"
)

// Identity is GET /identities/:id — the durable identity (keyed by the same
// ULID as the mailbox API) plus its bound store facets. Since core's binding
// model (identity-design §IX) an identity may exist without a mail store: a
// calendar-only identity has no "mail" facet and cannot speak the mail
// protocols, yet logs in, holds credentials, and owns a PIM store.
type Identity struct {
	ID             string  `json:"id"`
	PrimaryAddress *string `json:"primaryAddress"`
	QuotaBytes     *int64  `json:"quotaBytes"`
	AccountID      *string `json:"accountId"`
	CreatedAt      int64   `json:"createdAt"`
	// Per-mailbox send policy (always present; the Identity component composes
	// the Mailbox schema).
	// Pointers for the reason Mailbox's copies are: null = the platform default
	// applies, 0 = explicitly unlimited.
	SendHold         *string `json:"sendHold"`
	SendMsgsPerDay   *int64  `json:"sendMsgsPerDay"`
	SendRcptsPerDay  *int64  `json:"sendRcptsPerDay"`
	SendMsgsPerHour  *int64  `json:"sendMsgsPerHour"`
	SendRcptsPerHour *int64  `json:"sendRcptsPerHour"`
	// RetentionDays is the identity's OWN age window (nil = the account default
	// applies, if any) — see Mailbox.RetentionDays.
	RetentionDays *int64 `json:"retentionDays"`
	// GroupAddress — see Mailbox.GroupAddress: non-nil iff this identity is a
	// group's persona.
	GroupAddress *string        `json:"groupAddress"`
	Facets       IdentityFacets `json:"facets"`
}

// IdentityFacets maps each bound store to its usage. A key is present iff a
// binding row exists ("teardown must reach this store"); Provisioned reports
// whether the store was actually claimed. Both keys may be absent.
type IdentityFacets struct {
	Mail *IdentityMailFacet `json:"mail,omitempty"`
	Pim  *IdentityPimFacet  `json:"pim,omitempty"`
}

// IdentityMailFacet is the mail store's usage (live + trash tiers).
type IdentityMailFacet struct {
	StoreID       string `json:"storeId"`
	Provisioned   bool   `json:"provisioned"`
	MessageCount  int64  `json:"messageCount"`
	UsedBytes     int64  `json:"usedBytes"`
	ExpungedCount int64  `json:"expungedCount"`
	ExpungedBytes int64  `json:"expungedBytes"`
}

// IdentityPimFacet is the PIM store's usage (calendars + addressbooks).
type IdentityPimFacet struct {
	StoreID     string `json:"storeId"`
	Provisioned bool   `json:"provisioned"`
	Collections int64  `json:"collections"`
	Objects     int64  `json:"objects"`
	Bytes       int64  `json:"bytes"`
}

// GetIdentity fetches an identity with per-facet usage. Non-adopting: reading
// never provisions a store. A mailbox principal may read its own (by the id
// /auth/whoami reports); account/system keys read identities they own; missing
// or foreign is 404.
func (c *Client) GetIdentity(ctx context.Context, identityID string) (*Identity, error) {
	var out Identity
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/identities/" + escapeSegment(identityID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// WhoamiResult is GET /auth/whoami — the exact scope of the current bearer,
// answered from the token alone (no extra lookups).
type WhoamiResult struct {
	Type         string  `json:"type"` // system | account | mailbox
	KeyID        *string `json:"keyId"`
	AccountID    *string `json:"accountId"`
	MailboxID    *string `json:"mailboxId"`
	CredentialID *string `json:"credentialId"`
	// Kind is display metadata core records at mint ("cli" for a key the browser
	// login produced), or nil. Never an authorization signal — the minting
	// caller chooses it.
	Kind *string `json:"kind"`
	// IdleExpiresAt is when this credential stops working from disuse (epoch
	// seconds), or nil when it never does. Present so the CLI can warn while the
	// key still works, rather than letting a 401 be the first news of it.
	IdleExpiresAt *int64 `json:"idleExpiresAt"`
}

// Whoami introspects the current bearer.
func (c *Client) Whoami(ctx context.Context) (*WhoamiResult, error) {
	var out WhoamiResult
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/auth/whoami", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
