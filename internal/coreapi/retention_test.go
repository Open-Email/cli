package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The retention client: the preview ladder rides the GET as `?days=`, the two
// writes carry exactly one integer, and the account read passes days/limit/
// cursor through — each on the path core serves it at.
func TestRetentionClient(t *testing.T) {
	type call struct {
		method, path, query string
		body                map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &c.body)
		}
		calls = append(calls, c)
		if strings.HasSuffix(r.URL.Path, "/accounts/acc1/retention") {
			w.Write([]byte(`{"accountId":"acc1","retentionDays":90,"minDays":30,"mailboxes":[{"id":"m1","primaryAddress":"a@x.test","retentionDays":null,"effectiveDays":90,"source":"account","preview":{"days":90,"messages":3,"bytes":300}}],"unreadable":[]}`))
			return
		}
		w.Write([]byte(`{"retentionDays":60,"source":"own","ownRetentionDays":60,"accountRetentionDays":null,"minDays":30,"oldestReceivedAt":1700000000,"nextRunAt":1705000000,"previews":[{"days":60,"messages":12,"bytes":4096}]}`))
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ctx := context.Background()

	got, err := c.GetMailboxRetention(ctx, "m1", []int{30, 90, 365})
	if err != nil || got.RetentionDays == nil || *got.RetentionDays != 60 || got.Source != "own" || len(got.Previews) != 1 {
		t.Fatalf("get: %v %+v", err, got)
	}
	if _, err := c.SetMailboxRetention(ctx, "m1", 60); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := c.ClearMailboxRetention(ctx, "m1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	days := 90
	acc, err := c.GetAccountRetention(ctx, "acc1", &days, 10, "cur")
	if err != nil || acc.RetentionDays == nil || *acc.RetentionDays != 90 || len(acc.Mailboxes) != 1 || acc.Mailboxes[0].Preview == nil {
		t.Fatalf("account get: %v %+v", err, acc)
	}
	if _, err := c.SetAccountRetention(ctx, "acc1", 90); err != nil {
		t.Fatalf("account set: %v", err)
	}
	if _, err := c.ClearAccountRetention(ctx, "acc1"); err != nil {
		t.Fatalf("account clear: %v", err)
	}

	want := []call{
		{method: "GET", path: "/mailboxes/m1/retention", query: "days=30%2C90%2C365"},
		{method: "PUT", path: "/mailboxes/m1/retention", body: map[string]any{"retentionDays": float64(60)}},
		{method: "DELETE", path: "/mailboxes/m1/retention"},
		{method: "GET", path: "/accounts/acc1/retention", query: "cursor=cur&days=90&limit=10"},
		{method: "PUT", path: "/accounts/acc1/retention", body: map[string]any{"retentionDays": float64(90)}},
		{method: "DELETE", path: "/accounts/acc1/retention"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i, w := range want {
		g := calls[i]
		// The client prefixes the API root itself; the suffix is what this test owns.
		if g.method != w.method || !strings.HasSuffix(g.path, w.path) || g.query != w.query {
			t.Errorf("call %d = %s %s?%s; want %s %s?%s", i, g.method, g.path, g.query, w.method, w.path, w.query)
		}
		if w.body != nil && (g.body == nil || g.body["retentionDays"] != w.body["retentionDays"]) {
			t.Errorf("call %d body = %v; want %v", i, g.body, w.body)
		}
	}
}
