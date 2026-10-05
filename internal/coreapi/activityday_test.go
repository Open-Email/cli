package coreapi

import (
	"encoding/json"
	"testing"
)

// activityDay is optional while consumers support both directory authorities.
// Its explicit null must survive decoding and JSON output, rather than become
// indistinguishable from an absent field that permits the legacy fallback.
func TestActivityDayWireRoundTrip(t *testing.T) {
	cases := []struct {
		name, body, want string
		present          bool
	}{
		{"fresh day", `{"activityDay":"2026-10-03","lastClientAt":null}`, `"2026-10-03"`, true},
		{"conflicting stamp", `{"activityDay":"2026-10-03","lastClientAt":1735689600}`, `"2026-10-03"`, true},
		{"explicit null", `{"activityDay":null,"lastClientAt":1735689600}`, `null`, true},
		{"legacy stamp", `{"lastClientAt":1735689600}`, "", false},
		{"legacy null", `{"lastClientAt":null}`, "", false},
	}
	for _, entity := range []string{"account", "identity"} {
		for _, tc := range cases {
			t.Run(entity+"/"+tc.name, func(t *testing.T) {
				var decoded any
				if entity == "account" {
					decoded = &Account{}
				} else {
					decoded = &Identity{}
				}
				if err := json.Unmarshal([]byte(tc.body), decoded); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(decoded)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				value, present := fields["activityDay"]
				if present != tc.present || string(value) != tc.want {
					t.Fatalf("activityDay = %s (present %v), want %s (present %v)", value, present, tc.want, tc.present)
				}
				if tc.name == "legacy stamp" && string(fields["lastClientAt"]) != "1735689600" {
					t.Fatalf("legacy stamp changed: %s", encoded)
				}
			})
		}
	}
}

func TestActivityDayRejectsNonStringNonNull(t *testing.T) {
	for _, body := range []string{`{"activityDay":123}`, `{"activityDay":false}`} {
		for _, decoded := range []any{&Account{}, &Identity{}} {
			if err := json.Unmarshal([]byte(body), decoded); err == nil {
				t.Errorf("%T accepted activityDay outside string or null: %s", decoded, body)
			}
		}
	}
}
