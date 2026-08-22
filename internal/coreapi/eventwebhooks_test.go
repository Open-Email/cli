package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The event-webhook client (docs/events-design.md §XIV): the eight calls, the
// three-way secret on the wire (omit keeps, string rotates, JSON null clears —
// never the empty string, which core would STORE as a secret), and the test
// verb's 202.
func TestEventWebhookClient(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &c.body)
			}
		}
		calls = append(calls, c)
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"batchId":"01BATCH"}`))
		default:
			w.Write([]byte(`{"scope":"mailbox","mailboxId":"m1","url":"https://recv.example/h","hasSecret":true,"enabled":true,"disabledReason":null,"failingSince":null,"consecutiveFailures":0,"lastDeliveredAt":1,"lastFailureAt":null,"lastFailure":null,"createdAt":1,"updatedAt":2}`))
		}
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ctx := context.Background()

	got, err := c.GetMailboxEventWebhook(ctx, "m1")
	if err != nil || got.URL != "https://recv.example/h" || !got.HasSecret {
		t.Fatalf("get: %v %+v", err, got)
	}
	secret := "s3"
	if _, err := c.PutMailboxEventWebhook(ctx, "m1", EventWebhookInput{URL: "https://recv.example/h", Secret: &secret}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := c.PutMailboxEventWebhook(ctx, "m1", EventWebhookInput{URL: "https://recv.example/h"}); err != nil {
		t.Fatalf("put keep: %v", err)
	}
	if _, err := c.PutMailboxEventWebhook(ctx, "m1", EventWebhookInput{URL: "https://recv.example/h", ClearSecret: true}); err != nil {
		t.Fatalf("put clear: %v", err)
	}
	if err := c.DeleteMailboxEventWebhook(ctx, "m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, err := c.TestMailboxEventWebhook(ctx, "m1")
	if err != nil || res.BatchID != "01BATCH" {
		t.Fatalf("test: %v %+v", err, res)
	}
	if _, err := c.GetDomainEventWebhook(ctx, "acme.example"); err != nil {
		t.Fatalf("domain get: %v", err)
	}
	if _, err := c.PutDomainEventWebhook(ctx, "acme.example", EventWebhookInput{URL: "https://recv.example/d"}); err != nil {
		t.Fatalf("domain put: %v", err)
	}
	if err := c.DeleteDomainEventWebhook(ctx, "acme.example"); err != nil {
		t.Fatalf("domain delete: %v", err)
	}
	if _, err := c.TestDomainEventWebhook(ctx, "acme.example"); err != nil {
		t.Fatalf("domain test: %v", err)
	}

	want := []string{
		"GET /api/v1/mailboxes/m1/event-webhook",
		"PUT /api/v1/mailboxes/m1/event-webhook",
		"PUT /api/v1/mailboxes/m1/event-webhook",
		"PUT /api/v1/mailboxes/m1/event-webhook",
		"DELETE /api/v1/mailboxes/m1/event-webhook",
		"POST /api/v1/mailboxes/m1/event-webhook/test",
		"GET /api/v1/domains/acme.example/event-webhook",
		"PUT /api/v1/domains/acme.example/event-webhook",
		"DELETE /api/v1/domains/acme.example/event-webhook",
		"POST /api/v1/domains/acme.example/event-webhook/test",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %d, want %d", len(calls), len(want))
	}
	for i, w := range want {
		if got := calls[i].method + " " + calls[i].path; got != w {
			t.Fatalf("call %d = %q, want %q", i, got, w)
		}
	}
	// The three-way secret on the wire.
	if v, ok := calls[1].body["secret"]; !ok || v != "s3" {
		t.Fatalf("rotate body = %v", calls[1].body)
	}
	if _, ok := calls[2].body["secret"]; ok {
		t.Fatalf("keep must OMIT secret: %v", calls[2].body)
	}
	if v, ok := calls[3].body["secret"]; !ok || v != nil {
		t.Fatalf("clear must send JSON null: %v", calls[3].body)
	}
}
