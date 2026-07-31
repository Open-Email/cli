package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// DeliverResult is the shared delivery trichotomy returned by /deliver/outbound,
// /deliver/inbound, and the pop3-fetch pickup ingest. status is delivered | filtered |
// queued. uid/uidValidity/threadId are null on an idempotent replay. sentCopy /
// sentThreadId appear only on an outbound ?save=true.
type DeliverResult struct {
	Status       string  `json:"status"`
	MessageID    string  `json:"messageId,omitempty"`
	UID          *int64  `json:"uid,omitempty"`
	UIDValidity  *int64  `json:"uidValidity,omitempty"`
	ThreadID     *string `json:"threadId,omitempty"`
	Duplicate    bool    `json:"duplicate,omitempty"`
	DeliveryID   string  `json:"deliveryId,omitempty"`
	SentCopy     string  `json:"sentCopy,omitempty"`
	SentThreadID *string `json:"sentThreadId,omitempty"`
}

// OutboundOptions carries the outbound envelope (headers) + strict save/bounce
// query flags. Save/Bounce are nil to omit (core defaults false).
type OutboundOptions struct {
	EnvelopeFrom string
	EnvelopeTo   string
	DeliveryID   string
	SourceHost   string
	DeliveryMeta string // raw JSON string (X-Delivery-Meta)
	Save         *bool
	Bounce       *bool
}

func deliverHeaders(from, to, deliveryID, sourceHost, meta string) map[string]string {
	h := map[string]string{
		"X-Envelope-From": from,
		"X-Envelope-To":   to,
		"X-Delivery-Id":   deliveryID,
	}
	if sourceHost != "" {
		h["X-Source-Host"] = sourceHost
	}
	if meta != "" {
		h["X-Delivery-Meta"] = meta
	}
	return h
}

func strictBool(q url.Values, key string, v *bool) {
	if v == nil {
		return
	}
	if *v {
		q.Set(key, "true")
	} else {
		q.Set(key, "false")
	}
}

// SubmitOutbound submits one outbound message (exactly one recipient). The upload
// carries X-Delivery-Id, so it is idempotent and safe to retry on 5xx.
func (c *Client) SubmitOutbound(ctx context.Context, opts OutboundOptions, getBody func() (io.ReadCloser, error), bodyLen int64) (*DeliverResult, []byte, error) {
	q := url.Values{}
	strictBool(q, "save", opts.Save)
	strictBool(q, "bounce", opts.Bounce)
	var out DeliverResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/deliver/outbound", query: q,
		getBody: getBody, bodyLen: bodyLen, contentType: "message/rfc822",
		headers:    deliverHeaders(opts.EnvelopeFrom, opts.EnvelopeTo, opts.DeliveryID, opts.SourceHost, opts.DeliveryMeta),
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// InboundOptions carries the inbound envelope (headers). Null sender ("" / "<>")
// is accepted inbound.
type InboundOptions struct {
	EnvelopeFrom string
	EnvelopeTo   string
	DeliveryID   string
	SourceHost   string
	DeliveryMeta string
}

// DeliverInbound injects one inbound message through the routing ladder.
func (c *Client) DeliverInbound(ctx context.Context, opts InboundOptions, getBody func() (io.ReadCloser, error), bodyLen int64) (*DeliverResult, []byte, error) {
	var out DeliverResult
	raw, err := c.doJSONRaw(ctx, request{
		method: http.MethodPost, path: "/deliver/inbound",
		getBody: getBody, bodyLen: bodyLen, contentType: "message/rfc822",
		headers:    deliverHeaders(opts.EnvelopeFrom, opts.EnvelopeTo, opts.DeliveryID, opts.SourceHost, opts.DeliveryMeta),
		idempotent: true,
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// CheckRecipient runs the RCPT pre-flight. Returns true on 200 accepted; a
// rejection surfaces as an APIError (404 unknown_address / 403 receiving_disabled).
func (c *Client) CheckRecipient(ctx context.Context, to, from, ip, ptr, helo string) (bool, error) {
	q := url.Values{}
	q.Set("to", to)
	for k, v := range map[string]string{"from": from, "ip": ip, "ptr": ptr, "helo": helo} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: "/deliver/check", query: q, idempotent: true,
	}, &out)
	if err != nil {
		return false, err
	}
	return out.Accepted, nil
}
