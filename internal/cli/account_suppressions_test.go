package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
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
