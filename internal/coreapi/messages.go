package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MessageLabel is one of a message's label memberships (each label owns its own
// UID space, so the same message has a different uid per label).
type MessageLabel struct {
	Name        string `json:"name"`
	UID         int64  `json:"uid"`
	UIDValidity int64  `json:"uidValidity"`
	// Modseq is the RFC 7162 MODSEQ of this membership row (floored to 1).
	Modseq int64 `json:"modseq"`
}

// MessageMeta is the shared message-metadata object (core's rowToMeta), returned
// by messages get/list, search, threads, and label listing. Body content is
// NEVER here — fetch it via .../raw. Timestamps are epoch seconds; nullable
// fields are pointers. In a label message listing each element also carries a
// top-level UID (nil elsewhere).
type MessageMeta struct {
	ID           string         `json:"id"`
	Labels       []MessageLabel `json:"labels"`
	Flags        []string       `json:"flags"`
	EnvelopeFrom string         `json:"envelopeFrom"`
	EnvelopeTo   string         `json:"envelopeTo"`
	// From/To are the DISPLAY view of the RFC5322 headers, stamped at delivery.
	// Nil means unknown (an older row, or an unparseable head) — fall back to
	// EnvelopeFrom/EnvelopeTo rather than showing nothing.
	From            []ContentAddress `json:"from"`
	To              []ContentAddress `json:"to"`
	CC              []ContentAddress `json:"cc"`
	BCC             []ContentAddress `json:"bcc"`
	Subject         *string          `json:"subject"`
	MessageIDHeader *string          `json:"messageIdHeader"`
	ThreadID        *string          `json:"threadId"`
	InReplyTo       *string          `json:"inReplyTo"`
	ReferencesIDs   []string         `json:"referencesIds"`
	SentAt          *int64           `json:"sentAt"`
	ReceivedAt      int64            `json:"receivedAt"`
	Size            int64            `json:"size"`
	Snippet         *string          `json:"snippet"`
	BlobHash        string           `json:"blobHash"`
	BlobGen         string           `json:"blobGen"`
	DeliveryMeta    json.RawMessage  `json:"deliveryMeta"`
	// DeliveryState is what became of a message this mailbox SENT: "queued"
	// (handed to the relay), "relayed" (the receiving system accepted it, which
	// is NOT "read"), "delivered", and the failure states. NIL for received
	// mail, which is most of it — the field describes egress, so a message that
	// arrived has no state to report and null is not "unknown".
	DeliveryState *string `json:"deliveryState"`
	// RestoresTo is where a recovery would file this message, as label names.
	// Present only while the message is RECOVERABLE: carrying the Trash label
	// (POST /messages/untrash) or sitting on the expunged tier
	// (POST /messages/recover). Nil on an ordinary live message, so its
	// presence is the "this can be undone" signal rather than a separate probe.
	RestoresTo []string `json:"restoresTo"`
	// HasAttachment follows the RFC 8621 §4.1.4 rule (the same computation
	// GET /content's attachment list uses). Nil = not yet known: the flag is
	// stamped at delivery and backfilled lazily on first read of an older
	// message, so a client should show nothing rather than guess.
	HasAttachment *bool `json:"hasAttachment"`
	// Keywords are the arbitrary JMAP keywords (lowercase-canonical), e.g.
	// $forwarded. The four bitmask-backed ones ($seen/$answered/$flagged/$draft)
	// live in Flags and are never repeated here.
	Keywords []string `json:"keywords"`
	UID      *int64   `json:"uid,omitempty"`
	// Modseq is the per-label MODSEQ — present only in a label's UID listing.
	Modseq *int64 `json:"modseq,omitempty"`
}

// ExpungedMessageMeta is a trash-listing row: MessageMeta (labels always empty)
// plus the trash-window fields.
type ExpungedMessageMeta struct {
	MessageMeta
	ExpungedAt     int64    `json:"expungedAt"`
	PurgeAfter     int64    `json:"purgeAfter"`
	ExpungedLabels []string `json:"expungedLabels"`
}

// AppendResult is the APPEND success union (both HTTP 200): status=="delivered"
// (messageId/uid/uidValidity/threadId/duplicate; uid/uidValidity/threadId may be
// null on an idempotent replay) or status=="filtered" (deliveryId/redirected —
// the active Sieve script discarded or redirected it; nothing stored).
type AppendResult struct {
	Status      string  `json:"status"`
	MessageID   string  `json:"messageId,omitempty"`
	UID         *int64  `json:"uid,omitempty"`
	UIDValidity *int64  `json:"uidValidity,omitempty"`
	ThreadID    *string `json:"threadId,omitempty"`
	Duplicate   bool    `json:"duplicate,omitempty"`
	DeliveryID  string  `json:"deliveryId,omitempty"`
	Redirected  bool    `json:"redirected,omitempty"`
}

// PatchResult is the PATCH message union: the full updated MessageMeta, or an
// expunge notice ({expunged:true, expungedAt, purgeAfter}) when the patch removed
// the message's last label. Discriminate on Expunged.
type PatchResult struct {
	MessageMeta
	Expunged   bool   `json:"expunged"`
	ExpungedAt *int64 `json:"expungedAt,omitempty"`
	PurgeAfter *int64 `json:"purgeAfter,omitempty"`
}

// DeleteResult is the DELETE message union (three shapes, all HTTP 200):
//   - purge:    {deleted:true, purged:true, blobOrphaned}
//   - ?label=X: {removedFromLabel:"<label name>", expunged, [expungedAt, purgeAfter]}
//   - default:  {deleted:true, expunged:true, expungedAt, purgeAfter}
//
// RemovedFromLabel is the label NAME (a string), NOT a boolean.
type DeleteResult struct {
	Deleted          bool    `json:"deleted,omitempty"`
	Purged           bool    `json:"purged,omitempty"`
	BlobOrphaned     bool    `json:"blobOrphaned,omitempty"`
	RemovedFromLabel *string `json:"removedFromLabel,omitempty"`
	Expunged         bool    `json:"expunged,omitempty"`
	ExpungedAt       *int64  `json:"expungedAt,omitempty"`
	PurgeAfter       *int64  `json:"purgeAfter,omitempty"`
}

// RestoreResult is the restore-from-trash response.
type RestoreResult struct {
	Restored bool        `json:"restored"`
	Message  MessageMeta `json:"message"`
}

// MimeEntry is one entry of the /messages/mime batch (null in the map when the id
// is absent, not live, or has no cached MIME yet). Envelope/structure are opaque.
type MimeEntry struct {
	Ver       int64           `json:"ver"`
	Envelope  json.RawMessage `json:"envelope,omitempty"`
	Structure json.RawMessage `json:"structure,omitempty"`
}

// PatchInput is the PATCH body: flags to set/clear and labels to add/remove
// (additions apply before removals, so a one-call move never passes through a
// zero-label state). Empty fields are omitted.
type PatchInput struct {
	FlagsSet     []string `json:"flagsSet,omitempty"`
	FlagsClear   []string `json:"flagsClear,omitempty"`
	LabelsAdd    []string `json:"labelsAdd,omitempty"`
	LabelsRemove []string `json:"labelsRemove,omitempty"`
}

// AppendOptions carries the APPEND envelope + placement (headers/query).
type AppendOptions struct {
	Label        string // ?label= (target label name; empty → inbox)
	Flags        []string
	Filter       bool   // ?filter=true runs the active Sieve script
	InternalDate *int64 // ?internaldate= (IMAP INTERNALDATE preservation)
	DeliveryID   string // X-Delivery-Id (idempotency); empty → core mints its own
	EnvelopeFrom string // X-Envelope-From ("" allowed → bypasses routing)
	EnvelopeTo   string // X-Envelope-To
}

func (c *Client) messagesPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/messages"
}

func (c *Client) messagePath(mailboxID, messageID string) string {
	return c.messagesPath(mailboxID) + "/" + escapeSegment(messageID)
}

// ListMessages returns one page of live messages, newest-first (see ListOpts
// for the order/sortBy knobs). The next-page cursor rides the response's
// `nextCursor` field — absent once the listing is exhausted.
func (c *Client) ListMessages(ctx context.Context, mailboxID string, opts ListOpts) (Page[MessageMeta], error) {
	q := opts.values(opts.Cursor)
	var out struct {
		Messages   []MessageMeta `json:"messages"`
		NextCursor string        `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagesPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[MessageMeta]{}, err
	}
	return Page[MessageMeta]{Items: out.Messages, NextCursor: out.NextCursor}, nil
}

// ListTrash returns one page of soft-deleted (expunged) messages. Takes the
// same ListOpts as the live listing minus Label, which core refuses here.
func (c *Client) ListTrash(ctx context.Context, mailboxID string, opts ListOpts) (Page[ExpungedMessageMeta], error) {
	opts.Label = ""
	q := opts.values(opts.Cursor)
	q.Set("state", "expunged")
	var out struct {
		Messages   []ExpungedMessageMeta `json:"messages"`
		NextCursor string                `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagesPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[ExpungedMessageMeta]{}, err
	}
	return Page[ExpungedMessageMeta]{Items: out.Messages, NextCursor: out.NextCursor}, nil
}

// GetMessage returns one live message's metadata.
func (c *Client) GetMessage(ctx context.Context, mailboxID, messageID string) (*MessageMeta, error) {
	var out MessageMeta
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagePath(mailboxID, messageID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetMessageRaw streams a message's raw message/rfc822 body. The caller must
// close the returned reader.
func (c *Client) GetMessageRaw(ctx context.Context, mailboxID, messageID string) (io.ReadCloser, error) {
	return c.StreamGet(ctx, c.messagePath(mailboxID, messageID)+"/raw", nil)
}

// AppendMessage streams a raw MIME message into a mailbox (IMAP APPEND, bypasses
// routing). getBody must yield a fresh reader per attempt; bodyLen is the exact
// Content-Length. A non-empty DeliveryID makes the upload idempotent (safe to
// retry).
func (c *Client) AppendMessage(ctx context.Context, mailboxID string, opts AppendOptions, getBody func() (io.ReadCloser, error), bodyLen int64) (*AppendResult, []byte, error) {
	q := url.Values{}
	if opts.Label != "" {
		q.Set("label", opts.Label)
	}
	if len(opts.Flags) > 0 {
		q.Set("flags", strings.Join(opts.Flags, ","))
	}
	if opts.Filter {
		q.Set("filter", "true")
	}
	if opts.InternalDate != nil {
		q.Set("internaldate", itoa64(*opts.InternalDate))
	}
	headers := map[string]string{
		"X-Envelope-From": opts.EnvelopeFrom,
		"X-Envelope-To":   opts.EnvelopeTo,
	}
	if opts.DeliveryID != "" {
		headers["X-Delivery-Id"] = opts.DeliveryID
	}
	var out AppendResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: c.messagesPath(mailboxID), query: q,
		getBody: getBody, bodyLen: bodyLen, contentType: "message/rfc822",
		headers: headers, idempotent: true,
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// PatchMessage sets/clears flags and adds/removes labels.
func (c *Client) PatchMessage(ctx context.Context, mailboxID, messageID string, in PatchInput) (*PatchResult, error) {
	var out PatchResult
	err := c.doJSON(ctx, request{
		method: http.MethodPatch, path: c.messagePath(mailboxID, messageID),
		body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMessage soft-expunges a message (default), detaches it from one label
// (label != ""), or permanently purges it (purge=true; not combinable with
// label).
func (c *Client) DeleteMessage(ctx context.Context, mailboxID, messageID, label string, purge bool) (*DeleteResult, error) {
	q := url.Values{}
	if label != "" {
		q.Set("label", label)
	}
	if purge {
		q.Set("purge", "true")
	}
	var out DeleteResult
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.messagePath(mailboxID, messageID), query: q,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RestoreMessage restores an expunged (trash) message.
func (c *Client) RestoreMessage(ctx context.Context, mailboxID, messageID string) (*RestoreResult, error) {
	var out RestoreResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.messagePath(mailboxID, messageID) + "/restore",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LearnMessage teaches the spam filter from a message: class "spam" marks it
// junk, "ham" marks it legitimate. The sample trains THIS mailbox's personal
// overlay, so one mailbox's idea of junk never becomes another's.
//
// Fire-and-forget by design — a 202 means the sample was accepted for
// submission, not that the filter has learned it, and repeated calls on the
// same message dedupe filter-side. 503 learning_unavailable when the deployment
// has no spam filter configured at all.
func (c *Client) LearnMessage(ctx context.Context, mailboxID, messageID, class string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.messagePath(mailboxID, messageID) + "/learn",
		body: mustJSON(map[string]string{"class": class}), contentType: "application/json",
	}, &out)
	return out.Status, err
}

// BatchLearnEntry is one message's outcome in a batch training call, in REQUEST
// order so a caller can pair the rows with the ids it sent. "not_found" is per
// message and does not refuse the others: the id is not in the live tier, which
// is the same tier the single form reads, so a trashed message is unresolvable
// here exactly as it is there.
type BatchLearnEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"` // accepted | not_found
}

// BatchLearnResult carries the per-message outcomes of one batch training call.
type BatchLearnResult struct {
	Results []BatchLearnEntry `json:"results"`
}

// LearnMessages teaches the spam filter from up to 200 messages in ONE call.
//
// The saving is core-side and larger than "one request instead of N": the batch
// route resolves every id in a single read against the mailbox, then submits the
// samples under ONE background budget. That budget is also the reason to prefer
// this over a loop only up to the documented ceiling — core trains a prefix and
// logs a truncation if the whole batch cannot be submitted in time, so a caller
// that wants certainty for a huge set should send several batches rather than
// one oversized one.
//
// Fire-and-forget like the single form: 202 means the samples were accepted for
// submission, not learned, and duplicate ids collapse to one sample while still
// getting their own result row. 503 learning_unavailable when the deployment has
// no spam filter configured at all, answered before any id is resolved.
func (c *Client) LearnMessages(ctx context.Context, mailboxID string, ids []string, class string) (*BatchLearnResult, error) {
	var out BatchLearnResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.messagesPath(mailboxID) + "/learn",
		body: mustJSON(map[string]any{"ids": ids, "class": class}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// BatchRestoreEntry is one message's outcome in a batch restore. Message is
// present iff Status is "restored" (and carries the FRESH label UIDs the
// restore minted, as IMAP requires). An id that is missing, already live, or
// already purged is "not_found" for itself and does not refuse the others.
type BatchRestoreEntry struct {
	ID      string       `json:"id"`
	Status  string       `json:"status"` // restored | not_found
	Message *MessageMeta `json:"message,omitempty"`
}

// BatchRestoreResult carries per-message outcomes in REQUEST order, so a caller
// can pair them with the ids it sent without matching on id.
type BatchRestoreResult struct {
	Results []BatchRestoreEntry `json:"results"`
}

// RestoreMessages restores up to 200 expunged messages in ONE call — the undo
// of a batch delete. The point is atomicity: N per-message calls can half-fail
// and leave the trash in a state nobody chose, while this lands in a single
// commit against the mailbox. Each message is re-attached under its pre-expunge
// labels, or INBOX if none survive.
func (c *Client) RestoreMessages(ctx context.Context, mailboxID string, ids []string) (*BatchRestoreResult, error) {
	var out BatchRestoreResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.messagesPath(mailboxID) + "/restore",
		body: mustJSON(map[string]any{"ids": ids}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// EmptyTrash permanently purges every message currently in the trash.
func (c *Client) EmptyTrash(ctx context.Context, mailboxID string) (int64, error) {
	q := url.Values{}
	q.Set("state", "expunged")
	var out struct {
		PurgedCount int64 `json:"purgedCount"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.messagesPath(mailboxID), query: q,
	}, &out)
	return out.PurgedCount, err
}

// MimeBatch fetches cached ENVELOPE/BODYSTRUCTURE for up to 200 message ids. The
// map is fully seeded: a null value means the id is absent, not live, or
// uncached.
func (c *Client) MimeBatch(ctx context.Context, mailboxID string, ids []string) (map[string]*MimeEntry, error) {
	var out struct {
		Mime map[string]*MimeEntry `json:"mime"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: c.messagesPath(mailboxID) + "/mime",
		body: mustJSON(map[string]any{"ids": ids}), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Mime, nil
}
