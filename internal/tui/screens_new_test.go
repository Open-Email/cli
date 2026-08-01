package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

func newTestClient(t *testing.T, baseURL string) *coreapi.Client {
	t.Helper()
	c, err := coreapi.New(coreapi.Config{
		BaseURL: baseURL, Token: "oek_test",
		RetryBackoffMin: time.Millisecond, RetryBackoffMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("coreapi.New: %v", err)
	}
	return c
}

// Core allows at most ONE full-text condition and never under OR/NOT, so the
// form treats text and subject as alternatives rather than sending both.
func TestSearchSpecFilterOneFullText(t *testing.T) {
	f := searchSpec{text: "invoice", subject: "ignored"}.filter()
	if f["text"] != "invoice" {
		t.Fatalf("a lone text term must be sent bare: %v", f)
	}
	if _, present := f["subject"]; present {
		t.Errorf("subject must be dropped while text is set: %v", f)
	}
	// Subject is used only when text is empty.
	f = searchSpec{subject: "invoice"}.filter()
	if f["subject"] != "invoice" {
		t.Errorf("subject fallback: %v", f)
	}
}

// Several conditions become an AND tree; one stays bare, since core reads a
// bare condition and a one-element AND identically but the bare form is what
// the grammar documents.
func TestSearchSpecFilterAnd(t *testing.T) {
	f := searchSpec{text: "q", from: "ann@x.test", unreadOnly: true, hasAttach: true}.filter()
	if f["operator"] != "AND" {
		t.Fatalf("want an AND tree, got %v", f)
	}
	conds, _ := f["conditions"].([]any)
	if len(conds) != 4 {
		t.Fatalf("want 4 conditions, got %d: %v", len(conds), conds)
	}
	// Unread is the ABSENCE of $seen — notKeyword, not a keyword match, or the
	// filter would return exactly the messages the user did not ask for.
	var sawNotSeen, sawAttach bool
	for _, c := range conds {
		m := c.(map[string]any)
		if m["notKeyword"] == "$seen" {
			sawNotSeen = true
		}
		if m["hasAttachment"] == true {
			sawAttach = true
		}
	}
	if !sawNotSeen || !sawAttach {
		t.Errorf("conditions = %v", conds)
	}
}

// isAscending is a *bool with omitempty, and core reads an ABSENT value as
// ascending — so omitting it silently puts the oldest matches on page one while
// every other listing in the console is newest-first. It must be on the wire.
func TestSearchSendsDescendingSort(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"results":[],"position":0}`))
	}))
	defer srv.Close()

	desc := searchDesc(coreapi.Mailbox{ID: "01M"}, searchSpec{text: "invoice"})
	if _, _, err := desc.fetch(context.Background(), newTestClient(t, srv.URL), ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	sort, _ := got["sort"].([]any)
	if len(sort) != 1 {
		t.Fatalf("sort = %v", got["sort"])
	}
	cmp := sort[0].(map[string]any)
	if cmp["property"] != "receivedAt" {
		t.Errorf("property = %v", cmp["property"])
	}
	asc, present := cmp["isAscending"]
	if !present {
		t.Fatal("isAscending was OMITTED — core reads that as ascending, so page 1 is the OLDEST matches")
	}
	if asc != false {
		t.Errorf("isAscending = %v, want false (newest first)", asc)
	}
}

func TestSearchSpecEmpty(t *testing.T) {
	if !(searchSpec{}).empty() {
		t.Error("a blank spec must be refused before any round trip")
	}
	if (searchSpec{unreadOnly: true}).empty() {
		t.Error("a flag-only spec is a real query")
	}
	if got := (searchSpec{text: "q", from: "a@x"}).describe(); !strings.Contains(got, "text") || !strings.Contains(got, "from") {
		t.Errorf("describe = %q", got)
	}
}

// The excerpt shares a table cell with real content, so the <mark> tags are
// stripped rather than styled and the whitespace is collapsed to one line.
func TestSnippetText(t *testing.T) {
	preview := "the <mark>invoice</mark> is\n  attached"
	got := snippetText(coreapi.EmailSearchSnippet{Preview: &preview})
	if got != "the invoice is attached" {
		t.Errorf("preview = %q", got)
	}
	// With no body match, the subject excerpt stands in.
	subj := "Re: <mark>invoice</mark>"
	got = snippetText(coreapi.EmailSearchSnippet{Subject: &subj})
	if got != "Re: invoice" {
		t.Errorf("subject fallback = %q", got)
	}
	if got := snippetText(coreapi.EmailSearchSnippet{}); got != "" {
		t.Errorf("no match = %q", got)
	}
}

// Nothing in the API distinguishes an operator disable from an auto-disable, so
// "(auto)" may only be claimed once the failure streak has actually reached
// core's threshold — otherwise an operator who switched a flapping source off
// themselves is told the platform did it.
func TestPickupStatus(t *testing.T) {
	cases := []struct {
		name string
		src  coreapi.PickupSource
		want string
	}{
		{"never run", coreapi.PickupSource{Enabled: true}, "never run"},
		{"healthy", coreapi.PickupSource{Enabled: true, LastStatus: strPtr("ok")}, "ok"},
		{"failing but on", coreapi.PickupSource{Enabled: true, LastStatus: strPtr("error"), ConsecutiveFailures: 2}, "error ×2"},
		{"auto-disabled at the threshold", coreapi.PickupSource{Enabled: false, LastStatus: strPtr("auth_failed"), ConsecutiveFailures: 5}, "disabled (auto)"},
		// One transient failure then an operator switching it off by hand.
		{"switched off while flapping", coreapi.PickupSource{Enabled: false, LastStatus: strPtr("error"), ConsecutiveFailures: 1}, "disabled"},
		{"switched off, clean history", coreapi.PickupSource{Enabled: false, LastStatus: strPtr("ok")}, "disabled"},
	}
	for _, tc := range cases {
		if got := pickupStatus(tc.src); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// screenPane matches a descriptor's actions BEFORE the table sees the key, so an
// action key that collides with the table keymap silently replaces navigation —
// and on the pickups screen "run now" against a deleteAfterFetch source DELETES
// mail from the remote server.
func TestScreenActionKeysDoNotShadowTableNavigation(t *testing.T) {
	// The keys newTable() leaves bound (styles.go).
	nav := map[string]string{
		"j": "line down", "k": "line up", "g": "goto top", "G": "goto bottom",
		"pgup": "page up", "pgdown": "page down", "home": "goto top", "end": "goto bottom",
	}
	mbx := coreapi.Mailbox{ID: "01M"}
	for _, d := range []resourceDesc{
		pickupsDesc(mbx), prefsDesc(mbx), sieveDesc(mbx),
		suppressionsDesc(), dkimDesc(),
	} {
		for _, a := range d.actions {
			if what, clash := nav[a.key]; clash {
				t.Errorf("%s: action %q (%s) shadows the table's %s", d.key, a.key, a.label, what)
			}
		}
	}
}

// The failing-sources banner is the whole point of the screen: a broken pickup
// is otherwise silent — mail simply stops arriving.
func TestPickupsSummaryWarnsOnFailures(t *testing.T) {
	st := &pickupsScreenState{}
	st.set([]coreapi.PickupSource{{Enabled: true, LastStatus: strPtr("ok")}})
	if lines := st.summaryLines(80); len(lines) != 0 {
		t.Errorf("healthy sources need no banner, got %v", lines)
	}
	// Below core's threshold, a disabled source is an OPERATOR decision — the
	// banner may report it as failing but must not claim the platform did it.
	st.set([]coreapi.PickupSource{
		{Enabled: true, ConsecutiveFailures: 1},
		{Enabled: false, ConsecutiveFailures: 4},
	})
	lines := st.summaryLines(120)
	if len(lines) != 1 || !strings.Contains(lines[0], "2 source(s) failing") {
		t.Errorf("summary = %v", lines)
	}
	if strings.Contains(lines[0], "auto-disabled") {
		t.Errorf("summary claims auto-disable below the threshold: %v", lines)
	}

	// At the threshold it is genuinely core's doing, and no mail is arriving.
	st.set([]coreapi.PickupSource{{Enabled: false, ConsecutiveFailures: pickupAutoDisableThreshold}})
	lines = st.summaryLines(120)
	if len(lines) != 1 || !strings.Contains(lines[0], "1 auto-disabled") {
		t.Errorf("summary = %v", lines)
	}
}

// The Sieve screen exists to answer one question — what is filtering mail —
// and "nothing" must read as a warning, not as an empty list.
func TestSieveSummaryStates(t *testing.T) {
	st := &sieveScreenState{}
	if lines := st.summaryLines(80); lines != nil {
		t.Errorf("before the first fetch there is nothing to claim, got %v", lines)
	}
	st.set(nil)
	if lines := st.summaryLines(80); len(lines) != 1 || !strings.Contains(lines[0], "unfiltered") {
		t.Errorf("no scripts = %v", lines)
	}
	st.set([]coreapi.SieveScript{{Name: "vacation"}})
	if lines := st.summaryLines(120); len(lines) != 1 || !strings.Contains(lines[0], "NO ACTIVE SCRIPT") {
		t.Errorf("scripts but none active = %v", lines)
	}
	st.set([]coreapi.SieveScript{{Name: rulesScriptName, Active: true}})
	lines := st.summaryLines(120)
	// A user staring at two names needs to know the active one is generated by
	// the Filters screen, not something they hand-wrote.
	if len(lines) != 1 || !strings.Contains(lines[0], "Filters screen") {
		t.Errorf("rules script active = %v", lines)
	}
}

// The listing exposes only the NAME, while core's refusal is ownership-aware —
// a pre-v14 mailbox can hold a hand-written script under the reserved name and
// still delete it. The label must therefore hedge, not assert authorship.
func TestSieveOwner(t *testing.T) {
	if got := sieveOwner(rulesScriptName); got != "reserved" {
		t.Errorf("reserved script owner = %q", got)
	}
	if got := sieveOwner("vacation"); got != "hand-written" {
		t.Errorf("ordinary script owner = %q", got)
	}
}

// Preferences are opaque JSON: the viewer must report a value's real type and
// render nested values as JSON rather than as a Go dump.
func TestPrefRendering(t *testing.T) {
	cases := []struct {
		v    any
		kind string
		line string
	}{
		{nil, "null", "null"},
		{true, "bool", "true"},
		{float64(3), "number", "3"},
		{"dark", "string", "dark"},
		{[]any{1.0, 2.0}, "array", "[1,2]"},
		{map[string]any{"left": 220.0}, "object", `{"left":220}`},
	}
	for _, tc := range cases {
		if got := prefKind(tc.v); got != tc.kind {
			t.Errorf("prefKind(%v) = %q, want %q", tc.v, got, tc.kind)
		}
		if got := prefInline(tc.v); got != tc.line {
			t.Errorf("prefInline(%v) = %q, want %q", tc.v, got, tc.line)
		}
	}
}

// A public key is one long line; the detail pane must break it into readable
// rows without losing a character.
func TestChunk(t *testing.T) {
	got := chunk("abcdefghij", 4)
	if strings.Join(got, "") != "abcdefghij" || len(got) != 3 {
		t.Errorf("chunk = %v", got)
	}
	if got := chunk("  ", 4); len(got) != 1 || got[0] != "—" {
		t.Errorf("empty chunk = %v", got)
	}
}

// The DKIM banner must name the two states that break outbound mail: nothing
// signing, and a staged key whose TXT was never confirmed published.
func TestDkimSummaryWarnings(t *testing.T) {
	st := &dkimScreenState{}
	st.set(&coreapi.DkimStatus{Configured: false})
	lines := st.summaryLines(160)
	if len(lines) != 2 || !strings.Contains(lines[0], "NOT CONFIGURED") || !strings.Contains(lines[1], "nothing is signing") {
		t.Fatalf("unconfigured = %v", lines)
	}

	sel := "oe1"
	st.set(&coreapi.DkimStatus{
		Configured: true, ActiveSelector: &sel,
		Keys: []coreapi.DkimKey{
			{Selector: "oe1", State: "active", ActivatedAt: int64Ptr(3)},
			{Selector: "oe2", State: "staged"}, // publishedAt nil: the soak never started
		},
	})
	lines = st.summaryLines(160)
	if len(lines) != 2 {
		t.Fatalf("want a signing line plus the staged warning, got %v", lines)
	}
	if !strings.Contains(lines[0], "signing with oe1") || !strings.Contains(lines[0], "not armed") {
		t.Errorf("signing line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "soak has not started") {
		t.Errorf("staged warning = %q", lines[1])
	}
}

func strPtr(s string) *string { return &s }
func int64Ptr(v int64) *int64 { return &v }
