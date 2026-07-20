package coreapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetContentDecodes(t *testing.T) {
	body := `{
		"headers": {
			"from": [{"name": "Acme Billing", "email": "billing@acme.com"}],
			"to": [{"name": null, "email": "alice@example.com"}],
			"cc": [],
			"replyTo": [],
			"subject": "Invoice",
			"date": 1784332800,
			"messageIdHeader": "<abc@acme.com>",
			"inReplyTo": null
		},
		"text": {"section": "1", "content": "hi", "size": 2, "truncated": false, "charset": "utf-8"},
		"html": null,
		"attachments": [
			{"section": "2", "filename": "invoice.pdf", "contentType": "application/pdf", "size": 35102, "inline": false, "contentId": null},
			{"section": "3", "filename": "part-3", "contentType": "text/plain", "size": 10, "inline": false, "contentId": null, "degraded": true}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/m1/content") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := m3Client(t, srv.URL).GetContent(context.Background(), "mbx", "m1")
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if c.Headers.From[0].Name == nil || *c.Headers.From[0].Name != "Acme Billing" {
		t.Errorf("from name: %+v", c.Headers.From)
	}
	if c.Headers.To[0].Name != nil {
		t.Errorf("to name should be nil, got %v", *c.Headers.To[0].Name)
	}
	if c.Headers.Date == nil || *c.Headers.Date != 1784332800 {
		t.Errorf("date: %v", c.Headers.Date)
	}
	if c.Headers.InReplyTo != nil {
		t.Errorf("inReplyTo should be nil")
	}
	if c.Text == nil || c.Text.Content == nil || *c.Text.Content != "hi" {
		t.Errorf("text: %+v", c.Text)
	}
	if c.HTML != nil {
		t.Errorf("html should be nil")
	}
	if len(c.Attachments) != 2 || c.Attachments[0].Section != "2" || c.Attachments[0].Filename != "invoice.pdf" {
		t.Errorf("attachments: %+v", c.Attachments)
	}
	if !c.Attachments[1].Degraded {
		t.Errorf("attachment 3 should be degraded")
	}
}

func TestGetPartStreamsWithHeaders(t *testing.T) {
	want := "decoded pdf bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/m1/parts/3.1") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("raw") != "true" {
			t.Errorf("raw query = %q, want true", r.URL.Query().Get("raw"))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="invoice.pdf"`)
		w.Write([]byte(want))
	}))
	defer srv.Close()

	rc, hdr, err := m3Client(t, srv.URL).GetPart(context.Background(), "mbx", "m1", "3.1", true)
	if err != nil {
		t.Fatalf("GetPart: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body: got %q want %q", got, want)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, "invoice.pdf") {
		t.Errorf("Content-Disposition header not surfaced: %q", cd)
	}
}
