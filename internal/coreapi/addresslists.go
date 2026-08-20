package coreapi

import (
	"context"
	"net/http"
)

// Address lists are the tenant's own allow/block policy in BOTH directions
// (core migration 0040): a NAMED list carrying a scope (account | domain |
// mailbox), a direction (inbound | outbound) and a verdict (allow | block),
// holding address patterns.
//
// They replaced the per-account do-not-send table (core 0033), which was
// always an outbound `block` list at account scope; core's migration copies
// those rows into a seeded list per account and the old
// /accounts/:id/suppressions endpoints are gone. The deployment-global
// `suppressions` list next door is a different thing and stays: it is EVIDENCE
// the platform gathered (complaints, hard bounces), operator-managed, and it
// wins every tie at the relay.
//
// Two path families answer these, over one implementation in core:
// /accounts/{id}/lists… (an account key, any scope inside the account) and
// /mailboxes/{id}/lists… (that mailbox, pinned to its own scope). This client
// takes the base path so both are one set of methods — see AccountLists and
// MailboxLists.
type AddressList struct {
	ID        string `json:"id"`
	ScopeKind string `json:"scopeKind"` // account | domain | mailbox
	ScopeID   string `json:"scopeId"`
	Name      string `json:"name"`
	Direction string `json:"direction"` // inbound | outbound
	Verdict   string `json:"verdict"`   // allow | block
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// EntryCount is present on a single-list read only — a listing deliberately
	// does not count per row.
	EntryCount *int64 `json:"entryCount,omitempty"`
}

// AddressListEntry is one pattern. Patterns are NORMALIZED by core at write
// (trimmed, lowercased, `@x` stored as `*@x`), so what comes back may not be
// the spelling that went in — and a delete accepts either.
type AddressListEntry struct {
	Pattern   string  `json:"pattern"`
	Note      *string `json:"note"`
	CreatedAt int64   `json:"createdAt"`
}

// AddressListBatchResult reports an import: what was written, and what core
// refused as unparseable rather than failing the whole batch over.
type AddressListBatchResult struct {
	Added   int64    `json:"added"`
	Invalid []string `json:"invalid"`
}

// AddressListVerdict is what the `evaluate` verb answers — the ONE read that
// explains a refusal, naming the list and pattern that decided. Verdict is
// "none" when no list has an opinion, which is a real answer and the common
// one.
type AddressListVerdict struct {
	Verdict   string `json:"verdict"` // allow | block | none
	ListID    string `json:"listId,omitempty"`
	ScopeKind string `json:"scopeKind,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
}

// AddressListCreate is the create body. ScopeKind/ScopeID are required on the
// account family and ignored on the mailbox family (the path pins them).
type AddressListCreate struct {
	Name      string `json:"name"`
	ScopeKind string `json:"scopeKind,omitempty"`
	ScopeID   string `json:"scopeId,omitempty"`
	Direction string `json:"direction"`
	Verdict   string `json:"verdict"`
}

// AddressListEntryInput is one pattern going in, single or batched.
type AddressListEntryInput struct {
	Pattern string `json:"pattern"`
	Note    string `json:"note,omitempty"`
}

// AddressListEvaluateInput asks what the lists decide for one address. Domain
// and MailboxID narrow the scope chain, and a caller may only name scopes it
// owns.
type AddressListEvaluateInput struct {
	Direction string `json:"direction"`
	Address   string `json:"address"`
	Domain    string `json:"domain,omitempty"`
	MailboxID string `json:"mailboxId,omitempty"`
}

// AddressLists is the base path of one family. Build it with AccountLists or
// MailboxLists rather than by hand, so the escaping stays in one place.
type AddressLists struct {
	base string
}

// AccountLists addresses an account's lists — any scope inside the account.
func AccountLists(accountID string) AddressLists {
	return AddressLists{base: "/accounts/" + escapeSegment(accountID) + "/lists"}
}

// MailboxLists addresses one mailbox's own lists. The path pins the scope, so
// a mailbox credential can curate its senders without being able to name
// anyone else's.
func MailboxLists(mailboxID string) AddressLists {
	return AddressLists{base: "/mailboxes/" + escapeSegment(mailboxID) + "/lists"}
}

// ListAddressListsFilter narrows a listing. Empty fields are omitted.
type ListAddressListsFilter struct {
	ScopeKind string
	ScopeID   string
	Direction string
	Verdict   string
}

// ListAddressLists returns one page of lists.
func (c *Client) ListAddressLists(ctx context.Context, fam AddressLists, f ListAddressListsFilter, limit int, cursor string) (Page[AddressList], error) {
	var out struct {
		Lists      []AddressList `json:"lists"`
		NextCursor string        `json:"nextCursor"`
	}
	q := pageValues(limit, cursor)
	for k, v := range map[string]string{
		"scopeKind": f.ScopeKind, "scopeId": f.ScopeID, "direction": f.Direction, "verdict": f.Verdict,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: fam.base, query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[AddressList]{}, err
	}
	return Page[AddressList]{Items: out.Lists, NextCursor: out.NextCursor}, nil
}

// GetAddressList reads one list, with its entry count.
func (c *Client) GetAddressList(ctx context.Context, fam AddressLists, listID string) (*AddressList, error) {
	var out AddressList
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: fam.base + "/" + escapeSegment(listID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateAddressList makes a list. 409 list_name_taken when the name is used at
// that scope; 409 list_limit_reached at 50 lists per scope.
func (c *Client) CreateAddressList(ctx context.Context, fam AddressLists, in AddressListCreate) (*AddressList, error) {
	var out AddressList
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: fam.base, body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameAddressList changes the label. Scope, direction and verdict are
// immutable in core — they would change what every existing entry MEANS.
func (c *Client) RenameAddressList(ctx context.Context, fam AddressLists, listID, name string) (*AddressList, error) {
	var out AddressList
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: fam.base + "/" + escapeSegment(listID),
		body: mustJSON(map[string]string{"name": name}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAddressList removes a list and every entry on it.
func (c *Client) DeleteAddressList(ctx context.Context, fam AddressLists, listID string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: fam.base + "/" + escapeSegment(listID),
	}, &out)
	return out.Deleted, err
}

// ListAddressListEntries returns one page of patterns, ordered by pattern.
func (c *Client) ListAddressListEntries(ctx context.Context, fam AddressLists, listID string, limit int, cursor string) (Page[AddressListEntry], error) {
	var out struct {
		Entries    []AddressListEntry `json:"entries"`
		NextCursor string             `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: fam.base + "/" + escapeSegment(listID) + "/entries",
		query: pageValues(limit, cursor), idempotent: true,
	}, &out)
	if err != nil {
		return Page[AddressListEntry]{}, err
	}
	return Page[AddressListEntry]{Items: out.Entries, NextCursor: out.NextCursor}, nil
}

// AddAddressListEntry puts one pattern on a list. Idempotent on the NORMALIZED
// pattern: a repeat refreshes the note and keeps the original CreatedAt.
func (c *Client) AddAddressListEntry(ctx context.Context, fam AddressLists, listID string, in AddressListEntryInput) (*AddressListEntry, error) {
	var out AddressListEntry
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: fam.base + "/" + escapeSegment(listID) + "/entries",
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AddAddressListEntries imports up to 500 patterns at once. Unparseable inputs
// come back in Invalid rather than failing the import.
func (c *Client) AddAddressListEntries(ctx context.Context, fam AddressLists, listID string, entries []AddressListEntryInput) (*AddressListBatchResult, error) {
	var out AddressListBatchResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: fam.base + "/" + escapeSegment(listID) + "/entries/batch",
		body:        mustJSON(map[string]any{"entries": entries}),
		contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveAddressListEntry takes one pattern off a list. Either spelling of a
// bare-domain pattern works — core normalizes before it looks.
func (c *Client) RemoveAddressListEntry(ctx context.Context, fam AddressLists, listID, pattern string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   fam.base + "/" + escapeSegment(listID) + "/entries/" + escapeSegment(pattern),
	}, &out)
	return out.Deleted, err
}

// EvaluateAddressLists answers what the lists decide for one address — the
// same evaluator the relay and the delivery funnel run.
func (c *Client) EvaluateAddressLists(ctx context.Context, fam AddressLists, in AddressListEvaluateInput) (*AddressListVerdict, error) {
	var out AddressListVerdict
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: fam.base + "/evaluate",
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
