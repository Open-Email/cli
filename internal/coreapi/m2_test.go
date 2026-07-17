package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Domains return real booleans (core's present() converts 0/1); routes/patterns
// return paced as a raw 0/1 integer. Decoding must honor both.
func TestDomainDecodesBooleans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"domains":[{"domain":"x.example","enabled":true,"canSend":false,"canReceive":true,"fbl":false,"aliasOf":null,"dnsStatus":{"mx":true,"spf":false},"dnsCheckedAt":null,"accountId":"a1","createdAt":10}],"nextCursor":""}`))
	}))
	defer srv.Close()
	pg, err := testClient(t, srv.URL).ListDomains(context.Background(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	d := pg.Items[0]
	if !d.Enabled || d.CanSend || !d.CanReceive || d.FBL {
		t.Fatalf("bad bools: %+v", d)
	}
	if d.DNSStatus == nil || d.DNSStatus.MX == nil || !*d.DNSStatus.MX || *d.DNSStatus.SPF {
		t.Fatalf("bad dnsStatus: %+v", d.DNSStatus)
	}
}

func TestRouteDecodesPacedAsBool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"routes":[{"address":"u@x.example","domain":"x.example","destinationType":"mailbox","mailboxId":"m1","webhookUrl":null,"remoteAddress":null,"paced":true,"createdAt":5}],"nextCursor":""}`))
	}))
	defer srv.Close()
	pg, err := testClient(t, srv.URL).ListRoutes(context.Background(), "", "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	r := pg.Items[0]
	if !r.Paced || r.MailboxID == nil || *r.MailboxID != "m1" {
		t.Fatalf("bad route: %+v", r)
	}
}

func TestUpdateRouteSendsPatchVerbatim(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Write([]byte(`{"address":"u@x.example","domain":"x.example","destinationType":"mailbox","mailboxId":"m2","webhookUrl":null,"remoteAddress":null,"paced":false,"createdAt":5}`))
	}))
	defer srv.Close()
	_, err := testClient(t, srv.URL).UpdateRoute(context.Background(), "u@x.example",
		map[string]any{"destinationType": "mailbox", "mailboxId": "m2", "paced": true})
	if err != nil {
		t.Fatal(err)
	}
	if body["destinationType"] != "mailbox" || body["mailboxId"] != "m2" || body["paced"] != true {
		t.Fatalf("patch not sent verbatim: %+v", body)
	}
}

func TestListCredentialsUnpaginated(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"credentials":[{"id":"c1","kind":"app_password","username":"u@x.example","name":null,"createdAt":1,"lastUsedAt":null,"revokedAt":null}]}`))
	}))
	defer srv.Close()
	creds, err := testClient(t, srv.URL).ListCredentials(context.Background(), "mb+1")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].Kind != "app_password" {
		t.Fatalf("bad creds: %+v", creds)
	}
	// mailbox id with '+' must be escaped in the path, not double-encoded.
	if want := "/api/v1/mailboxes/mb+1/credentials"; gotPath != want {
		t.Fatalf("path %q want %q", gotPath, want)
	}
}
