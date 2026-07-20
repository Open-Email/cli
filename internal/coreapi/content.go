package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// The structured "email-client view" of a message (core's GET .../content):
// decoded headers, text/html bodies inline, attachments as fetchable /parts
// references. Consumes core's core-private part_index; MIME mechanics never
// surface — `Section` is the only part handle. Nullable fields are pointers.

// ContentAddress is one entry of a header address-list. Name is nil when absent.
type ContentAddress struct {
	Name  *string `json:"name"`
	Email string  `json:"email"`
}

// ContentHeaders is the header-derived view — NOT the SMTP envelope (that is
// MessageMeta.EnvelopeFrom/To). Date is epoch seconds (the message's sent time),
// nil when the Date header is absent or unparseable.
type ContentHeaders struct {
	From            []ContentAddress `json:"from"`
	To              []ContentAddress `json:"to"`
	Cc              []ContentAddress `json:"cc"`
	ReplyTo         []ContentAddress `json:"replyTo"`
	Subject         *string          `json:"subject"`
	Date            *int64           `json:"date"`
	MessageIDHeader *string          `json:"messageIdHeader"`
	InReplyTo       *string          `json:"inReplyTo"`
}

// ContentBody is a decoded text or html body. Content is nil iff Truncated (the
// decoded body exceeds core's inlining ceiling — fetch it via GetPart instead);
// Size is the full decoded byte size either way. Charset is the source charset
// (Content is already transcoded to a UTF-8 string).
type ContentBody struct {
	Section   string  `json:"section"`
	Content   *string `json:"content"`
	Size      int64   `json:"size"`
	Truncated bool    `json:"truncated"`
	Charset   *string `json:"charset"`
}

// AttachmentRef is a non-body part, fetchable via GetPart(Section). Size is the
// DECODED size (what GetPart delivers). Inline is set for a Content-Disposition
// of inline or a bare Content-ID; Degraded marks a synthetic fallback part.
type AttachmentRef struct {
	Section     string  `json:"section"`
	Filename    string  `json:"filename"`
	ContentType string  `json:"contentType"`
	Size        int64   `json:"size"`
	Inline      bool    `json:"inline"`
	ContentID   *string `json:"contentId"`
	Degraded    bool    `json:"degraded,omitempty"`
}

// ContentResult is the flat email-client view: headers + text/html + attachments.
// Text/HTML are nil when the message has no such body.
type ContentResult struct {
	Headers     ContentHeaders  `json:"headers"`
	Text        *ContentBody    `json:"text"`
	HTML        *ContentBody    `json:"html"`
	Attachments []AttachmentRef `json:"attachments"`
}

// GetContent returns the structured content view of one live message.
func (c *Client) GetContent(ctx context.Context, mailboxID, messageID string) (*ContentResult, error) {
	var out ContentResult
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.messagePath(mailboxID, messageID) + "/content", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPart streams one MIME part by its IMAP section number (e.g. "2", "3.1").
// raw=false reverses the transfer encoding (the decoded bytes); raw=true serves
// the stored encoded slice. The returned header carries Content-Type,
// Content-Disposition (a suggested filename), and Content-Length. Close the reader.
func (c *Client) GetPart(ctx context.Context, mailboxID, messageID, section string, raw bool) (io.ReadCloser, http.Header, error) {
	q := url.Values{}
	if raw {
		q.Set("raw", "true")
	}
	return c.StreamGetResp(ctx, c.messagePath(mailboxID, messageID)+"/parts/"+escapeSegment(section), q)
}
