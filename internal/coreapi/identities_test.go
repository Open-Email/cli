package coreapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GetIdentity decodes the facet map; an absent facet key stays nil.
func TestGetIdentityMailless(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"id":"01I","primaryAddress":null,"quotaBytes":null,"accountId":"acc1","createdAt":5,"facets":{"pim":{"storeId":"01I","provisioned":true,"collections":2,"objects":40,"bytes":98304}}}`))
	}))
	defer srv.Close()
	id, err := m3Client(t, srv.URL).GetIdentity(context.Background(), "01I")
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if gotPath != "/api/v1/identities/01I" {
		t.Fatalf("path = %q", gotPath)
	}
	if id.PrimaryAddress != nil || id.Facets.Mail != nil {
		t.Fatalf("mail-less identity decoded a mail side: %+v", id)
	}
	if id.Facets.Pim == nil || id.Facets.Pim.Objects != 40 || !id.Facets.Pim.Provisioned {
		t.Fatalf("pim facet: %+v", id.Facets.Pim)
	}
}

// Resolve prefers /auth/whoami: one call, no probes, exact ids.
func TestResolveUsesWhoami(t *testing.T) {
	probes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/whoami":
			w.Write([]byte(`{"type":"mailbox","keyId":null,"accountId":"acc1","mailboxId":"01M","credentialId":"01Q"}`))
		default:
			probes++
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, Token: "oemp_x", RetryBackoffMin: time.Millisecond})
	p, err := c.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Type != PrincipalMailbox || p.MailboxID != "01M" || p.CredentialID != "01Q" || p.AccountID != "acc1" {
		t.Fatalf("principal: %+v", p)
	}
	if probes != 0 {
		t.Fatalf("Resolve probed %d time(s) despite a whoami answer", probes)
	}
}

// A 401 from whoami is the caller's answer — no probes can improve on it.
func TestResolveWhoami401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid_key"}`))
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL, Token: "oek_bad", RetryBackoffMin: time.Millisecond})
	if _, err := c.Resolve(context.Background()); !IsUnauthorized(err) {
		t.Fatalf("want 401 through, got %v", err)
	}
}

// VerifyLogin forwards the optional client IP for the per-client throttle
// dimension, and omits the header without one.
func TestVerifyLoginClientIP(t *testing.T) {
	var gotIP []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIP = append(gotIP, r.Header.Get("X-Client-Ip"))
		w.Write([]byte(`{"identityId":"01I","mailboxId":"01I","accountId":null,"credentialId":"c1","kind":"password","canSend":true,"permittedFrom":["a@x"],"facets":["mail","pim"]}`))
	}))
	defer srv.Close()
	c := m3Client(t, srv.URL)
	if _, err := c.VerifyLogin(context.Background(), "a@x", "pw", "203.0.113.9"); err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	if _, err := c.VerifyLogin(context.Background(), "a@x", "pw", ""); err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	if len(gotIP) != 2 || gotIP[0] != "203.0.113.9" || gotIP[1] != "" {
		t.Fatalf("X-Client-Ip: %v", gotIP)
	}
}
