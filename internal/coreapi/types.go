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
	// Managed marks an infrastructure-held key (e.g. the console's session
	// key). Account-scoped listings exclude them, so account callers only ever
	// see false.
	Managed bool `json:"managed"`
}

// CreatedAPIKey is POST /api-keys — the only place the plaintext token appears.
type CreatedAPIKey struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	AccountID *string `json:"accountId"`
	Token     string  `json:"token"`
	Managed   bool    `json:"managed"`
}

// Account is an accounts row.
type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MaxMailboxes *int64 `json:"maxMailboxes"`
	// VerificationToken is the value this account publishes in
	// `_openemail.<domain>` TXT to claim a domain. Per-account and stable, so it
	// is knowable before the first domain exists. Public by construction (it
	// lives in DNS) — a claim capability, never an authentication credential.
	VerificationToken *string `json:"verificationToken"`
	// SendDisabled is the TENANT-SCALE send freeze: true stops every mailbox on
	// every domain this account owns, at submission and in the already-queued
	// relay backlog. Egress only — a frozen account still receives its mail.
	// System-writable only.
	SendDisabled bool `json:"sendDisabled"`
	// SendPaused is the REVERSIBLE form of SendDisabled — a hold you intend to
	// lift (non-payment, an investigation). Submissions answer 429
	// sending_paused rather than 403, so SMTP submission gets a 451 and the
	// sending MTA queues the mail, and the already-queued relay backlog is
	// DEFERRED rather than bounced. SendDisabled wins if both are set. Egress
	// only; system-writable only.
	SendPaused bool `json:"sendPaused"`
	// Account-tier daily caps. NIL = the platform default is in force; 0 =
	// explicitly unlimited. Pointers because those are DIFFERENT states and
	// collapsing them inverts the meaning in the direction that removes a bound.
	SendMsgsPerDay  *int64 `json:"sendMsgsPerDay"`
	SendRcptsPerDay *int64 `json:"sendRcptsPerDay"`
	// StorageLimitBytes is the account storage pool: NIL = platform default,
	// 0 = explicitly unlimited/metered.
	StorageLimitBytes *int64 `json:"storageLimitBytes"`
	CreatedAt         int64  `json:"createdAt"`
}

// AccountSendUsage is GET /accounts/:accountId/send-usage — the tenant-scale
// allowance window: what the account has SENT, how many mailboxes it has
// MINTED, and whether it is frozen. The cross-mailbox reading a per-mailbox
// send-usage call cannot give.
type AccountSendUsage struct {
	AccountID string `json:"accountId"`
	Frozen    bool   `json:"frozen"`
	// Paused is the reversible half: refused temporarily (429) rather than
	// permanently. Reported apart from Frozen because the two are different
	// answers for a support agent — "we stopped them" vs "we are holding them".
	Paused bool `json:"paused"`
	Send   struct {
		Messages    int64  `json:"messages"`
		Recipients  int64  `json:"recipients"`
		MsgsPerDay  *int64 `json:"msgsPerDay"`
		RcptsPerDay *int64 `json:"rcptsPerDay"`
	} `json:"send"`
	Creates struct {
		Mailboxes int64  `json:"mailboxes"`
		PerDay    *int64 `json:"perDay"`
	} `json:"creates"`
	WindowSeconds int64 `json:"windowSeconds"`
}

// Mailbox is a live mailbox row; the stats fields are populated only by
// GET /mailboxes/:id (MailboxGet), absent from list rows.
type Mailbox struct {
	ID             string  `json:"id"`
	PrimaryAddress *string `json:"primaryAddress"`
	QuotaBytes     *int64  `json:"quotaBytes"`
	AccountID      *string `json:"accountId"`
	CreatedAt      int64   `json:"createdAt"`
	// Per-mailbox send policy. The caps are POINTERS because core distinguishes
	// three states and a plain int64 can only carry two: null = no override, so
	// the platform default applies; 0 = explicitly unlimited; n = that cap.
	// Decoding null into 0 would read "inherit the default" as "unlimited",
	// which is the one misreading that removes a bound instead of adding one.
	SendDisabled bool `json:"sendDisabled"`
	// SendPaused is the mailbox-scope reversible hold: 429 sending_paused at
	// submission, DEFER in the relay. Independent of SendDisabled, which wins.
	SendPaused      bool   `json:"sendPaused"`
	SendMsgsPerDay  *int64 `json:"sendMsgsPerDay"`
	SendRcptsPerDay *int64 `json:"sendRcptsPerDay"`

	MessageCount  *int64 `json:"messageCount,omitempty"`
	UsedBytes     *int64 `json:"usedBytes,omitempty"`
	ExpungedCount *int64 `json:"expungedCount,omitempty"`
	ExpungedBytes *int64 `json:"expungedBytes,omitempty"`
	// Store-level stats (GET-only, like the four above): the DO's own SQLite
	// footprint and the live-events socket occupancy.
	DatabaseSize       *int64           `json:"databaseSize,omitempty"`
	DatabaseLimitBytes *int64           `json:"databaseLimitBytes,omitempty"`
	LiveSockets        *int64           `json:"liveSockets,omitempty"`
	SocketCapacity     *int64           `json:"socketCapacity,omitempty"`
	SocketsByClass     map[string]int64 `json:"socketsByClass,omitempty"`
}

// SendUsage is a mailbox's outbound send allowance for the current rolling
// window (GET /mailboxes/{id}/send-usage).
//
// Messages counts DISTINCT CONTENT rather than submissions, so a fan-out that
// posts the same bytes once per recipient — which is what the SMTP submission
// path does — spends one message here, not one per recipient.
type SendUsage struct {
	Messages   int64 `json:"messages"`
	Recipients int64 `json:"recipients"`
	// The limits in force: the mailbox override if it has one, else the
	// platform default. Null means that axis is not enforced at all.
	MsgsPerDay  *int64 `json:"msgsPerDay"`
	RcptsPerDay *int64 `json:"rcptsPerDay"`
	// Sending frozen: every submission is refused regardless of usage.
	Disabled bool `json:"disabled"`
	// Sending HELD: refused temporarily (429) rather than permanently.
	Paused        bool  `json:"paused"`
	WindowSeconds int64 `json:"windowSeconds"`
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
	// When the deadman wipe is due (or was due — WipeOverdue flags a wipe the
	// alarm has not executed yet).
	WipeDueAt   *int64 `json:"wipeDueAt,omitempty"`
	WipeOverdue *bool  `json:"wipeOverdue,omitempty"`
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
