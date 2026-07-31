package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestParseConditionForms(t *testing.T) {
	cases := []struct {
		spec  string
		want  coreapi.RuleCondition
		valid bool
	}{
		{spec: "from:contains:@acme.com", want: coreapi.RuleCondition{Field: "from", Op: "contains", Value: "@acme.com"}, valid: true},
		{spec: "subject:is:Invoice", want: coreapi.RuleCondition{Field: "subject", Op: "is", Value: "Invoice"}, valid: true},
		{spec: "listId:exists", want: coreapi.RuleCondition{Field: "listId", Op: "exists"}, valid: true},
		// Inversion prefix.
		{spec: "!toOrCc:contains:me@x.com", want: coreapi.RuleCondition{Field: "toOrCc", Op: "contains", Value: "me@x.com", Not: true}, valid: true},
		// The header form carries its own name segment.
		{spec: "header:X-Spam:contains:yes", want: coreapi.RuleCondition{Field: "header", Header: "X-Spam", Op: "contains", Value: "yes"}, valid: true},
		{spec: "header:List-Id:exists", want: coreapi.RuleCondition{Field: "header", Header: "List-Id", Op: "exists"}, valid: true},
		// A value may contain colons — only the fixed prefix is split.
		{spec: "subject:contains:re: fwd: hi", want: coreapi.RuleCondition{Field: "subject", Op: "contains", Value: "re: fwd: hi"}, valid: true},
		{spec: "body:contains:urgent", want: coreapi.RuleCondition{Field: "body", Op: "contains", Value: "urgent"}, valid: true},
		// Size is numeric, with unit suffixes.
		{spec: "size:over:5000000", want: coreapi.RuleCondition{Field: "size", Op: "over", Value: int64(5000000)}, valid: true},
		{spec: "size:under:2m", want: coreapi.RuleCondition{Field: "size", Op: "under", Value: int64(2 << 20)}, valid: true},

		{spec: "nope:contains:x", valid: false},   // unknown field
		{spec: "from:startswith:x", valid: false}, // unknown op
		{spec: "from:contains", valid: false},     // op needing a value
		{spec: "body:is:x", valid: false},         // body takes contains only
		{spec: "size:over:huge", valid: false},    // unparseable size
		{spec: "size:contains:5", valid: false},   // size takes over/under
		{spec: "header:X-Spam", valid: false},     // header needs an op
		{spec: "from", valid: false},              // no op at all
	}
	for _, tc := range cases {
		got, err := parseCondition(tc.spec)
		if tc.valid != (err == nil) {
			t.Errorf("parseCondition(%q): err=%v, want valid=%v", tc.spec, err, tc.valid)
			continue
		}
		if !tc.valid {
			continue
		}
		if got.Field != tc.want.Field || got.Op != tc.want.Op || got.Header != tc.want.Header || got.Not != tc.want.Not {
			t.Errorf("parseCondition(%q) = %+v, want %+v", tc.spec, *got, tc.want)
		}
		if tc.want.Value == nil {
			if got.Value != nil {
				t.Errorf("parseCondition(%q) value = %v, want nil (exists takes none)", tc.spec, got.Value)
			}
		} else if got.Value != tc.want.Value {
			t.Errorf("parseCondition(%q) value = %#v, want %#v", tc.spec, got.Value, tc.want.Value)
		}
	}
}

func TestParseActionForms(t *testing.T) {
	cases := []struct {
		spec  string
		want  coreapi.RuleAction
		valid bool
	}{
		{spec: "label:Work", want: coreapi.RuleAction{Type: "fileInto", Label: "Work"}, valid: true},
		{spec: "label:Lists/Go Nuts", want: coreapi.RuleAction{Type: "fileInto", Label: "Lists/Go Nuts"}, valid: true},
		{spec: "flag:seen", want: coreapi.RuleAction{Type: "addFlag", Flag: "seen"}, valid: true},
		{spec: "redirect:a@b.com", want: coreapi.RuleAction{Type: "redirect", To: "a@b.com"}, valid: true},
		{spec: "redirect-copy:a@b.com", want: coreapi.RuleAction{Type: "redirect", To: "a@b.com", KeepCopy: true}, valid: true},
		{spec: "discard", want: coreapi.RuleAction{Type: "discard"}, valid: true},

		{spec: "flag:important", valid: false}, // not a core flag
		{spec: "label:", valid: false},
		{spec: "redirect:", valid: false},
		{spec: "delete", valid: false},
	}
	for _, tc := range cases {
		got, err := parseAction(tc.spec)
		if tc.valid != (err == nil) {
			t.Errorf("parseAction(%q): err=%v, want valid=%v", tc.spec, err, tc.valid)
			continue
		}
		if tc.valid && *got != tc.want {
			t.Errorf("parseAction(%q) = %+v, want %+v", tc.spec, *got, tc.want)
		}
	}
}

// A rule needs at least one condition and one action, and --disabled must
// serialize as an explicit false (omitting it would mean "enabled").
func TestBuildRule(t *testing.T) {
	if _, err := buildRule("", "all", nil, []string{"discard"}, false, false); err == nil {
		t.Error("a rule with no conditions should be refused")
	}
	if _, err := buildRule("", "all", []string{"from:exists"}, nil, false, false); err == nil {
		t.Error("a rule with no actions should be refused")
	}
	if _, err := buildRule("", "either", []string{"from:exists"}, []string{"discard"}, false, false); err == nil {
		t.Error("an invalid --match should be refused")
	}
	r, err := buildRule("Acme", "any", []string{"from:contains:@acme.com", "!subject:contains:spam"},
		[]string{"label:Work", "flag:seen"}, true, true)
	if err != nil {
		t.Fatalf("buildRule: %v", err)
	}
	if r.Name != "Acme" || r.Match != "any" || !r.Stop || len(r.Conditions) != 2 || len(r.Actions) != 2 {
		t.Fatalf("built rule wrong: %+v", r)
	}
	if r.IsEnabled() {
		t.Error("--disabled should switch the rule off")
	}
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	_ = json.Unmarshal(blob, &back)
	if v, ok := back["enabled"]; !ok || v != false {
		t.Errorf("disabled rule must serialize enabled:false, got %v (present=%v)", v, ok)
	}
}

// An enabled-by-default rule omits the field entirely (core defaults it true),
// so a round trip never rewrites a user's document with redundant keys.
func TestEnabledOmittedWhenDefault(t *testing.T) {
	r, err := buildRule("x", "all", []string{"from:exists"}, []string{"discard"}, false, false)
	if err != nil {
		t.Fatalf("buildRule: %v", err)
	}
	blob, _ := json.Marshal(r)
	var back map[string]any
	_ = json.Unmarshal(blob, &back)
	if _, present := back["enabled"]; present {
		t.Errorf("an enabled rule should omit the field, got %s", blob)
	}
	if !r.IsEnabled() {
		t.Error("a rule with no explicit enabled must read as enabled")
	}
}

func TestParseRulesDocument(t *testing.T) {
	// The full envelope.
	doc, err := parseRulesDocument([]byte(`{"rules":[{"conditions":[{"field":"from","op":"exists"}],"actions":[{"type":"discard"}]}]}`))
	if err != nil || len(doc.Rules) != 1 {
		t.Fatalf("envelope form: %v %+v", err, doc)
	}
	// A bare array, which is what hand-editing usually produces.
	doc, err = parseRulesDocument([]byte(`[{"conditions":[{"field":"from","op":"exists"}],"actions":[{"type":"discard"}]}]`))
	if err != nil || len(doc.Rules) != 1 {
		t.Fatalf("bare array form: %v %+v", err, doc)
	}
	// An empty list is legal: rules mode, no rules.
	doc, err = parseRulesDocument([]byte(`{"rules":[]}`))
	if err != nil || doc.Rules == nil || len(doc.Rules) != 0 {
		t.Fatalf("empty list should parse to an empty non-nil slice: %v %+v", err, doc)
	}
	for _, bad := range []string{"", "   ", "{}", "not json", `{"rule":[]}`} {
		if _, err := parseRulesDocument([]byte(bad)); err == nil {
			t.Errorf("parseRulesDocument(%q) unexpectedly parsed", bad)
		}
	}
}

func TestRulePosition(t *testing.T) {
	if _, err := rulePosition("1", 0); err == nil {
		t.Error("a position against an empty list should be refused")
	}
	if idx, err := rulePosition("2", 3); err != nil || idx != 1 {
		t.Errorf("rulePosition(2,3) = %d, %v; want 1", idx, err)
	}
	for _, bad := range []string{"0", "4", "x", "-1"} {
		if _, err := rulePosition(bad, 3); err == nil {
			t.Errorf("rulePosition(%q,3) unexpectedly ok", bad)
		}
	}
}

func TestMoveRule(t *testing.T) {
	mk := func(names ...string) []coreapi.FilterRule {
		out := make([]coreapi.FilterRule, len(names))
		for i, n := range names {
			out[i] = coreapi.FilterRule{Name: n}
		}
		return out
	}
	names := func(rs []coreapi.FilterRule) string {
		s := ""
		for _, r := range rs {
			s += r.Name
		}
		return s
	}
	in := mk("A", "B", "C", "D")
	if got := names(moveRule(in, 0, 2)); got != "BCAD" {
		t.Errorf("move first to third = %s, want BCAD", got)
	}
	if got := names(moveRule(in, 3, 0)); got != "DABC" {
		t.Errorf("move last to first = %s, want DABC", got)
	}
	if got := names(moveRule(in, 1, 1)); got != "ABCD" {
		t.Errorf("move to itself = %s, want ABCD", got)
	}
	// The source list must not be mutated (the caller still holds it).
	if names(in) != "ABCD" {
		t.Errorf("moveRule mutated its input: %s", names(in))
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{"0": 0, "1024": 1024, "5k": 5 << 10, "2M": 2 << 20, "1g": 1 << 30}
	for in, want := range cases {
		got, err := parseByteSize(in)
		if err != nil || got != want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "-5", "5tb", "1.5m"} {
		if _, err := parseByteSize(bad); err == nil {
			t.Errorf("parseByteSize(%q) unexpectedly parsed", bad)
		}
	}
}

func TestSummarizeConditionsAndActions(t *testing.T) {
	conds := []coreapi.RuleCondition{
		{Field: "from", Op: "contains", Value: "@acme.com"},
		{Field: "header", Header: "X-Spam", Op: "is", Value: "yes", Not: true},
		{Field: "listId", Op: "exists"},
		{Field: "size", Op: "over", Value: float64(5 << 20)}, // as decoded from JSON
	}
	got := summarizeConditions(conds)
	for _, want := range []string{"from contains @acme.com", "NOT X-Spam is yes", "listId exists", "size over 5.0 MiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("summarizeConditions missing %q in %q", want, got)
		}
	}
	acts := []coreapi.RuleAction{
		{Type: "fileInto", Label: "Work"},
		{Type: "addFlag", Flag: "seen"},
		{Type: "redirect", To: "a@b.com", KeepCopy: true},
		{Type: "discard"},
	}
	got = summarizeActions(acts)
	for _, want := range []string{"file into Work", "flag seen", "redirect to a@b.com (keep copy)", "discard"} {
		if !strings.Contains(got, want) {
			t.Errorf("summarizeActions missing %q in %q", want, got)
		}
	}
}
