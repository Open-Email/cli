package coreapi

import (
	"context"
	"net/http"
)

// Mailbox sharing (core README §"Shared mailboxes", docs/mail-sharing-design.md).
//
// Two shapes over one table, and the split is core's rather than this client's:
// MailShare is the OWNER's view of a grant (whom did I give access to — carries
// the grantee's address) and SharedMailbox is the GRANTEE's view (what do I have
// access to — carries the owner's address). Each names the party the caller does
// not already know, so a merged shape would carry one always-null column.

// MailShare is one grant on a mailbox, as its owner sees it.
type MailShare struct {
	MailboxID         string `json:"mailboxId"`
	GranteeIdentityID string `json:"granteeIdentityId"`
	// GranteeAddress is null for an address-less identity (a calendar-only
	// user), which is a real state — a mailbox is a ULID-identified store and
	// an address is optional. Print the ULID rather than dropping the row.
	GranteeAddress *string `json:"granteeAddress"`
	Rights         string  `json:"rights"`
	// LabelScope names FOLDERS, or is null for the whole mailbox. Names, not
	// ids: the grant follows a RENAME and does not follow the name, and a
	// scoped folder that was deleted is simply absent from the answer.
	LabelScope []string `json:"labelScope"`
	CreatedAt  int64    `json:"createdAt"`
}

// SharedMailbox is one mailbox somebody granted the caller access to.
type SharedMailbox struct {
	MailboxID         string   `json:"mailboxId"`
	GranteeIdentityID string   `json:"granteeIdentityId"`
	OwnerAddress      *string  `json:"ownerAddress"`
	Rights            string   `json:"rights"`
	LabelScope        []string `json:"labelScope"`
	CreatedAt         int64    `json:"createdAt"`
}

// MailShareScope selects which of core's THREE requests to make about a grant's
// folders. They are genuinely three, and two of them are easy to conflate:
//
//   - ScopePreserve OMITS labelScope. Core reads that as "I am not saying
//     anything about the scope" and KEEPS whatever folders the grant already
//     had. This exists because IMAP SETACL cannot express a folder scope at all,
//     and a protocol that cannot say "scope" must not be able to destroy one.
//   - ScopeWholeMailbox sends an explicit JSON null, which is the only thing
//     that WIDENS a scoped grant back to the whole store.
//   - ScopeFolders sends the named folders. An empty list is refused by core
//     (empty_label_scope) rather than read as "no restriction".
//
// Modelling this as a *[]string with nil meaning "whole mailbox" was wrong in
// exactly the dangerous direction: it omitted the field, so re-granting a
// folder-scoped colleague "the whole mailbox" silently left them scoped.
type MailShareScope int

const (
	// ScopePreserve leaves the grant's folders exactly as they are.
	ScopePreserve MailShareScope = iota
	// ScopeWholeMailbox widens the grant to the entire mailbox.
	ScopeWholeMailbox
	// ScopeFolders confines the grant to Folders.
	ScopeFolders
)

// MailShareInput is a grant to write.
type MailShareInput struct {
	Rights  string
	Scope   MailShareScope
	Folders []string
}

// ListMailShares lists the grants issued ON a mailbox (owner, its account, or a
// system principal only — a grantee cannot enumerate or widen a grant, whatever
// rights it holds).
func (c *Client) ListMailShares(ctx context.Context, mailboxID string) ([]MailShare, error) {
	var out struct {
		Shares []MailShare `json:"shares"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/mailboxes/" + escapeSegment(mailboxID) + "/shares",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Shares, nil
}

// PutMailShare grants — or re-grants, which is how a grant is widened or
// narrowed — an identity's access to a mailbox.
//
// A FOLDER-SCOPED grant may carry up to `lrswit` — read, the viewer's own
// read/unread marks, flags, filing in, and moving to trash. Core answers
// `rights_not_allowed_on_folder_share` for `e` (permanent delete) and `a`
// (folder management), so `full` is not available with a scope. That is not
// worth pre-empting here: the server names the offending letters, which this
// client cannot.
func (c *Client) PutMailShare(ctx context.Context, mailboxID, granteeIdentityID string, in MailShareInput) (*MailShare, error) {
	// A map, not a struct, for the reason UpdateMailbox uses one: an omitted key
	// and an explicit null are different requests here, and `omitempty` cannot
	// express the difference.
	body := map[string]any{"rights": in.Rights}
	switch in.Scope {
	case ScopeWholeMailbox:
		body["labelScope"] = nil
	case ScopeFolders:
		body["labelScope"] = in.Folders
	case ScopePreserve:
		// Deliberately absent.
	}
	var out MailShare
	err := c.doJSON(ctx, request{
		method:      http.MethodPut,
		path:        "/mailboxes/" + escapeSegment(mailboxID) + "/shares/" + escapeSegment(granteeIdentityID),
		body:        mustJSON(body),
		contentType: "application/json",
		idempotent:  true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMailShare revokes a grant. Effective immediately — core re-reads rights
// on every request, so there is no cache to wait out.
func (c *Client) DeleteMailShare(ctx context.Context, mailboxID, granteeIdentityID string) error {
	return c.doJSON(ctx, request{
		method: http.MethodDelete,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/shares/" + escapeSegment(granteeIdentityID),
	}, nil)
}

// ListSharedMailboxes lists the mailboxes shared WITH the caller. Top-level and
// keyed on the principal, so it takes no mailbox argument.
func (c *Client) ListSharedMailboxes(ctx context.Context) ([]SharedMailbox, error) {
	var out struct {
		SharedMailboxes []SharedMailbox `json:"sharedMailboxes"`
	}
	err := c.doJSON(ctx, request{
		method:     http.MethodGet,
		path:       "/shared-mailboxes",
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.SharedMailboxes, nil
}
