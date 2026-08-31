package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func forwardingTestClient(t *testing.T, baseURL string) *coreapi.Client {
	t.Helper()
	c, err := coreapi.New(coreapi.Config{
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

func forwardingCmdCtx() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

const forwardingDoc = `{
	"destinations":[
		{"id":"01DEST","address":"me@elsewhere.test","state":"verified",
		 "codeExpiresAt":null,"verifiedAt":1700000000,"createdAt":1699999000}
	],
	"forwardAll":{"destinationId":null,"address":null,"paused":false}
}`

// An id is already the handle core wants, so resolving one must cost no call —
// a lookup there would only add a way to fail.
func TestResolveDestinationPassesIdsThrough(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(forwardingDoc))
	}))
	defer srv.Close()

	got, err := resolveDestination(forwardingCmdCtx(), forwardingTestClient(t, srv.URL), "01M", "01DEST")
	if err != nil {
		t.Fatalf("resolveDestination: %v", err)
	}
	if got != "01DEST" {
		t.Errorf("got %q, want the id unchanged", got)
	}
	if calls != 0 {
		t.Errorf("made %d call(s); an id needs no lookup", calls)
	}
}

// An address is what a person has in hand — core keys on the id precisely so no
// address is path-encoded, so the CLI does the lookup rather than making
// someone copy an id out of `show` first.
func TestResolveDestinationLooksUpAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mailboxes/01M/forwarding" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(forwardingDoc))
	}))
	defer srv.Close()

	// Case-insensitive: core lowercases the address it stores, and nobody types
	// their own address back in the same case twice.
	got, err := resolveDestination(forwardingCmdCtx(), forwardingTestClient(t, srv.URL), "01M", "Me@Elsewhere.test")
	if err != nil {
		t.Fatalf("resolveDestination: %v", err)
	}
	if got != "01DEST" {
		t.Errorf("got %q, want 01DEST", got)
	}
}

// An unknown address must name the next move, not just fail: the overwhelmingly
// likely cause is that the ceremony was never started for it.
func TestResolveDestinationUnknownAddressSaysWhatToDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(forwardingDoc))
	}))
	defer srv.Close()

	_, err := resolveDestination(forwardingCmdCtx(), forwardingTestClient(t, srv.URL), "01M", "nobody@elsewhere.test")
	if err == nil {
		t.Fatal("err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "forwarding add") {
		t.Errorf("err = %v; want it to name the add command", err)
	}
}

// `pending` is the state worth spelling out: it looks configured in a listing
// and forwards nothing.
func TestForwardingStateLabel(t *testing.T) {
	expires := int64(1700086400)
	for _, tc := range []struct {
		dest coreapi.ForwardingDestination
		want string
	}{
		{coreapi.ForwardingDestination{State: "verified"}, "verified"},
		{coreapi.ForwardingDestination{State: "revoked_by_recipient"}, "stopped by recipient"},
		{coreapi.ForwardingDestination{State: "pending"}, "awaiting code"},
		{coreapi.ForwardingDestination{State: "pending", CodeExpiresAt: &expires}, "awaiting code (expires"},
	} {
		got := forwardingStateLabel(tc.dest)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("state %q → %q, want prefix %q", tc.dest.State, got, tc.want)
		}
	}
}
