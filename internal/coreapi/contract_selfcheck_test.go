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

func hasIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}
