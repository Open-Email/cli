package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// --file input is people-made: comments, blank lines, and stray whitespace must
// not become member addresses.
func TestParseMemberList(t *testing.T) {
	got := parseMemberList("# team\n\n  a@x.test  \nb@x.test\n\t# trailing comment\nc@x.test\n")
	want := []string{"a@x.test", "b@x.test", "c@x.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseMemberList = %v, want %v", got, want)
	}
	if out := parseMemberList(""); out != nil {
		t.Errorf("empty input must yield nil, got %v", out)
	}
}

// Dedupe is case-insensitive (core lowercases addresses, so A@x and a@x would
// land as one member) and keeps first-seen order so the chunk boundaries are
// deterministic across re-runs.
func TestDedupeMembers(t *testing.T) {
	got := dedupeMembers([]string{"a@x.test", "B@x.test", "A@x.test", " b@x.test ", "", "c@x.test"})
	want := []string{"a@x.test", "B@x.test", "c@x.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeMembers = %v, want %v", got, want)
	}
}

func TestValidPosting(t *testing.T) {
	for _, ok := range []string{"", "open", "members"} {
		if err := validPosting(ok); err != nil {
			t.Errorf("validPosting(%q) = %v, want nil", ok, err)
		}
	}
	if err := validPosting("closed"); err == nil {
		t.Error("validPosting must refuse values outside open|members")
	}
}

func TestValidRouteType(t *testing.T) {
	for _, ok := range []string{"", "mailbox", "webhook", "remote", "group"} {
		if err := validRouteType(ok); err != nil {
			t.Errorf("validRouteType(%q) = %v, want nil", ok, err)
		}
	}
	if err := validRouteType("alias"); err == nil {
		t.Error("validRouteType must refuse unknown kinds")
	}
}

// A type=group listing is enriched by core, so the group view carries POSTING
// and MEMBERS columns; the bare listing must NOT grow headers it has no data
// for (posting/memberCount are absent outside type=group).
func TestPrintRouteListGroupView(t *testing.T) {
	n := int64(41)
	items := []coreapi.Route{{
		Address: "team@x.test", Domain: "x.test", DestinationType: "group",
		CreatedAt: 1, Posting: "members", MemberCount: &n,
	}}

	p, buf := testPrinter()
	printRouteList(p.out, p, items, true)
	out := buf.String()
	for _, want := range []string{"POSTING", "MEMBERS", "members", "41"} {
		if !strings.Contains(out, want) {
			t.Errorf("group view should contain %q:\n%s", want, out)
		}
	}

	buf.Reset()
	printRouteList(p.out, p, items, false)
	out = buf.String()
	if strings.Contains(out, "POSTING") || strings.Contains(out, "MEMBERS") {
		t.Errorf("bare view must not carry the group columns:\n%s", out)
	}
}
