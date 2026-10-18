package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func dnsTestClient(t *testing.T, baseURL string) *coreapi.Client {
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

// A concurrent add that loses the create race must ADOPT the winner's list,
// not surface a raw 409. The first (filtered) find sees nothing, the create
// 409s, and the re-read by name at scope finds the freshly-created
// outbound/block list.
func TestDoNotSendList_AdoptsRaceWinner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"list_name_taken"}`))
		case r.Method == http.MethodGet && r.URL.Query().Get("direction") == "outbound":
			// Initial filtered find: the race winner is not visible yet.
			_, _ = w.Write([]byte(`{"lists":[]}`))
		default:
			// Re-read by name at scope: the winner is here now.
			_, _ = w.Write([]byte(`{"lists":[{"id":"L_WON","name":"Do not send","scopeKind":"account","scopeId":"acc_1","direction":"outbound","verdict":"block","createdAt":1,"updatedAt":1}]}`))
		}
	}))
	defer srv.Close()

	_, list, err := doNotSendList(context.Background(), dnsTestClient(t, srv.URL), "acc_1", true)
	if err != nil {
		t.Fatalf("expected the race winner to be adopted, got error: %v", err)
	}
	if list == nil || list.ID != "L_WON" {
		t.Fatalf("expected adopted list L_WON, got %+v", list)
	}
}

// A same-named list created for a DIFFERENT purpose must produce a clear,
// actionable error — never a silent write into an inbound/allow list, and never
// a cryptic looping 409.
func TestDoNotSendList_RefusesPurposeCollision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"list_name_taken"}`))
		case r.Method == http.MethodGet && r.URL.Query().Get("direction") == "outbound":
			_, _ = w.Write([]byte(`{"lists":[]}`))
		default:
			// The colliding list is inbound/allow — wrong purpose.
			_, _ = w.Write([]byte(`{"lists":[{"id":"L_WRONG","name":"Do not send","scopeKind":"account","scopeId":"acc_1","direction":"inbound","verdict":"allow","createdAt":1,"updatedAt":1}]}`))
		}
	}))
	defer srv.Close()

	_, list, err := doNotSendList(context.Background(), dnsTestClient(t, srv.URL), "acc_1", true)
	if err == nil {
		t.Fatalf("expected a purpose-collision error, got list %+v", list)
	}
	if !strings.Contains(err.Error(), "different purpose") {
		t.Fatalf("error should explain the collision, got: %v", err)
	}
}

// helpText renders a command's --help with whitespace folded, so a phrase
// survives the line wrapping of the Long text.
func helpText(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(append(args, "--help"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

// Core judges an account's outbound lists at SUBMISSION and on every forward
// the account configures (core b19bdfd1, address-lists item 14); the relay is
// only the backstop for mail queued before the address was listed. The help
// predated that and told users the opposite on both counts (cli#49).
func TestDoNotSendHelpSaysWhereCoreEnforces(t *testing.T) {
	help := helpText(t, newSuppressionsCmd(&app{}))
	for _, want := range []string{
		"refused at submission", "recipient_not_permitted", "relay_suppressed",
		"forward-all", "Sieve redirects", "relaying routes", "external group members",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("do-not-send help lacks %q:\n%s", want, help)
		}
	}
	for _, stale := range []string{"does not judge forwarding", "accepted at submission", "last hop"} {
		if strings.Contains(help, stale) {
			t.Errorf("do-not-send help still says %q:\n%s", stale, help)
		}
	}
	check := helpText(t, newSuppressionsCmd(&app{}), "check")
	if !strings.Contains(check, "submission and the relay") {
		t.Errorf("do-not-send check help does not say the evaluator also runs at submission:\n%s", check)
	}
}
