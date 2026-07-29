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

// ListMessages returns one page of live messages, newest-first. The next-page
// cursor rides the response's `cursor` field (not `nextCursor`).
func (c *Client) ListMessages(ctx context.Context, mailboxID, label string, limit int, cursor string) (Page[MessageMeta], error) {
	q := pageValues(limit, cursor)
	if label != "" {
		q.Set("label", label)
	}
	var out struct {
		Messages []MessageMeta `json:"messages"`
		Cursor   *string       `json:"cursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagesPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[MessageMeta]{}, err
	}
	return Page[MessageMeta]{Items: out.Messages, NextCursor: derefStr(out.Cursor)}, nil
}

// ListTrash returns one page of soft-deleted (expunged) messages.
func (c *Client) ListTrash(ctx context.Context, mailboxID string, limit int, cursor string) (Page[ExpungedMessageMeta], error) {
	q := pageValues(limit, cursor)
	q.Set("state", "expunged")
	var out struct {
		Messages []ExpungedMessageMeta `json:"messages"`
		Cursor   *string               `json:"cursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagesPath(mailboxID), query: q, idempotent: true,
	}, &out)
	if err != nil {
		return Page[ExpungedMessageMeta]{}, err
	}
	return Page[ExpungedMessageMeta]{Items: out.Messages, NextCursor: derefStr(out.Cursor)}, nil
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

// EmptyTrash permanently purges every message currently in the trash.
func (c *Client) EmptyTrash(ctx context.Context, mailboxID string) (int64, error) {
	q := url.Values{}
	q.Set("state", "expunged")
	var out struct {
		Purged int64 `json:"purged"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.messagesPath(mailboxID), query: q,
	}, &out)
	return out.Purged, err
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
