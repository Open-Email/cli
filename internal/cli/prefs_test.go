package cli

import (
	"reflect"
	"testing"
)

// The put body may be the bare prefs object or the versioned envelope; the
// envelope's version becomes the CAS guard automatically, which is what makes
// `prefs get | prefs put` a safe round trip.
func TestParsePrefsInput(t *testing.T) {
	prefs, version, err := parsePrefsInput([]byte(`{"prefs":{"theme":"dark"},"version":7}`))
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if prefs["theme"] != "dark" {
		t.Errorf("prefs = %v", prefs)
	}
	if version == nil || *version != 7 {
		t.Errorf("version = %v, want 7 (the CAS guard)", version)
	}

	prefs, version, err = parsePrefsInput([]byte(`{"theme":"light"}`))
	if err != nil {
		t.Fatalf("bare object: %v", err)
	}
	if prefs["theme"] != "light" {
		t.Errorf("prefs = %v", prefs)
	}
	if version != nil {
		t.Errorf("a bare object carries no version, got %v", *version)
	}

	// An empty document is legal — it is how you clear every preference.
	prefs, _, err = parsePrefsInput([]byte(`{"prefs":{},"version":3}`))
	if err != nil || len(prefs) != 0 {
		t.Errorf("empty prefs: %v %v", prefs, err)
	}

	for _, bad := range []string{"", "   ", "not json", "[1,2]"} {
		if _, _, err := parsePrefsInput([]byte(bad)); err == nil {
			t.Errorf("parsePrefsInput(%q) unexpectedly parsed", bad)
		}
	}
}

// A key=value pair keeps its JSON type where it has one, so numbers and
// booleans do not silently become strings in a client's settings.
func TestParsePrefValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"dark", "dark"},
		{"3", float64(3)},
		{"true", true},
		{"false", false},
		{"null", nil},
		{`{"left":220}`, map[string]any{"left": float64(220)}},
		{`[1,2]`, []any{float64(1), float64(2)}},
		// A quoted JSON string decodes to its content, not the quotes.
		{`"quoted"`, "quoted"},
		// Anything that is not JSON stays a plain string.
		{"2026-07-31", "2026-07-31"},
		{"", ""},
	}
	for _, tc := range cases {
		got := parsePrefValue(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parsePrefValue(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
