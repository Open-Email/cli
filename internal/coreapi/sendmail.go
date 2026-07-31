package coreapi

import (
	"context"
	"io"
	"net/http"
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
func (c *Client) SendMessage(ctx context.Context, mailboxID string, req SendRequest) (*SendResult, []byte, error) {
	var out SendResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/mailboxes/" + escapeSegment(mailboxID) + "/send",
		body: mustJSON(req), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}
