package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// UploadResult is POST /uploads: a staged blob referenced by later calls.
// Expires is an ISO 8601 UTC instant after which the upload is treated as
// gone — reference it before then.
type UploadResult struct {
	BlobID  string `json:"blobId"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Expires string `json:"expires"`
}

// UploadBlob stages arbitrary bytes for later reference (an attachment on
// /send, or a JMAP blobId). getBody must yield a fresh reader per attempt and
// bodyLen must be the exact length — core requires a Content-Length (411
// without). 413 when the blob exceeds the per-upload cap.
func (c *Client) UploadBlob(ctx context.Context, mailboxID string, getBody func() (io.ReadCloser, error), bodyLen int64, contentType string) (*UploadResult, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var out UploadResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/uploads",
		getBody: getBody, bodyLen: bodyLen, contentType: contentType,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SendAddress is one addressee. Name is optional; Address is required.
type SendAddress struct {
	Address string  `json:"address"`
	Name    *string `json:"name,omitempty"`
}

// SendAttachment is one attachment in any of its three forms — exactly one is
// used: BlobID (a staged upload), Content (inline base64), or MessageID+Section
// (a part of a message already in this mailbox).
type SendAttachment struct {
	BlobID      string  `json:"blobId,omitempty"`
	Content     string  `json:"content,omitempty"`
	MessageID   string  `json:"messageId,omitempty"`
	Section     string  `json:"section,omitempty"`
	Filename    string  `json:"filename,omitempty"`
	ContentType *string `json:"contentType,omitempty"`
	ContentID   *string `json:"contentId,omitempty"`
}

// SendRequest is the structured send body: JSON in, core assembles the RFC
// 5322 message. From must resolve to the sending mailbox (it also becomes the
// envelope sender, keeping the From header DMARC-aligned with the signature).
type SendRequest struct {
	From        SendAddress       `json:"from"`
	To          []SendAddress     `json:"to,omitempty"`
	Cc          []SendAddress     `json:"cc,omitempty"`
	Bcc         []SendAddress     `json:"bcc,omitempty"`
	ReplyTo     []SendAddress     `json:"replyTo,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	Text        *string           `json:"text,omitempty"`
	HTML        *string           `json:"html,omitempty"`
	InReplyTo   string            `json:"inReplyTo,omitempty"`
	References  []string          `json:"references,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []SendAttachment  `json:"attachments,omitempty"`
}

// SendRecipientResult is one recipient's outcome. Error is present only when
// Status is "failed" (e.g. receiving_disabled, over_quota).
type SendRecipientResult struct {
	Address string `json:"address"`
	Status  string `json:"status"` // delivered | queued | filtered | failed
	Error   string `json:"error,omitempty"`
}

// SendResult is the structured send response: one result per recipient. The
// call answers 207 when the outcomes differ — both codes decode here, so
// callers inspect Recipients rather than the status code.
type SendResult struct {
	DeliveryID string                `json:"deliveryId"`
	Recipients []SendRecipientResult `json:"recipients"`
	// SentCopy is the fate of the single Sent copy; "over_quota" means sending
	// proceeded without one.
	SentCopy     string  `json:"sentCopy,omitempty"`
	SentThreadID *string `json:"sentThreadId,omitempty"`
}

// SendMessage submits a structured message to one or more recipients. The raw
// bytes are never built client-side — core assembles the MIME, so attachments,
// multipart structure, and encoding are its job.
//
// opts carries the same knobs ReplyToThread takes, and the DeliveryID among
// them is not optional in practice. /send answers 207 with a PER-RECIPIENT
// result precisely so a caller can retry and converge only the failures — core
// keys that on X-Delivery-Id. Submitting without one means every retry is a
// brand-new send, so the recipients who already received the message receive it
// again; that is the failure mode the structured API exists to avoid, and it is
// the shape a script hits first (207 → non-zero exit → re-run).
func (c *Client) SendMessage(ctx context.Context, mailboxID string, req SendRequest, opts SendOptions) (*SendResult, []byte, error) {
	r := request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/send",
		body: mustJSON(req), contentType: "application/json",
	}
	opts.apply(&r)
	var out SendResult
	raw, err := c.doJSONRaw(ctx, r, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// ThreadReplyRequest is the reply body: only what the user actually wrote.
// There is deliberately no recipient, no From, and no In-Reply-To/References —
// core derives every one of them from the conversation. Subject overrides the
// derived "Re: …" only when a caller really means to rename the thread, and
// FromName sets the display name (the address is always this mailbox's).
type ThreadReplyRequest struct {
	Subject     string            `json:"subject,omitempty"`
	FromName    string            `json:"fromName,omitempty"`
	Text        *string           `json:"text,omitempty"`
	HTML        *string           `json:"html,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []SendAttachment  `json:"attachments,omitempty"`
}

// SendOptions are the submission-time knobs shared by /send and the thread
// reply: whether to keep a Sent copy, whether a terminal relay failure should
// bounce a DSN back into this mailbox, and the idempotency key. Save/Bounce are
// nil to omit (core defaults save=true, bounce=false).
type SendOptions struct {
	Save       *bool
	Bounce     *bool
	DeliveryID string
}

func (o SendOptions) apply(r *request) {
	q := url.Values{}
	strictBool(q, "save", o.Save)
	strictBool(q, "bounce", o.Bounce)
	if len(q) > 0 {
		r.query = q
	}
	if o.DeliveryID != "" {
		r.headers = map[string]string{"X-Delivery-Id": o.DeliveryID}
	}
}

// ReplyToThread answers a thread. The recipient, subject and threading headers
// come from the conversation, not from the caller — which is the whole reason
// to prefer this over SendMessage: every client that rebuilds In-Reply-To and
// References itself gets them subtly wrong, and a broken chain is invisible
// until someone else's client fails to group the conversation.
//
// 400 no_reply_target when the thread has nobody to answer (a null-sender DSN,
// say). The result is the same per-recipient shape as a send, so a 207 decodes
// here too and callers inspect Recipients rather than the status code.
func (c *Client) ReplyToThread(ctx context.Context, mailboxID, threadID string, req ThreadReplyRequest, opts SendOptions) (*SendResult, []byte, error) {
	r := request{
		method: http.MethodPost,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/threads/" + escapeSegment(threadID) + "/reply",
		body:   mustJSON(req), contentType: "application/json",
	}
	opts.apply(&r)
	var out SendResult
	raw, err := c.doJSONRaw(ctx, r, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// ComposeOptions are the filing knobs for a composed (unsent) message — the
// same set APPEND takes, since it is the same storage path.
type ComposeOptions struct {
	Label        string
	Flags        []string
	Filter       bool
	InternalDate *int64
	DeliveryID   string
}

// ComposeMessage files a structured message into a mailbox WITHOUT sending it:
// drafts, imports, seeded threads. Routing, relay and the Sent copy are all
// bypassed, so nothing leaves the platform — use SendMessage when the message
// is meant to go somewhere, and AppendMessage when you already hold the raw
// RFC 5322 bytes rather than fields.
//
// Unlike a send, From is not required to be a sendable identity: nothing is
// submitted, so there is no envelope sender to keep DMARC-aligned.
func (c *Client) ComposeMessage(ctx context.Context, mailboxID string, req SendRequest, opts ComposeOptions) (*AppendResult, []byte, error) {
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
	var headers map[string]string
	if opts.DeliveryID != "" {
		headers = map[string]string{"X-Delivery-Id": opts.DeliveryID}
	}
	var out AppendResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/messages/compose",
		query: q, body: mustJSON(req), contentType: "application/json", headers: headers,
		// Retryable ONLY because the caller carries a delivery id: core
		// documents 503 superseded/reaped as "retry with the same
		// X-Delivery-Id", and without this the transparent retry that key
		// exists for never fires. A caller that omits the id gets at-most-once
		// behaviour by not being retried.
		idempotent: opts.DeliveryID != "",
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}
