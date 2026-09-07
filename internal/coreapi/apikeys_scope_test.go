package coreapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// The domain scope on the wire (core migration 0078).
//
// Two properties, and both are about a field that must not be invented. A
// scope the caller asked for has to reach core spelled the way core reads it,
// and a scope core did NOT record has to come back as nothing — never as the
// list that was requested — because the whole fail-closed rule in `keys create`
// is built on telling those two apart.
func TestCreateAPIKeySendsDomainScope(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "key_1", "name": "delegate", "token": "oek_x",
			"domains": []string{"a.test", "b.test"},
		})
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).CreateAPIKey(context.Background(), CreateKeyOptions{
		Name:    "delegate",
		Domains: []string{"a.test", "b.test"},
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	domains, _ := body["domains"].([]any)
	if len(domains) != 2 || domains[0] != "a.test" {
		t.Fatalf("domains not sent: %v", body["domains"])
	}
	// `role`/`accountId` stay OFF the wire when empty: core forces an account
	// caller to its own account, and sending "" would be a value where the
	// contract says absent.
	if _, ok := body["role"]; ok {
		t.Fatalf("empty role should be omitted, got %v", body)
	}
	if len(got.Domains) != 2 {
		t.Fatalf("echo not parsed: %v", got.Domains)
	}
}

// An UNSCOPED mint sends no `domains` at all. `[]` would be a key that reaches
// nothing, which core refuses — "the whole account" is the absence of the field.
func TestCreateAPIKeyOmitsEmptyScope(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "key_1", "name": "wide", "token": "oek_x"})
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).CreateAPIKey(context.Background(), CreateKeyOptions{Name: "wide"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if _, ok := body["domains"]; ok {
		t.Fatalf("empty scope should be omitted, got %v", body)
	}
	// The echo a caller checks: nil, not an empty slice standing in for one.
	if got.Domains != nil {
		t.Fatalf("want nil echo for an unscoped mint, got %v", got.Domains)
	}
}

// ignoringCore is a core that predates 0078: it takes `domains`, strips it,
// and answers 200 with an ACCOUNT-WIDE key. Its revoke answers `revokeStatus`
// (0 for success) and records what it was asked to revoke.
func ignoringCore(t *testing.T, revokeStatus int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var revoked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			mu.Lock()
			revoked = append(revoked, path.Base(r.URL.Path))
			mu.Unlock()
			if revokeStatus != 0 {
				w.WriteHeader(revokeStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "internal_error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"revoked": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "key_1", "name": "delegate", "token": "oek_x"})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), revoked...)
	}
}

// THE PROPERTY THE CLIENT HOLDS FOR EVERY MINTER. A core that ignores the
// scope hands back a key over the whole account; the client must not pass it
// on — even to a caller that would check — because the flag command and the
// TUI form both mint through here, and a check either could forget is a check
// one of them eventually will. So the key is revoked and the mint fails with
// an error that says so. This is the case that cannot be recovered from later.
func TestCreateAPIKeyRevokesAnIgnoredScope(t *testing.T) {
	srv, revoked := ignoringCore(t, 0)

	got, err := testClient(t, srv.URL).CreateAPIKey(context.Background(), CreateKeyOptions{
		Name:    "delegate",
		Domains: []string{"a.test"},
	})
	var unscoped *ScopeNotRecordedError
	if !errors.As(err, &unscoped) {
		t.Fatalf("want ScopeNotRecordedError, got err=%v key=%+v", err, got)
	}
	if got != nil {
		t.Fatalf("a mis-minted key must not be returned alongside the error: %+v", got)
	}
	if unscoped.KeyID != "key_1" || unscoped.RevokeErr != nil {
		t.Fatalf("error should name the key and a clean revoke: %+v", unscoped)
	}
	if !strings.Contains(err.Error(), "unscoped") {
		t.Fatalf("error should name the cause, got %v", err)
	}
	if r := revoked(); len(r) != 1 || r[0] != "key_1" {
		t.Fatalf("revoked %v, want [key_1] — the key exists on the account until it is", r)
	}
}

// A revoke that fails leaves a live key nobody asked for, so the error carries
// the id — from here on only a human can finish the job.
func TestCreateAPIKeyNamesTheKeyItCouldNotRevoke(t *testing.T) {
	srv, revoked := ignoringCore(t, http.StatusInternalServerError)

	_, err := testClient(t, srv.URL).CreateAPIKey(context.Background(), CreateKeyOptions{
		Name:    "delegate",
		Domains: []string{"a.test"},
	})
	var unscoped *ScopeNotRecordedError
	if !errors.As(err, &unscoped) {
		t.Fatalf("want ScopeNotRecordedError, got %v", err)
	}
	if unscoped.RevokeErr == nil || unscoped.KeyID != "key_1" {
		t.Fatalf("want a failed revoke naming key_1, got %+v", unscoped)
	}
	if !strings.Contains(err.Error(), "key_1") || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("error must name the stranded key and say what to do, got %v", err)
	}
	if r := revoked(); len(r) != 1 {
		t.Fatalf("the revoke must have been attempted, got %v", r)
	}
}

// The three wire cases of a key's scope, kept apart all the way to the cell —
// and in particular `[]` is not `absent`: the first is a key whose stored
// scope core cannot read (it refuses to authenticate), the second is a key
// over the whole account. Folding them would show a dead key as the widest one
// on the list.
func TestScopeLabelKeepsTheThreeWireCasesApart(t *testing.T) {
	for _, tc := range []struct {
		wire string
		want string
	}{
		{`{}`, "all"},
		{`{"domains":null}`, "all"},
		{`{"domains":[]}`, "none"},
		{`{"domains":["a.test"]}`, "a.test"},
		{`{"domains":["a.test","b.test"]}`, "a.test, b.test"},
		{`{"domains":["a.test","b.test","c.test"]}`, "a.test, b.test +1 more"},
	} {
		var k APIKey
		if err := json.Unmarshal([]byte(tc.wire), &k); err != nil {
			t.Fatal(err)
		}
		if got := k.ScopeLabel(2); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.wire, got, tc.want)
		}
	}
	// And the detail form — shown = the whole list — spells every domain out.
	k := APIKey{Domains: []string{"a.test", "b.test", "c.test"}}
	if got := k.ScopeLabel(len(k.Domains)); got != "a.test, b.test, c.test" {
		t.Fatalf("full label = %q", got)
	}
}

// `whoami` carries the scope so a holder can DISCOVER what its key reaches
// instead of inferring it from a trail of 404s.
func TestWhoamiCarriesDomainScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "account", "accountId": "acc_1", "domains": []string{"a.test"},
		})
	}))
	defer srv.Close()

	p, err := testClient(t, srv.URL).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(p.Domains) != 1 || p.Domains[0] != "a.test" {
		t.Fatalf("scope not carried onto the principal: %v", p.Domains)
	}
}

// And an unscoped key says nothing, rather than an empty list that would read
// as "reaches nothing".
func TestWhoamiUnscopedKeyHasNoDomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "account", "accountId": "acc_1"})
	}))
	defer srv.Close()

	p, err := testClient(t, srv.URL).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Domains != nil {
		t.Fatalf("want nil, got %v", p.Domains)
	}
}
