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

func hasIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}
