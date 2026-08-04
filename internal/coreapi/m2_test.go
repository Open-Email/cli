package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// PurgeMailbox POSTs to /mailboxes/:id/purge and decodes {purged,restorable}.
func TestPurgeMailboxHitsPurgePath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"purged":true,"restorable":false}`))
	}))
	defer srv.Close()
	res, err := testClient(t, srv.URL).PurgeMailbox(context.Background(), "01ABC")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/mailboxes/01ABC/purge" {
		t.Fatalf("hit %s %s, want POST /api/v1/mailboxes/01ABC/purge", gotMethod, gotPath)
	}
	if !res.Purged || res.Restorable {
		t.Fatalf("bad decode: %+v", res)
	}
}

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

// Core's system-caller contract distinguishes an omitted accountId (400
// account_required) from an explicit null (platform domain) — so the three
// marshal shapes must stay distinct on the wire.
func TestDomainCreateMarshalsExplicitOwnership(t *testing.T) {
	body := func(in DomainCreateInput) map[string]json.RawMessage {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if v, ok := body(DomainCreateInput{Domain: "x.example", Platform: true})["accountId"]; !ok || string(v) != "null" {
		t.Fatalf("platform create must send an explicit accountId null, got %q (present=%v)", v, ok)
	}
	acc := "a1"
	if v := body(DomainCreateInput{Domain: "x.example", AccountID: &acc})["accountId"]; string(v) != `"a1"` {
		t.Fatalf("owned create must send the account id, got %q", v)
	}
	if _, ok := body(DomainCreateInput{Domain: "x.example"})["accountId"]; ok {
		t.Fatal("neither AccountID nor Platform: accountId must be omitted, not sent")
	}
}

func TestRouteDecodesPacedAsBool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"routes":[{"address":"u@x.example","domain":"x.example","destinationType":"mailbox","mailboxId":"m1","webhookUrl":null,"remoteAddress":null,"paced":true,"createdAt":5}],"nextCursor":""}`))
	}))
	defer srv.Close()
	pg, err := testClient(t, srv.URL).ListRoutes(context.Background(), "", "", "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	r := pg.Items[0]
	if !r.Paced || r.MailboxID == nil || *r.MailboxID != "m1" {
		t.Fatalf("bad route: %+v", r)
	}
}

// The type filter must ride the query string (it is what makes core enrich the
// listing), and the group-only enrichment (posting, memberCount) must decode.
func TestListRoutesTypeFilterEnrichment(t *testing.T) {
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.URL.Query().Get("type")
		w.Write([]byte(`{"routes":[{"address":"team@x.example","domain":"x.example","destinationType":"group","mailboxId":null,"webhookUrl":null,"remoteAddress":null,"paced":false,"createdAt":5,"posting":"open","memberCount":3}],"nextCursor":""}`))
	}))
	defer srv.Close()
	pg, err := testClient(t, srv.URL).ListRoutes(context.Background(), "", "", "group", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "group" {
		t.Fatalf("type param = %q, want group", gotType)
	}
	r := pg.Items[0]
	if r.Posting != "open" || r.MemberCount == nil || *r.MemberCount != 3 {
		t.Fatalf("enrichment lost in decode: %+v", r)
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
