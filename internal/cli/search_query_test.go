package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

func searchClient(t *testing.T, baseURL string) *coreapi.Client {
	t.Helper()
	c, err := coreapi.New(coreapi.Config{BaseURL: baseURL, Token: "oek_test",
		RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// A bare query becomes the single full-text condition; adding a second one is
// refused locally, naming the flags the user actually typed.
func TestBuildSearchFilterFullTextExclusivity(t *testing.T) {
	f, err := buildSearchFilter(context.Background(), nil, "m", "hello", "", &searchFlags{})
	if err != nil || f["text"] != "hello" {
		t.Fatalf("bare query: %v %v", f, err)
	}
	if _, err := buildSearchFilter(context.Background(), nil, "m", "hello", "", &searchFlags{subject: "x"}); err == nil {
		t.Error("query + --subject should be refused (one full-text condition)")
	}
	if _, err := buildSearchFilter(context.Background(), nil, "m", "", "", &searchFlags{subject: "a", body: "b"}); err == nil {
		t.Error("--subject + --body should be refused")
	}
	// No criteria at all is a usage error, not an unbounded match-everything.
	if _, err := buildSearchFilter(context.Background(), nil, "m", "", "", &searchFlags{}); err == nil {
		t.Error("an empty filter should be refused")
	}
}

func TestBuildSearchFilterConditions(t *testing.T) {
	sf := &searchFlags{
		from: "boss@acme.com", to: "me@x.test", cc: "team@x.test",
		minSize: "1k", maxSize: "2m", hasAttachment: true,
		before: "2026-07-01", after: "1750000000",
	}
	f, err := buildSearchFilter(context.Background(), nil, "m", "", "", sf)
	if err != nil {
		t.Fatalf("buildSearchFilter: %v", err)
	}
	if f["from"] != "boss@acme.com" || f["to"] != "me@x.test" || f["cc"] != "team@x.test" {
		t.Errorf("address conditions wrong: %v", f)
	}
	if f["minSize"] != int64(1024) || f["maxSize"] != int64(2<<20) {
		t.Errorf("size conditions wrong: %v %v", f["minSize"], f["maxSize"])
	}
	if f["hasAttachment"] != true {
		t.Error("hasAttachment missing")
	}
	// Dates must reach the wire as RFC 3339 UTCDate strings.
	before, _ := f["before"].(string)
	if !strings.HasSuffix(before, "Z") || !strings.HasPrefix(before, "2026-07-01") {
		t.Errorf("before = %q, want an RFC3339 UTC instant", before)
	}
	if _, err := time.Parse(time.RFC3339, f["after"].(string)); err != nil {
		t.Errorf("after is not RFC3339: %v", f["after"])
	}
}

// --unread/--flagged are keyword sugar; a second keyword on either side has to
// become a boolean tree, since the wire takes one keyword per condition.
func TestBuildSearchFilterKeywords(t *testing.T) {
	f, err := buildSearchFilter(context.Background(), nil, "m", "", "", &searchFlags{unread: true, flagged: true})
	if err != nil {
		t.Fatalf("buildSearchFilter: %v", err)
	}
	if f["notKeyword"] != "$seen" || f["hasKeyword"] != "$flagged" {
		t.Errorf("keyword sugar wrong: %v", f)
	}
	f, err = buildSearchFilter(context.Background(), nil, "m", "", "",
		&searchFlags{hasKeyword: []string{"flagged", "answered"}})
	if err != nil {
		t.Fatalf("buildSearchFilter: %v", err)
	}
	if f["operator"] != "AND" {
		t.Fatalf("two keywords need a boolean tree, got %v", f)
	}
	conds, _ := f["conditions"].([]map[string]any)
	if len(conds) != 2 || conds[1]["hasKeyword"] != "$answered" {
		t.Errorf("tree conditions wrong: %v", conds)
	}
	// A user keyword keeps its own spelling (no $ prefix invented for it).
	f, _ = buildSearchFilter(context.Background(), nil, "m", "", "", &searchFlags{hasKeyword: []string{"todo"}})
	if f["hasKeyword"] != "todo" {
		t.Errorf("user keyword should pass through, got %v", f["hasKeyword"])
	}
}

// A label NAME is resolved to the L<id> mailbox id the filter wants.
func TestBuildSearchFilterResolvesLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"labels":[{"id":7,"name":"INBOX","role":"inbox","uidValidity":1,"uidNext":2,"messageCount":0,"unseenCount":0,"sortOrder":1}]}`))
	}))
	defer srv.Close()
	c := searchClient(t, srv.URL)

	f, err := buildSearchFilter(context.Background(), c, "m", "", "inbox", &searchFlags{})
	if err != nil {
		t.Fatalf("buildSearchFilter: %v", err)
	}
	if f["inMailbox"] != "L7" {
		t.Errorf("inMailbox = %v, want L7", f["inMailbox"])
	}
	// An id-shaped value is passed through without a lookup.
	f, err = buildSearchFilter(context.Background(), c, "m", "", "L42", &searchFlags{})
	if err != nil || f["inMailbox"] != "L42" {
		t.Errorf("id passthrough: %v %v", f["inMailbox"], err)
	}
	if _, err := buildSearchFilter(context.Background(), c, "m", "", "Nope", &searchFlags{}); err == nil {
		t.Error("an unknown label should be refused")
	}
}

func TestBuildSearchSort(t *testing.T) {
	got, err := buildSearchSort([]string{"receivedAt:desc", "size:asc", "subject"})
	if err != nil {
		t.Fatalf("buildSearchSort: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 comparators, got %d", len(got))
	}
	if got[0].Property != "receivedAt" || got[0].IsAscending == nil || *got[0].IsAscending {
		t.Errorf("desc comparator wrong: %+v", got[0])
	}
	if got[1].IsAscending == nil || !*got[1].IsAscending {
		t.Errorf("asc comparator wrong: %+v", got[1])
	}
	// No direction: leave it unset so the server default applies.
	if got[2].IsAscending != nil {
		t.Errorf("bare property should not force a direction: %+v", got[2])
	}
	// A descending sort must serialize isAscending:false, not omit it.
	blob, _ := json.Marshal(got[0])
	if !strings.Contains(string(blob), `"isAscending":false`) {
		t.Errorf("descending must be explicit on the wire, got %s", blob)
	}
	// hasKeyword needs its keyword.
	kw, err := buildSearchSort([]string{"hasKeyword:flagged:desc"})
	if err != nil || kw[0].Keyword != "$flagged" {
		t.Errorf("hasKeyword comparator: %+v %v", kw, err)
	}
	for _, bad := range []string{"nope:desc", "receivedAt:sideways", "hasKeyword", "hasKeyword:desc"} {
		if _, err := buildSearchSort([]string{bad}); err == nil {
			t.Errorf("buildSearchSort(%q) unexpectedly ok", bad)
		}
	}
	if _, err := buildSearchSort([]string{"size", "from", "subject", "sentAt", "receivedAt"}); err == nil {
		t.Error("more than 4 comparators should be refused")
	}
}

func TestParseSearchDate(t *testing.T) {
	if _, err := parseSearchDate("2026-07-01T10:00:00Z"); err != nil {
		t.Errorf("RFC3339: %v", err)
	}
	if got, _ := parseSearchDate("2026-07-01"); !strings.HasPrefix(got, "2026-07-01T00:00:00") {
		t.Errorf("bare date = %q", got)
	}
	// Relative forms resolve to a past instant.
	got, err := parseSearchDate("7d")
	if err != nil {
		t.Fatalf("relative: %v", err)
	}
	ts, _ := time.Parse(time.RFC3339, got)
	if d := time.Since(ts); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("7d resolved to %v ago", d)
	}
	for _, bad := range []string{"yesterday", "7x", ""} {
		if _, err := parseSearchDate(bad); err == nil {
			t.Errorf("parseSearchDate(%q) unexpectedly parsed", bad)
		}
	}
}

// The structured request reaches the wire with the flags the user set.
func TestSearchQueryRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search/query") || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"results":[],"position":0,"total":3}`))
	}))
	defer srv.Close()

	res, err := searchClient(t, srv.URL).SearchQuery(context.Background(), "m", coreapi.EmailSearchRequest{
		Filter: coreapi.EmailSearchFilter{"text": "hi"},
		Sort:   []coreapi.EmailSearchComparator{{Property: "size"}},
		Limit:  10, Position: 20, CalculateTotal: true, Snippet: true,
	})
	if err != nil {
		t.Fatalf("SearchQuery: %v", err)
	}
	if got["limit"] != float64(10) || got["position"] != float64(20) ||
		got["calculateTotal"] != true || got["snippet"] != true {
		t.Errorf("request shape wrong: %v", got)
	}
	if res.Total == nil || *res.Total != 3 {
		t.Errorf("total not decoded: %+v", res.Total)
	}
}
