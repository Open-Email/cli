package cli

import (
	"reflect"
	"testing"
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
