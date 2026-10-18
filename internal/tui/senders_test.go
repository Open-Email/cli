package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

const sendersAcc = "01KXRS3SHN1N35G4YETVADSN0R"

// fakeListsCore is just enough of core's account address-list surface to drive
// the senders screen: list listing with its filters, list create, and entry
// list/add/delete with pattern-ordered cursor pages.
type fakeListsCore struct {
	t            *testing.T
	mu           sync.Mutex
	lists        []coreapi.AddressList
	entries      map[string][]coreapi.AddressListEntry
	pageSize     int // 0 = every entry on one page
	createStatus int // non-zero: POST /lists answers this status
	addStatus    int // non-zero: POST /entries answers this status
	log          []string
	listQueries  []string
	bodies       []map[string]any
	nextID       int
}

func (f *fakeListsCore) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	base := "/api/v1/accounts/" + sendersAcc + "/lists"
	rest, ok := strings.CutPrefix(r.URL.Path, base)
	if !ok {
		f.t.Errorf("unexpected path %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
			f.bodies = append(f.bodies, body)
		}
	}
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not_found"}`)
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		q := r.URL.Query()
		f.listQueries = append(f.listQueries, q.Encode())
		out := []coreapi.AddressList{}
		for _, l := range f.lists {
			if v := q.Get("scopeKind"); v != "" && v != l.ScopeKind {
				continue
			}
			if v := q.Get("scopeId"); v != "" && v != l.ScopeID {
				continue
			}
			if v := q.Get("direction"); v != "" && v != l.Direction {
				continue
			}
			if v := q.Get("verdict"); v != "" && v != l.Verdict {
				continue
			}
			out = append(out, l)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"lists": out, "nextCursor": ""})
	case rest == "" && r.Method == http.MethodPost:
		if f.createStatus != 0 {
			w.WriteHeader(f.createStatus)
			_, _ = io.WriteString(w, `{"error":"list_name_taken"}`)
			return
		}
		f.nextID++
		l := coreapi.AddressList{
			ID: "created" + string(rune('0'+f.nextID)), Name: body["name"].(string),
			ScopeKind: body["scopeKind"].(string), ScopeID: body["scopeId"].(string),
			Direction: body["direction"].(string), Verdict: body["verdict"].(string),
		}
		f.lists = append(f.lists, l)
		f.entries[l.ID] = []coreapi.AddressListEntry{}
		_ = json.NewEncoder(w).Encode(l)
	default:
		parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
		if len(parts) < 2 || parts[1] != "entries" {
			notFound()
			return
		}
		id := parts[0]
		entries, exists := f.entries[id]
		if !exists {
			notFound()
			return
		}
		switch {
		case len(parts) == 2 && r.Method == http.MethodGet:
			sort.Slice(entries, func(i, j int) bool { return entries[i].Pattern < entries[j].Pattern })
			cursor := r.URL.Query().Get("cursor")
			page := []coreapi.AddressListEntry{}
			for _, e := range entries {
				if cursor != "" && e.Pattern <= cursor {
					continue
				}
				page = append(page, e)
			}
			next := ""
			if f.pageSize > 0 && len(page) > f.pageSize {
				page = page[:f.pageSize]
				next = page[len(page)-1].Pattern
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": page, "nextCursor": next})
		case len(parts) == 2 && r.Method == http.MethodPost:
			if f.addStatus != 0 {
				w.WriteHeader(f.addStatus)
				_, _ = io.WriteString(w, `{"error":"invalid_pattern"}`)
				return
			}
			p := strings.ToLower(strings.TrimSpace(body["pattern"].(string)))
			if strings.HasPrefix(p, "@") {
				p = "*" + p
			}
			e := coreapi.AddressListEntry{Pattern: p, CreatedAt: 1700000000}
			if n, ok := body["note"].(string); ok {
				e.Note = &n
			}
			f.entries[id] = append(entries, e)
			_ = json.NewEncoder(w).Encode(e)
		case len(parts) == 3 && r.Method == http.MethodDelete:
			for i, e := range entries {
				if e.Pattern == parts[2] {
					f.entries[id] = append(entries[:i:i], entries[i+1:]...)
					_, _ = io.WriteString(w, `{"deleted":true}`)
					return
				}
			}
			notFound()
		default:
			notFound()
		}
	}
}

func (f *fakeListsCore) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func inboundList(id, name, verdict string) coreapi.AddressList {
	return coreapi.AddressList{ID: id, ScopeKind: "account", ScopeID: sendersAcc, Name: name, Direction: "inbound", Verdict: verdict}
}

func entry(pattern string) coreapi.AddressListEntry {
	return coreapi.AddressListEntry{Pattern: pattern, CreatedAt: 1700000000}
}

func newFakeListsCore(t *testing.T) (*fakeListsCore, *coreapi.Client) {
	t.Helper()
	f := &fakeListsCore{t: t, entries: map[string][]coreapi.AddressListEntry{}}
	client, done := credClient(t, f.handler)
	t.Cleanup(done)
	return f, client
}

func ruleOf(t *testing.T, row rowData) senderRule {
	t.Helper()
	r, ok := row.item.(senderRule)
	if !ok {
		t.Fatalf("row item is %T, want senderRule", row.item)
	}
	return r
}

// The screen is an account-tier screen like Do-Not-Send: an account key with
// an account sees it, a system key (which has no account to scope it to) does
// not.
func TestSendersWiredIntoSidebarForAccountKeysOnly(t *testing.T) {
	if _, ok := allDescriptors(sendersAcc)["senders"]; !ok {
		t.Fatal("allDescriptors is missing the senders screen")
	}
	has := func(opts Options) (string, bool) {
		for _, it := range newRoot(t.Context(), opts).side.items {
			if it.key == "senders" {
				return it.label, true
			}
		}
		return "", false
	}
	label, ok := has(Options{Role: coreapi.PrincipalAccount, AccountID: sendersAcc})
	if !ok {
		t.Fatal("an account key's sidebar does not offer the senders screen")
	}
	if label != "Senders" {
		t.Fatalf("sidebar label = %q, want Senders", label)
	}
	if _, ok := has(Options{Role: coreapi.PrincipalSystem}); ok {
		t.Fatal("a system key's sidebar offers an account-scoped screen")
	}
}

// One table over the two lists the console owns, the verdict riding each row.
// A list the tenant made through `openemail lists` is not one of them and stays
// off this screen even though it is also inbound at account scope.
func TestSendersFetchShowsBothConsoleListsWithTheirVerdict(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{
		inboundList("lb", "Blocked senders", "block"),
		inboundList("la", "Allowed senders", "allow"),
		inboundList("lx", "Spam domains", "block"),
	}
	f.entries["lb"] = []coreapi.AddressListEntry{entry("a@x.example")}
	f.entries["la"] = []coreapi.AddressListEntry{entry("*@.lists.example"), entry("a@x.example")}
	f.entries["lx"] = []coreapi.AddressListEntry{entry("*@spam.example")}

	rows, next, err := sendersDesc(sendersAcc).fetch(t.Context(), client, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" {
		t.Fatalf("next = %q on a listing that fits one page", next)
	}
	var got []string
	ids := map[string]bool{}
	for _, row := range rows {
		r := ruleOf(t, row)
		got = append(got, r.Verdict+" "+r.Entry.Pattern+" "+r.ListID)
		if ids[row.id] {
			t.Fatalf("duplicate row id %q: one address on both lists must stay two rows", row.id)
		}
		ids[row.id] = true
		// The first cell titles the detail pane, so it is the sender.
		if row.cells[0] != r.Entry.Pattern || row.cells[1] != r.Verdict {
			t.Fatalf("cells = %q, want the sender then its verdict", row.cells)
		}
	}
	want := []string{"block a@x.example lb", "allow *@.lists.example la", "allow a@x.example la"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for _, q := range f.listQueries {
		if !strings.Contains(q, "direction=inbound") || !strings.Contains(q, "scopeKind=account") || !strings.Contains(q, "scopeId="+sendersAcc) {
			t.Fatalf("list lookup not narrowed to the account's inbound lists: %q", q)
		}
	}
}

// Opening the screen must never mint directory state: no lists is an empty
// table, not a POST.
func TestSendersFetchWithNoListsIsEmptyAndCreatesNothing(t *testing.T) {
	f, client := newFakeListsCore(t)
	rows, next, err := sendersDesc(sendersAcc).fetch(t.Context(), client, "")
	if err != nil || len(rows) != 0 || next != "" {
		t.Fatalf("fetch = %d rows, next %q, err %v; want an empty table", len(rows), next, err)
	}
	for _, req := range f.requests() {
		if strings.HasPrefix(req, http.MethodPost) {
			t.Fatalf("reading the screen wrote to core: %s", req)
		}
	}
}

// Pages walk the block list to its end and then carry on into the allow list,
// and a page that exhausts the block list fills itself from the allow list
// rather than ending short and hiding the allow rules behind a load-more.
func TestSendersPaginatesBlockListThenAllowList(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.pageSize = 2
	f.lists = []coreapi.AddressList{
		inboundList("lb", "Blocked senders", "block"),
		inboundList("la", "Allowed senders", "allow"),
	}
	f.entries["lb"] = []coreapi.AddressListEntry{entry("a@b.example"), entry("b@b.example"), entry("c@b.example")}
	f.entries["la"] = []coreapi.AddressListEntry{entry("z@a.example")}
	desc := sendersDesc(sendersAcc)

	rows, next, err := desc.fetch(t.Context(), client, "")
	if err != nil || len(rows) != 2 || next == "" {
		t.Fatalf("first page: %d rows, next %q, err %v", len(rows), next, err)
	}
	rows, next, err = desc.fetch(t.Context(), client, next)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	var got []string
	for _, row := range rows {
		r := ruleOf(t, row)
		got = append(got, r.Verdict+" "+r.Entry.Pattern)
	}
	if strings.Join(got, "|") != "block c@b.example|allow z@a.example" || next != "" {
		t.Fatalf("second page = %v, next %q; want the block list's tail then the allow list", got, next)
	}
}

// A load-more page appends to the rows already on screen with the old list's
// cursor; if that list was replaced meanwhile, its replacement's first page
// would mix with them. Same contract as Do-Not-Send: refuse, ask for a reload.
func TestSendersLoadMoreOnReplacedListAsksForReload(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.pageSize = 1
	f.lists = []coreapi.AddressList{inboundList("lb", "Blocked senders", "block")}
	f.entries["lb"] = []coreapi.AddressListEntry{entry("a@b.example"), entry("b@b.example")}
	desc := sendersDesc(sendersAcc)

	_, next, err := desc.fetch(t.Context(), client, "")
	if err != nil || next == "" {
		t.Fatalf("first page: next %q, err %v", next, err)
	}
	f.mu.Lock()
	delete(f.entries, "lb")
	f.lists = []coreapi.AddressList{inboundList("lb2", "Blocked senders", "block")}
	f.entries["lb2"] = []coreapi.AddressListEntry{entry("a@b.example")}
	f.mu.Unlock()

	rows, _, err := desc.fetch(t.Context(), client, next)
	if err == nil || !strings.Contains(err.Error(), "r to reload") || len(rows) != 0 {
		t.Fatalf("load-more on a replaced list: %d rows, err %v; want no rows and a reload hint", len(rows), err)
	}
	if rows, _, err := desc.fetch(t.Context(), client, ""); err != nil || len(rows) != 1 {
		t.Fatalf("reload: %d rows, err %v; want the replacement list's entry", len(rows), err)
	}
}

// The first write creates the list under the console's name, so a rule added
// here is the same rule the console shows.
func TestSendersAddCreatesTheConsoleListOnFirstWrite(t *testing.T) {
	f, client := newFakeListsCore(t)
	e, err := addSenderRule(t.Context(), client, sendersAcc, "allow", " @.Lists.Example ", " newsletter ")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if e.Pattern != "*@.lists.example" {
		t.Fatalf("returned pattern %q, want core's stored spelling", e.Pattern)
	}
	if len(f.lists) != 1 {
		t.Fatalf("lists after add = %v", f.lists)
	}
	l := f.lists[0]
	if l.Name != "Allowed senders" || l.ScopeKind != "account" || l.ScopeID != sendersAcc || l.Direction != "inbound" || l.Verdict != "allow" {
		t.Fatalf("created list = %+v, want the account's inbound allow list named Allowed senders", l)
	}
	got := f.entries[l.ID]
	if len(got) != 1 || got[0].Note == nil || *got[0].Note != "newsletter" {
		t.Fatalf("entries = %+v, want one entry carrying the trimmed note", got)
	}
}

func TestSendersAddWritesToTheExistingList(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{
		inboundList("lb", "Blocked senders", "block"),
		inboundList("la", "Allowed senders", "allow"),
	}
	f.entries["lb"] = []coreapi.AddressListEntry{}
	f.entries["la"] = []coreapi.AddressListEntry{}
	if _, err := addSenderRule(t.Context(), client, sendersAcc, "block", "spammer@x.example", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(f.lists) != 2 || len(f.entries["lb"]) != 1 || len(f.entries["la"]) != 0 {
		t.Fatalf("lists %v, block entries %v, allow entries %v", f.lists, f.entries["lb"], f.entries["la"])
	}
}

// A same-named list built for another purpose (here an OUTBOUND allow list a
// tenant named "Allowed senders" through the CLI) must be refused by name, not
// silently written into.
func TestSendersAddRefusesASameNamedListOfAnotherKind(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{{ID: "lo", ScopeKind: "account", ScopeID: sendersAcc, Name: "Allowed senders", Direction: "outbound", Verdict: "allow"}}
	f.entries["lo"] = []coreapi.AddressListEntry{}
	f.createStatus = http.StatusConflict
	_, err := addSenderRule(t.Context(), client, sendersAcc, "allow", "a@x.example", "")
	if err == nil || !strings.Contains(err.Error(), "different purpose") {
		t.Fatalf("add = %v, want a refusal naming the clash", err)
	}
	if len(f.entries["lo"]) != 0 {
		t.Fatal("wrote an inbound rule into an outbound list")
	}
}

// One address can sit on both lists; removing one rule must delete from the
// list that row came from, never the other.
func TestSendersRemoveDeletesFromTheRowsOwnList(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{
		inboundList("lb", "Blocked senders", "block"),
		inboundList("la", "Allowed senders", "allow"),
	}
	f.entries["lb"] = []coreapi.AddressListEntry{entry("a@x.example")}
	f.entries["la"] = []coreapi.AddressListEntry{entry("a@x.example")}
	if err := removeSenderRule(t.Context(), client, sendersAcc, senderRule{Verdict: "allow", ListID: "la", Entry: entry("a@x.example")}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(f.entries["la"]) != 0 || len(f.entries["lb"]) != 1 {
		t.Fatalf("after removing the allow rule: allow %v, block %v", f.entries["la"], f.entries["lb"])
	}
}

// A row read before its list was replaced still removes the rule from the
// list that holds it now.
func TestSendersRemoveFollowsAReplacedList(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{inboundList("lb2", "Blocked senders", "block")}
	f.entries["lb2"] = []coreapi.AddressListEntry{entry("a@x.example")}
	if err := removeSenderRule(t.Context(), client, sendersAcc, senderRule{Verdict: "block", ListID: "lb", Entry: entry("a@x.example")}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(f.entries["lb2"]) != 0 {
		t.Fatalf("the rule survived on the replacement list: %v", f.entries["lb2"])
	}
}

// A flip ADDS the new verdict before it REMOVES the old one, as the console
// does: the moment between holds both rules, which core resolves to allow, and
// a failed add leaves the old rule rather than no rule at all. The note rides
// across.
func TestSendersFlipAddsTheNewVerdictBeforeRemovingTheOld(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{inboundList("lb", "Blocked senders", "block")}
	note := "was spam, is a customer now"
	e := entry("a@x.example")
	e.Note = &note
	f.entries["lb"] = []coreapi.AddressListEntry{e}

	if err := flipSenderRule(t.Context(), client, sendersAcc, senderRule{Verdict: "block", ListID: "lb", Entry: e}); err != nil {
		t.Fatalf("flip: %v", err)
	}
	var addAt, delAt = -1, -1
	for i, req := range f.requests() {
		switch {
		case strings.HasPrefix(req, "POST ") && strings.HasSuffix(req, "/entries"):
			addAt = i
		case strings.HasPrefix(req, "DELETE "):
			delAt = i
		}
	}
	if addAt < 0 || delAt < 0 || addAt > delAt {
		t.Fatalf("requests %v: want the add before the delete", f.requests())
	}
	if len(f.entries["lb"]) != 0 {
		t.Fatalf("the block rule survived the flip: %v", f.entries["lb"])
	}
	var allowID string
	for _, l := range f.lists {
		if l.Name == "Allowed senders" && l.Verdict == "allow" {
			allowID = l.ID
		}
	}
	got := f.entries[allowID]
	if len(got) != 1 || got[0].Pattern != "a@x.example" || got[0].Note == nil || *got[0].Note != note {
		t.Fatalf("allow entries after the flip = %+v, want the pattern with its note", got)
	}
}

func TestSendersFlipKeepsTheOldRuleWhenTheAddFails(t *testing.T) {
	f, client := newFakeListsCore(t)
	f.lists = []coreapi.AddressList{
		inboundList("lb", "Blocked senders", "block"),
		inboundList("la", "Allowed senders", "allow"),
	}
	f.entries["lb"] = []coreapi.AddressListEntry{entry("*@*")}
	f.entries["la"] = []coreapi.AddressListEntry{}
	f.addStatus = http.StatusBadRequest // core refuses a catch-all on an allow list

	if err := flipSenderRule(t.Context(), client, sendersAcc, senderRule{Verdict: "block", ListID: "lb", Entry: entry("*@*")}); err == nil {
		t.Fatal("flip reported success although the add was refused")
	}
	for _, req := range f.requests() {
		if strings.HasPrefix(req, "DELETE ") {
			t.Fatalf("removed the old rule after a failed add: %v", f.requests())
		}
	}
}
