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
	// Kind (core migration 0051) is what the key IS — "cli" for one the browser
	// login minted, null otherwise. Display metadata: the minting caller sets
	// it, so nothing may authorize on it.
	Kind *string `json:"kind"`
	// IdleTTLS is the configured seconds of disuse before the key stops
	// working; null for a key that never lapses, which is every key minted
	// before core 0051 and every key created outside the browser login.
	IdleTTLS *int64 `json:"idleTtlS"`
	// IdleExpiresAt is when that lapse falls, derived from IdleTTLS and the last
	// use (or creation, for a key never presented). Null when it never lapses.
	IdleExpiresAt *int64 `json:"idleExpiresAt"`
}

// CreatedAPIKey is POST /api-keys — the only place the plaintext token appears.
type CreatedAPIKey struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	AccountID *string `json:"accountId"`
	Token     string  `json:"token"`
	Managed   bool    `json:"managed"`
	// Echoed back from the mint (core migration 0051) so a caller knows what it
	// got without re-reading the key: see APIKey.Kind and APIKey.IdleTTLS.
	Kind     *string `json:"kind"`
	IdleTTLS *int64  `json:"idleTtlS"`
	// When the key would lapse if never used. Computed by core from the TTL it
	// just recorded, so a caller shows the date without doing the arithmetic
	// against a clock that is not the one enforcing the lapse.
	IdleExpiresAt *int64 `json:"idleExpiresAt"`
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
	// SendHold is the TENANT-SCALE send enforcement over every mailbox on every
	// domain this account owns, at submission and in the already-queued relay
	// backlog: nil = none, "paused" = the reversible hold (429 sending_paused —
	// SMTP submission gets a 451, the sending MTA queues, the backlog is
	// DEFERRED; for non-payment or an investigation), "disabled" = the
	// permanent stop (403 — 550, the backlog bounced; the abuse response). One
	// field, so there is no precedence to know. Egress only — a held account
	// still receives its mail. System-writable only.
	SendHold *string `json:"sendHold"`
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
	VanityHosts bool `json:"vanityHosts"`
	// RecoverySelfService is whether mailbox users in this account may recover
	// their OWN password, via recovery codes. False is the admin_only policy,
	// under which existing codes and addresses are KEPT rather than destroyed:
	// the flag gates the self-service PATH, so flipping it back restores what
	// was already issued instead of forcing every user to re-enrol.
	RecoverySelfService bool  `json:"recoverySelfService"`
	CreatedAt           int64 `json:"createdAt"`
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
	// SendHold is the account's own hold, named: nil, "paused" (refused
	// temporarily, 429) or "disabled" (refused permanently) — the two are
	// different answers for a support agent, "we are holding them" vs "we
	// disabled them".
	SendHold *string `json:"sendHold"`
	Send     struct {
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
	// SendHold is the mailbox-scope operator enforcement: nil, "paused" (429
	// sending_paused at submission, DEFER in the relay) or "disabled" (403,
	// the backlog bounced). One field, no precedence to know.
	SendHold         *string `json:"sendHold"`
	SendMsgsPerDay   *int64  `json:"sendMsgsPerDay"`
	SendRcptsPerDay  *int64  `json:"sendRcptsPerDay"`
	SendMsgsPerHour  *int64  `json:"sendMsgsPerHour"`
	SendRcptsPerHour *int64  `json:"sendRcptsPerHour"`

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
	// SendHold is the mailbox's own hold: nil, "paused" (refused temporarily,
	// 429) or "disabled" (refused permanently regardless of usage).
	SendHold           *string `json:"sendHold"`
	WindowSeconds      int64   `json:"windowSeconds"`
	BurstWindowSeconds int64   `json:"burstWindowSeconds"`
}

// DeletedMailbox is a restorable tombstone (GET /identities?state=deleted).
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

// MailboxCreateInput is the POST /identities body (all fields optional; accountId
// is honored only for system callers).
type MailboxCreateInput struct {
	PrimaryAddress *string `json:"primaryAddress,omitempty"`
	QuotaBytes     *int64  `json:"quotaBytes,omitempty"`
	AccountID      *string `json:"accountId,omitempty"`
}

// MailboxDeleteResult is the DELETE /identities/:id body.
type MailboxDeleteResult struct {
	Deleted         bool   `json:"deleted"`
	Purged          bool   `json:"purged,omitempty"`
	Restorable      bool   `json:"restorable"`
	RestorableUntil *int64 `json:"restorableUntil,omitempty"`
}

// MailboxPurgeResult is the POST /identities/:id/purge body. Restorable is
// always false (the tombstone is now non-restorable).
type MailboxPurgeResult struct {
	Purged     bool `json:"purged"`
	Restorable bool `json:"restorable"`
}

// MailboxRestoreResult is the POST /identities/:id/restore body.
type MailboxRestoreResult struct {
	Restored       bool    `json:"restored"`
	ID             string  `json:"id"`
	AccountID      *string `json:"accountId"`
	QuotaBytes     *int64  `json:"quotaBytes"`
	PrimaryAddress *string `json:"primaryAddress"`
	AddressChanged bool    `json:"addressChanged"`
}
