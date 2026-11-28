package coreapi

// Self-check for the drift contract harness: proves compare() actually flags
// each drift class, so a passing TestWireStructsMatchOpenAPISnapshot can never
// be a silent no-op.

import (
	"reflect"
	"strings"
	"testing"
)

func selfCheckSchemas() map[string]any {
	// Component "Foo": a required string `a`, a required nullable int64 `b`.
	return map[string]any{
		"Foo": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "string"},
				"b": map[string]any{"type": []any{"integer", "null"}, "format": "int64"},
			},
			"required": []any{"a", "b"},
		},
	}
}

func issuesFor(v any) []string {
	schemas := selfCheckSchemas()
	comp := schemas["Foo"].(map[string]any)
	props := comp["properties"].(map[string]any)
	return compare(pairing{comp: "Foo"}, schemas, props, flattenGoFields(reflect.TypeOf(v)))
}

func TestContractHarnessFlagsDrift(t *testing.T) {
	// A struct that matches exactly → no issues.
	type good struct {
		A string `json:"a"`
		B *int64 `json:"b"`
	}
	if got := issuesFor(good{}); len(got) != 0 {
		t.Fatalf("matching struct should have no issues, got: %v", got)
	}

	// (a) missing a spec field.
	type missing struct {
		A string `json:"a"`
	}
	if !hasIssue(issuesFor(missing{}), "no Go field") {
		t.Error("expected a 'no Go field' issue for a struct missing property b")
	}

	// (b) a phantom Go field absent from the spec.
	type phantom struct {
		A     string `json:"a"`
		B     *int64 `json:"b"`
		Extra string `json:"extra"`
	}
	if !hasIssue(issuesFor(phantom{}), "absent from spec") {
		t.Error("expected an 'absent from spec' issue for a phantom field")
	}

	// (c) a kind mismatch (spec b is integer; Go decodes it as a string).
	type wrongKind struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	if !hasIssue(issuesFor(wrongKind{}), "spec kind") {
		t.Error("expected a kind-mismatch issue")
	}

	// (d) a nullable spec field mapped to a non-nilable Go type.
	type notNilable struct {
		A string `json:"a"`
		B int64  `json:"b"`
	}
	if !hasIssue(issuesFor(notNilable{}), "nullable") {
		t.Error("expected a nullability issue for int64 vs a nullable spec field")
	}
}

// A nested INLINE object is descended into; a $ref is not. The first half is
// the gap that let `forwarded` into both traffic totals unnoticed — `totals`
// matched as object-vs-struct and its contents were never read. The second
// half is deliberate: a $ref names a component pinned on its own row, and
// following it would report one drift once per referrer.
func TestContractHarnessDescendsInlineObjects(t *testing.T) {
	schemas := map[string]any{
		"Leaf": map[string]any{
			"type":       "object",
			"properties": map[string]any{"deep": map[string]any{"type": "string"}},
		},
		"Bar": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"totals": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"events":    map[string]any{"type": "integer", "format": "int64"},
						"forwarded": map[string]any{"type": "integer", "format": "int64"},
					},
				},
				"leaf": map[string]any{"$ref": "#/components/schemas/Leaf"},
			},
		},
	}
	props := schemas["Bar"].(map[string]any)["properties"].(map[string]any)
	issuesFor := func(v any) []string {
		return compare(pairing{comp: "Bar"}, schemas, props, flattenGoFields(reflect.TypeOf(v)))
	}

	type leaf struct {
		Deep string `json:"deep"`
	}
	type whole struct {
		Totals struct {
			Events    int64 `json:"events"`
			Forwarded int64 `json:"forwarded"`
		} `json:"totals"`
		Leaf leaf `json:"leaf"`
	}
	if got := issuesFor(whole{}); len(got) != 0 {
		t.Fatalf("matching nested struct should have no issues, got: %v", got)
	}

	type shallow struct {
		Totals struct {
			Events int64 `json:"events"`
		} `json:"totals"`
		Leaf leaf `json:"leaf"`
	}
	got := issuesFor(shallow{})
	if !hasIssue(got, "no Go field") {
		t.Error("expected the missing nested property to be flagged")
	}
	if !hasIssue(got, "Bar.totals:") {
		t.Errorf("expected the issue to name the nested path Bar.totals, got: %v", got)
	}

	// The $ref side: an EMPTY struct behind `leaf` must stay unreported here,
	// or every referrer would repeat the component's own drift.
	type refDrift struct {
		Totals struct {
			Events    int64 `json:"events"`
			Forwarded int64 `json:"forwarded"`
		} `json:"totals"`
		Leaf struct{} `json:"leaf"`
	}
	if got := issuesFor(refDrift{}); len(got) != 0 {
		t.Errorf("a $ref property must not be descended into, got: %v", got)
	}
}

// Core's nullable enums are a union with a null LITERAL arm since core
// 2f97d38a, `z.union([z.enum([...]), z.literal(null)])`, which its generator
// writes as the three arms below. The middle arm admits null alone, whatever
// its `type` says, so the property is a string or null: a *string matches, a
// plain string cannot hold the null, and a number is the wrong kind. Read by
// its `type`, the arm made every such property an "object" and failed eleven
// structs whose Go types were right.
func TestContractHarnessReadsNullLiteralArms(t *testing.T) {
	schemas := map[string]any{
		"Held": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hold": map[string]any{
					"anyOf": []any{
						map[string]any{"enum": []any{"disabled", "paused"}, "type": "string"},
						map[string]any{"enum": []any{nil}, "type": []any{"object", "null"}},
						map[string]any{"type": "null"},
					},
				},
			},
		},
	}
	props := schemas["Held"].(map[string]any)["properties"].(map[string]any)
	issuesFor := func(v any) []string {
		return compare(pairing{comp: "Held"}, schemas, props, flattenGoFields(reflect.TypeOf(v)))
	}

	type pointer struct {
		Hold *string `json:"hold"`
	}
	if got := issuesFor(pointer{}); len(got) != 0 {
		t.Fatalf("a *string should match a string-or-null union, got: %v", got)
	}
	type plain struct {
		Hold string `json:"hold"`
	}
	if !hasIssue(issuesFor(plain{}), "nullable") {
		t.Error("expected a nullability issue for a plain string under a union with a null arm")
	}
	type number struct {
		Hold *int64 `json:"hold"`
	}
	if !hasIssue(issuesFor(number{}), "spec kind") {
		t.Error("expected a kind mismatch for a number under a string-or-null union")
	}
}

// isNullSchema says "null only" for exactly the shapes that admit nothing but
// null. An enum or a type list that also admits a value is no null arm: taking
// it for one would hide that value's kind from the comparison.
func TestIsNullSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   bool
	}{
		{"type null", map[string]any{"type": "null"}, true},
		{"type list of null", map[string]any{"type": []any{"null"}}, true},
		{"enum of null, typed object (core's z.literal(null))", map[string]any{"enum": []any{nil}, "type": []any{"object", "null"}}, true},
		{"const null", map[string]any{"const": nil}, true},
		{"enum with a value beside null", map[string]any{"enum": []any{"a", nil}}, false},
		{"nullable string", map[string]any{"type": []any{"string", "null"}}, false},
		{"const value", map[string]any{"const": "x"}, false},
		{"string", map[string]any{"type": "string"}, false},
		{"no type", map[string]any{}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isNullSchema(c.schema); got != c.want {
			t.Errorf("%s: isNullSchema = %v, want %v", c.name, got, c.want)
		}
	}
}

func hasIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}
