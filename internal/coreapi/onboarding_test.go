package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Verified domain onboarding (openemail-core docs/domain-onboarding-design.md).
// A domain row exists only for a domain the account proved it controls, and
// sending is EARNED from live DNS rather than asserted by the caller — so the
// client's job is to carry the proof instructions and the lifecycle faithfully.

// CreateDomain is create-or-advance: the same call creates a verified domain and,
// on a later call, activates sending. Both halves of the body must survive.
func TestCreateDomainReturnsDomainAndRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/domains" {
			t.Errorf("hit %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{
			"domain":"a.test","enabled":true,"canSend":false,"canReceive":true,
			"aliasOf":null,"fbl":false,"dmarc":false,"jmap":false,
			"dnsStatus":{"mx":true,"spf":false,"dkim":false,"dmarc":false},
			"dnsCheckedAt":1700000000,"accountId":"ACC","createdAt":1700000000,
			"receiving":true,"sending":false,
			"records":[
				{"kind":"mx","type":"MX","name":"a.test","value":"mx.open.email","priority":10,
				 "purpose":"Receive mail","required":true,"ok":true,"found":["mx.open.email"]},
				{"kind":"spf","type":"CNAME","name":"oe-bounce.a.test","value":"oe-bounce.open.email",
				 "purpose":"Bounce domain","required":true,"ok":false,"found":[]}
			]}`))
	}))
	defer srv.Close()

	d, records, err := testClient(t, srv.URL).CreateDomain(context.Background(), DomainCreateInput{Domain: "a.test"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Receiving || d.Sending {
		t.Fatalf("lifecycle mis-decoded: %+v", d)
	}
	if d.Domain != "a.test" {
		t.Fatalf("domain name mis-decoded: %q", d.Domain)
	}
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %d", len(records))
	}
	// The embedded DNSRecord must flatten — a nested object here would mean the
	// name and value a customer has to publish arrive empty.
	if records[0].Name != "a.test" || records[0].Kind != "mx" {
		t.Fatalf("record fields did not flatten: %+v", records[0])
	}
	if records[0].Priority == nil || *records[0].Priority != 10 {
		t.Fatalf("priority mis-decoded: %+v", records[0].Priority)
	}
	if records[0].OK == nil || !*records[0].OK {
		t.Fatalf("liveness mis-decoded: %+v", records[0].OK)
	}
	if records[1].OK == nil || *records[1].OK {
		t.Fatalf("want spf ok=false, got %+v", records[1].OK)
	}
}

// The refusal IS the instructions: a first-run caller must be able to render the
// exact record without deriving its name or the account token itself.
func TestVerificationRecordExtractsTheRefusalRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"domain_not_verified","record":{
			"kind":"ownership","type":"TXT","name":"_openemail.a.test",
			"value":"openemail-verification=abc123","purpose":"Prove you control this domain",
			"required":true}}`))
	}))
	defer srv.Close()

	_, _, err := testClient(t, srv.URL).CreateDomain(context.Background(), DomainCreateInput{Domain: "a.test"})
	if err == nil {
		t.Fatal("want an error")
	}
	rec, ok := VerificationRecord(err)
	if !ok {
		t.Fatalf("record not extracted from %v", err)
	}
	if rec.Name != "_openemail.a.test" || rec.Value != "openemail-verification=abc123" {
		t.Fatalf("bad record: %+v", rec)
	}
	if rec.Type != "TXT" || !rec.Required {
		t.Fatalf("bad record: %+v", rec)
	}
}

// Only that one code carries a record; nothing else may be mistaken for it.
func TestVerificationRecordIgnoresOtherErrors(t *testing.T) {
	for _, body := range []string{
		`{"error":"domain_claimed_elsewhere"}`,
		`{"error":"domain_unavailable"}`,
		`{"error":"validation_failed","issues":[]}`,
		// Right code, no record (a core that changed shape) must not panic.
		`{"error":"domain_not_verified"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(body))
		}))
		_, _, err := testClient(t, srv.URL).CreateDomain(context.Background(), DomainCreateInput{Domain: "a.test"})
		if _, ok := VerificationRecord(err); ok {
			t.Fatalf("%s wrongly yielded a record", body)
		}
		srv.Close()
	}
	// A non-API error (transport failure) must be inert too.
	if _, ok := VerificationRecord(io.EOF); ok {
		t.Fatal("non-API error yielded a record")
	}
}

func TestCheckDomainDNSPathAndForce(t *testing.T) {
	var gotMethod, gotPath, gotForce string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotForce = r.URL.Query().Get("force")
		w.Write([]byte(`{"domain":"a.test","checkedAt":null,"cached":false,
			"resolverUnavailable":true,"status":{},
			"records":[{"kind":"dkim","type":"CNAME","name":"oe1._domainkey.a.test",
			 "value":"oe1.dkim.open.email","purpose":"Sign mail","required":true,"found":[]}]}`))
	}))
	defer srv.Close()

	res, err := testClient(t, srv.URL).CheckDomainDNS(context.Background(), "a.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/domains/a.test/dns/check" {
		t.Fatalf("hit %s %s", gotMethod, gotPath)
	}
	if gotForce != "true" {
		t.Fatalf("force not sent: %q", gotForce)
	}
	if !res.ResolverUnavailable {
		t.Fatal("resolverUnavailable lost")
	}
	// UNKNOWN liveness must stay nil. Decoding it as false would report the
	// customer's DNS as broken during one of our own resolver outages.
	if res.Records[0].OK != nil {
		t.Fatalf("absent ok decoded as %v, want nil", *res.Records[0].OK)
	}
	if res.CheckedAt != nil {
		t.Fatalf("null checkedAt decoded as %v", *res.CheckedAt)
	}
}

func TestCheckDomainDNSOmitsForceWhenFalse(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		w.Write([]byte(`{"domain":"a.test","checkedAt":1,"cached":true,"status":{},"records":[]}`))
	}))
	defer srv.Close()

	if _, err := testClient(t, srv.URL).CheckDomainDNS(context.Background(), "a.test", false); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Fatalf("unexpected query %q", raw)
	}
}

// The token is per-account and stable; it must survive as a pointer so "absent"
// (a system key, or a core that did not send one) stays distinguishable from "".
func TestAccountCarriesVerificationToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"ACC","name":"a","maxMailboxes":null,
			"verificationToken":"abc123","createdAt":1}`))
	}))
	defer srv.Close()

	acct, err := testClient(t, srv.URL).GetAccount(context.Background(), "ACC")
	if err != nil {
		t.Fatal(err)
	}
	if acct.VerificationToken == nil || *acct.VerificationToken != "abc123" {
		t.Fatalf("token mis-decoded: %+v", acct.VerificationToken)
	}

	null := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"ACC","name":"a","maxMailboxes":null,
			"verificationToken":null,"createdAt":1}`))
	}))
	defer null.Close()
	acct, err = testClient(t, null.URL).GetAccount(context.Background(), "ACC")
	if err != nil {
		t.Fatal(err)
	}
	if acct.VerificationToken != nil {
		t.Fatalf("null token decoded as %q", *acct.VerificationToken)
	}
}

// DomainCreateInput must keep marshalling the explicit accountId:null a platform
// create needs — verification did not change that contract.
func TestPlatformCreateStillMarshalsNullAccount(t *testing.T) {
	b, err := json.Marshal(DomainCreateInput{Domain: "a.test", Platform: true})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	v, present := m["accountId"]
	if !present || v != nil {
		t.Fatalf("want explicit null accountId, got %v (present=%v)", v, present)
	}
}
