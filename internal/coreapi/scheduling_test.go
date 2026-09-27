package coreapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `admin scheduling` is what support reads when someone says an invitation
// never arrived: core's system read of one mailbox's calendar scheduling
// (docs/scheduling-durability-plan.md, "Failure visibility", reader C).
func TestGetMailboxScheduling(t *testing.T) {
	var gotPath, gotUID, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUID, gotLimit = r.URL.Path, r.URL.Query().Get("uid"), r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mailboxId":"M1",
			"pending":[{"id":7,"uid":"u1","state":"pending","attempts":2,"createdAt":10,"dueAt":20,"expiresAt":30,
				"lastError":"unfinished_recipients","recipients":3,"completed":2,"refused":0}],
			"recent":[{"id":6,"uid":"u0","state":"failed","attempts":1,"createdAt":5,"dueAt":null,"expiresAt":9,
				"lastError":"recipient_refused","recipients":null,"completed":null,"refused":null}],
			"failures":[{"id":"r1","uid":"u0","method":"REQUEST","counterpart":"a@x.test","outcome":"refused",
				"reason":"organizer_mismatch","status":"5.1","createdAt":6}]}`))
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).GetMailboxScheduling(context.Background(), "M 1", "u0", 25)
	if err != nil {
		t.Fatalf("GetMailboxScheduling: %v", err)
	}
	if gotPath != "/api/v1/system/mailboxes/M 1/scheduling" || gotUID != "u0" || gotLimit != "25" {
		t.Fatalf("asked core %q uid=%q limit=%q", gotPath, gotUID, gotLimit)
	}
	if len(got.Pending) != 1 || got.Pending[0].DueAt == nil || *got.Pending[0].DueAt != 20 ||
		got.Pending[0].Completed == nil || *got.Pending[0].Completed != 2 {
		t.Fatalf("pending job not decoded: %+v", got.Pending)
	}
	if len(got.Recent) != 1 || got.Recent[0].DueAt != nil || got.Recent[0].Recipients != nil {
		t.Fatalf("a finished job has no next attempt or recipients: %+v", got.Recent)
	}
	if len(got.Failures) != 1 || got.Failures[0].Status == nil || *got.Failures[0].Status != "5.1" ||
		got.Failures[0].Counterpart != "a@x.test" {
		t.Fatalf("failure not decoded: %+v", got.Failures)
	}
}
