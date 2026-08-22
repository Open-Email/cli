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
	// RequireActing (core migration 0046): a system key that must name an
	// acting mailbox on every call.
	RequireActing bool `json:"requireActing"`
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
	// The BURST window (core migration 0047): the same two axes over one hour,
	// same nil/0 spelling.
	SendMsgsPerHour  *int64 `json:"sendMsgsPerHour"`
	SendRcptsPerHour *int64 `json:"sendRcptsPerHour"`
	// The inbound RECEIVE allowance (core migration 0048) — a platform
	// backstop, not a plan axis: over it inbound is DEFERRED, never bounced.
	RecvMsgsPerDay  *int64 `json:"recvMsgsPerDay"`
	RecvBytesPerDay *int64 `json:"recvBytesPerDay"`
	// NoticeMailboxID is where limit notices land as local postmaster mail
	// (core migration 0049); nil = console-only.
	NoticeMailboxID *string `json:"noticeMailboxId"`
	// Plan is the service-class LABEL last applied (core migration 0050) —
	// display and billing only; enforcement reads the stamped columns.
	Plan *string `json:"plan"`
	// StorageLimitBytes is the account storage pool: NIL = platform default,
	// 0 = explicitly unlimited/metered.
	StorageLimitBytes *int64 `json:"storageLimitBytes"`
	// VanityHosts is whether this account may claim VANITY HOSTNAMES — its own
	// `mail.`/`smtp.`/`webmail.`/`dav.` names in front of the platform services.
	// Off by default: each one costs a certificate order and a persistent key on
	// our own fleets, or a billable Cloudflare custom hostname. It gates CLAIMING
	// only — clearing it never revokes hostnames already serving clients.
	VanityHosts bool  `json:"vanityHosts"`
	CreatedAt   int64 `json:"createdAt"`
	// Deletion lifecycle (core migration 0038). All three are epoch SECONDS and
	// all three are nil on a live account.
	//
	// DeletedAt set = the account is FENCED but INTACT: its API key reaches only
	// its own lifecycle routes, its mailbox credentials no longer authenticate,
	// inbound mail DEFERS (429, so the sending MTA queues rather than bounces)
	// and outbound is held. Nothing has been destroyed — a restore before
	// PurgeAt puts everything back.
	//
	// PurgeAt is the instant core PROMISED at delete time. It is a stored value,
	// not one derived from a platform setting, so it is the number to count
	// down against and it does not move.
	//
	// PurgedAt set = the teardown has run and this row is a scrubbed tombstone:
	// an id and these timestamps, nothing else. It survives because the usage
	// ledger's open days cannot close without it.
	DeletedAt *int64 `json:"deletedAt"`
	PurgeAt   *int64 `json:"purgeAt"`
	PurgedAt  *int64 `json:"purgedAt"`
}

// AccountDeleteResult is DELETE /accounts/:accountId — what the soft delete
// promised. Idempotent: a repeat answers the FIRST call's instants, so a
// retried command never silently extends a window somebody is waiting out.
type AccountDeleteResult struct {
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
	PurgeAt   int64  `json:"purgeAt"`
	// Restorable is false only for a ?purge=true delete, which starts the
	// teardown at once.
	Restorable bool `json:"restorable"`
}

// AccountSendUsage is GET /accounts/:accountId/send-usage — the tenant-scale
// allowance window: what the account has SENT, how many mailboxes it has
// MINTED, and whether sending is disabled. The cross-mailbox reading a
// per-mailbox send-usage call cannot give.
type AccountSendUsage struct {
	AccountID string `json:"accountId"`
	Disabled  bool   `json:"disabled"`
	// Paused is the reversible half: refused temporarily (429) rather than
	// permanently. Reported apart from Disabled because the two are different
	// answers for a support agent — "we disabled them" vs "we are holding them".
	Paused bool `json:"paused"`
	Send   struct {
		Messages    int64  `json:"messages"`
		Recipients  int64  `json:"recipients"`
		MsgsPerDay  *int64 `json:"msgsPerDay"`
		RcptsPerDay *int64 `json:"rcptsPerDay"`
		// The burst window's consumption and limits (current + previous hourly
		// bucket, summed across the account's ledger shards).
		MessagesHour   int64  `json:"messagesHour"`
		RecipientsHour int64  `json:"recipientsHour"`
		MsgsPerHour    *int64 `json:"msgsPerHour"`
		RcptsPerHour   *int64 `json:"rcptsPerHour"`
	} `json:"send"`
	Creates struct {
		Mailboxes int64  `json:"mailboxes"`
		PerDay    *int64 `json:"perDay"`
	} `json:"creates"`
	WindowSeconds      int64 `json:"windowSeconds"`
	BurstWindowSeconds int64 `json:"burstWindowSeconds"`
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
	SendPaused       bool   `json:"sendPaused"`
	SendMsgsPerDay   *int64 `json:"sendMsgsPerDay"`
	SendRcptsPerDay  *int64 `json:"sendRcptsPerDay"`
	SendMsgsPerHour  *int64 `json:"sendMsgsPerHour"`
	SendRcptsPerHour *int64 `json:"sendRcptsPerHour"`

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
	// The burst window (current + previous hourly bucket) and its limits.
	MessagesHour   int64  `json:"messagesHour"`
	RecipientsHour int64  `json:"recipientsHour"`
	MsgsPerHour    *int64 `json:"msgsPerHour"`
	RcptsPerHour   *int64 `json:"rcptsPerHour"`
	// Sending DISABLED: every submission is refused regardless of usage.
	Disabled bool `json:"disabled"`
	// Sending HELD: refused temporarily (429) rather than permanently.
	Paused             bool  `json:"paused"`
	WindowSeconds      int64 `json:"windowSeconds"`
	BurstWindowSeconds int64 `json:"burstWindowSeconds"`
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
