package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// EventWebhook is one scope's event-webhook resource (docs/events-design.md
// §XI): the endpoint URL and its delivery status. The signing secret is
// write-only — HasSecret is all a read ever says about it.
type EventWebhook struct {
	Scope     string `json:"scope"`
	MailboxID string `json:"mailboxId,omitempty"`
	Domain    string `json:"domain,omitempty"`
	URL       string `json:"url"`
	HasSecret bool   `json:"hasSecret"`
	Enabled   bool   `json:"enabled"`
	// DisabledReason is "failing" after the 24h auto-disable; any PUT re-enables.
	DisabledReason      *string `json:"disabledReason"`
	FailingSince        *int64  `json:"failingSince"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	LastDeliveredAt     *int64  `json:"lastDeliveredAt"`
	LastFailureAt       *int64  `json:"lastFailureAt"`
	LastFailure         *string `json:"lastFailure"`
	CreatedAt           int64   `json:"createdAt"`
	UpdatedAt           int64   `json:"updatedAt"`
}

// EventWebhookInput is the PUT body. The secret is THREE-WAY on the wire:
// omitted keeps the stored one, a string rotates it, JSON null clears it.
// Go cannot spell "absent" and "null" with one pointer, so ClearSecret is the
// explicit third state; it wins over Secret when both are set.
type EventWebhookInput struct {
	URL         string
	Secret      *string
	ClearSecret bool
}

func (in EventWebhookInput) MarshalJSON() ([]byte, error) {
	body := map[string]any{"url": in.URL}
	switch {
	case in.ClearSecret:
		body["secret"] = nil
	case in.Secret != nil:
		body["secret"] = *in.Secret
	}
	return json.Marshal(body)
}

// EventWebhookTestResult is the test verb's 202: the queued batch's id. Its
// outcome shows on the resource (LastDeliveredAt / LastFailureAt) and in the
// traffic log.
type EventWebhookTestResult struct {
	BatchID string `json:"batchId"`
}

func mailboxEventWebhookPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/event-webhook"
}

func domainEventWebhookPath(domain string) string {
	return "/domains/" + escapeSegment(domain) + "/event-webhook"
}

func (c *Client) getEventWebhook(ctx context.Context, path string) (*EventWebhook, error) {
	var out EventWebhook
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: path, idempotent: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) putEventWebhook(ctx context.Context, path string, in EventWebhookInput) (*EventWebhook, error) {
	var out EventWebhook
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: path, body: mustJSON(in), contentType: "application/json",
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) testEventWebhook(ctx context.Context, path string) (*EventWebhookTestResult, error) {
	var out EventWebhookTestResult
	if err := c.doJSON(ctx, request{method: http.MethodPost, path: path + "/test"}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetMailboxEventWebhook reads the mailbox-scope hook. 404 event_webhook_not_found when none is set.
func (c *Client) GetMailboxEventWebhook(ctx context.Context, mailboxID string) (*EventWebhook, error) {
	return c.getEventWebhook(ctx, mailboxEventWebhookPath(mailboxID))
}

// PutMailboxEventWebhook creates or replaces the mailbox-scope hook. Any PUT
// re-enables a hook the 24h auto-disable turned off.
func (c *Client) PutMailboxEventWebhook(ctx context.Context, mailboxID string, in EventWebhookInput) (*EventWebhook, error) {
	return c.putEventWebhook(ctx, mailboxEventWebhookPath(mailboxID), in)
}

// DeleteMailboxEventWebhook removes the mailbox-scope hook.
func (c *Client) DeleteMailboxEventWebhook(ctx context.Context, mailboxID string) error {
	return c.doJSON(ctx, request{method: http.MethodDelete, path: mailboxEventWebhookPath(mailboxID)}, nil)
}

// TestMailboxEventWebhook queues a `webhook.test` batch through the real queue.
func (c *Client) TestMailboxEventWebhook(ctx context.Context, mailboxID string) (*EventWebhookTestResult, error) {
	return c.testEventWebhook(ctx, mailboxEventWebhookPath(mailboxID))
}

// GetDomainEventWebhook reads the domain-scope hook.
func (c *Client) GetDomainEventWebhook(ctx context.Context, domain string) (*EventWebhook, error) {
	return c.getEventWebhook(ctx, domainEventWebhookPath(domain))
}

// PutDomainEventWebhook creates or replaces the domain-scope hook. The
// response is not a promise about mutations already in flight: take a
// baseline (each mailbox's /messages/changes state) after it returns.
func (c *Client) PutDomainEventWebhook(ctx context.Context, domain string, in EventWebhookInput) (*EventWebhook, error) {
	return c.putEventWebhook(ctx, domainEventWebhookPath(domain), in)
}

// DeleteDomainEventWebhook removes the domain-scope hook.
func (c *Client) DeleteDomainEventWebhook(ctx context.Context, domain string) error {
	return c.doJSON(ctx, request{method: http.MethodDelete, path: domainEventWebhookPath(domain)}, nil)
}

// TestDomainEventWebhook queues a `webhook.test` batch for the domain hook.
func (c *Client) TestDomainEventWebhook(ctx context.Context, domain string) (*EventWebhookTestResult, error) {
	return c.testEventWebhook(ctx, domainEventWebhookPath(domain))
}
