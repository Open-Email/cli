package coreapi

// Wire types mirror openemail-core's JSON responses exactly (camelCase; epoch
// SECONDS for timestamps; byte/count fields int64; nullable fields are pointers
// so null is distinguishable from zero).

// APIKey is one row of GET /api-keys (tokens are never returned by list).
type APIKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	AccountID   *string `json:"accountId"`
	AccountName *string `json:"accountName"` // owning account's name; null for system keys
	CreatedAt   int64   `json:"createdAt"`
	LastUsedAt  *int64  `json:"lastUsedAt"`
	RevokedAt   *int64  `json:"revokedAt"`
}

// CreatedAPIKey is POST /api-keys — the only place the plaintext token appears.
type CreatedAPIKey struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	AccountID *string `json:"accountId"`
	Token     string  `json:"token"`
}

// Account is an accounts row.
type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MaxMailboxes *int64 `json:"maxMailboxes"`
	CreatedAt    int64  `json:"createdAt"`
}

// Mailbox is a live mailbox row; the stats fields are populated only by
// GET /mailboxes/:id (MailboxGet), absent from list rows.
type Mailbox struct {
	ID             string  `json:"id"`
	PrimaryAddress *string `json:"primaryAddress"`
	QuotaBytes     *int64  `json:"quotaBytes"`
	AccountID      *string `json:"accountId"`
	CreatedAt      int64   `json:"createdAt"`

	MessageCount  *int64 `json:"messageCount,omitempty"`
	UsedBytes     *int64 `json:"usedBytes,omitempty"`
	ExpungedCount *int64 `json:"expungedCount,omitempty"`
	ExpungedBytes *int64 `json:"expungedBytes,omitempty"`
}

// DeletedMailbox is a restorable tombstone (GET /mailboxes?state=deleted).
type DeletedMailbox struct {
	ID              string  `json:"id"`
	AccountID       *string `json:"accountId"`
	PrimaryAddress  *string `json:"primaryAddress"`
	QuotaBytes      *int64  `json:"quotaBytes"`
	DeletedAt       int64   `json:"deletedAt"`
	Restorable      bool    `json:"restorable"`
	RestorableUntil *int64  `json:"restorableUntil,omitempty"`
}

// MailboxCreateInput is the POST /mailboxes body (all fields optional; accountId
// is honored only for system callers).
type MailboxCreateInput struct {
	PrimaryAddress *string `json:"primaryAddress,omitempty"`
	QuotaBytes     *int64  `json:"quotaBytes,omitempty"`
	AccountID      *string `json:"accountId,omitempty"`
}

// MailboxDeleteResult is the DELETE /mailboxes/:id body.
type MailboxDeleteResult struct {
	Deleted         bool   `json:"deleted"`
	Purged          bool   `json:"purged,omitempty"`
	Restorable      bool   `json:"restorable"`
	RestorableUntil *int64 `json:"restorableUntil,omitempty"`
}

// MailboxPurgeResult is the POST /mailboxes/:id/purge body. Restorable is
// always false (the tombstone is now non-restorable).
type MailboxPurgeResult struct {
	Purged     bool `json:"purged"`
	Restorable bool `json:"restorable"`
}

// MailboxRestoreResult is the POST /mailboxes/:id/restore body.
type MailboxRestoreResult struct {
	Restored       bool    `json:"restored"`
	ID             string  `json:"id"`
	AccountID      *string `json:"accountId"`
	QuotaBytes     *int64  `json:"quotaBytes"`
	PrimaryAddress *string `json:"primaryAddress"`
	AddressChanged bool    `json:"addressChanged"`
}
