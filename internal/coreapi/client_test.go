package coreapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:         baseURL,
		Token:           "oek_test",
		RetryBackoffMin: time.Millisecond,
		RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestErrorEnvelopeDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"validation_failed","issues":[{"path":"query.limit","code":"too_big","message":"too big"}],"maxMailboxes":5}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).ListAPIKeys(context.Background(), 0, "")
	ae, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("want APIError, got %v", err)
	}
	if ae.Status != 400 || ae.Code != "validation_failed" {
		t.Fatalf("status/code: %d %q", ae.Status, ae.Code)
	}
	if !IsValidation(err) {
		t.Fatal("IsValidation false")
	}
	if len(ae.Issues) != 1 || ae.Issues[0].Path != "query.limit" {
		t.Fatalf("issues: %+v", ae.Issues)
	}
	if ae.Extra["maxMailboxes"] == nil {
		t.Fatalf("extra not preserved: %+v", ae.Extra)
	}
}

func TestRetryOn503ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"reaped"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"apiKeys":[],"nextCursor":""}`))
	}))
	defer srv.Close()

	if _, err := testClient(t, srv.URL).ListAPIKeys(context.Background(), 0, ""); err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
}

func TestNoRetryOn503ForNonIdempotentCreate(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"reaped"}`))
	}))
	defer srv.Close()

	// A POST create carries no idempotency key; a 503 could be post-commit, so it
	// must NOT be replayed (would duplicate the resource).
	_, err := testClient(t, srv.URL).CreateMailbox(context.Background(), MailboxCreateInput{})
	if Status(err) != 503 {
		t.Fatalf("want 503, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("non-idempotent 503 must not retry: %d attempts", got)
	}
}

func TestDepaginateStallGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"mailboxes":[{"id":"x"}],"nextCursor":"stuck"}`)) // never advances
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	_, err := Depaginate(context.Background(), func(ctx context.Context, cur string) (Page[Mailbox], error) {
		return c.ListMailboxes(ctx, 0, cur)
	})
	if err == nil {
		t.Fatal("want a stall error, got nil (would loop forever in production)")
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).GetMailbox(context.Background(), "x")
	if !IsNotFound(err) {
		t.Fatalf("want 404, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("4xx must not retry: %d attempts", got)
	}
}

func TestRawPathNotDoubleEncoded(t *testing.T) {
	var escapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escapedPath = r.URL.EscapedPath()
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"x","primaryAddress":null,"quotaBytes":null,"accountId":null,"createdAt":0}`))
	}))
	defer srv.Close()

	// An id with a space and a percent: PathEscape → "a%20b%25c". The kernel must
	// publish that verbatim (RawPath), not re-escape to "a%2520b%2525c".
	if _, err := testClient(t, srv.URL).GetMailbox(context.Background(), "a b%c"); err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if want := "/api/v1/mailboxes/a%20b%25c"; escapedPath != want {
		t.Fatalf("escaped path\n got %q\nwant %q", escapedPath, want)
	}
}

func TestDepaginate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"mailboxes":[{"id":"1"},{"id":"2"}],"nextCursor":"c2"}`))
		} else {
			w.Write([]byte(`{"mailboxes":[{"id":"3"}]}`))
		}
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	all, err := Depaginate(context.Background(), func(ctx context.Context, cur string) (Page[Mailbox], error) {
		return c.ListMailboxes(ctx, 0, cur)
	})
	if err != nil {
		t.Fatalf("Depaginate: %v", err)
	}
	if len(all) != 3 || all[0].ID != "1" || all[2].ID != "3" {
		t.Fatalf("got %+v", all)
	}
}

func TestResolveClassifiesPrincipals(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		handler http.HandlerFunc
		want    string
		wantAcc string
	}{
		{
			name:  "account",
			token: "oek_acct",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/api-keys"):
					w.Write([]byte(`{"apiKeys":[{"id":"k","name":"n","role":"account","accountId":"acc1","createdAt":1}]}`))
				case strings.HasSuffix(r.URL.Path, "/accounts"):
					w.WriteHeader(403)
					w.Write([]byte(`{"error":"system_credentials_required"}`))
				}
			},
			want:    PrincipalAccount,
			wantAcc: "acc1",
		},
		{
			name:  "system",
			token: "oek_sys",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/accounts") {
					w.Write([]byte(`{"accounts":[]}`))
					return
				}
				w.Write([]byte(`{"apiKeys":[]}`))
			},
			want: PrincipalSystem,
		},
		{
			name:  "mailbox",
			token: "oemp_alice",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(403)
				w.Write([]byte(`{"error":"insufficient_scope"}`))
			},
			want: PrincipalMailbox,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c, _ := New(Config{BaseURL: srv.URL, Token: tc.token, RetryBackoffMin: time.Millisecond})
			id, err := c.Resolve(context.Background())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if id.Type != tc.want || id.AccountID != tc.wantAcc {
				t.Fatalf("got %q/%q want %q/%q", id.Type, id.AccountID, tc.want, tc.wantAcc)
			}
		})
	}
}
