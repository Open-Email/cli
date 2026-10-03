package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActivityDayTUIAccountRendering(t *testing.T) {
	stamp := int64(1735689600)
	cases := []struct {
		name, fields, want string
	}{
		{"fresh day", `"activityDay":"2026-10-03","lastClientAt":null`, "2026-10-03 (UTC day)"},
		{"conflicting stamp", `"activityDay":"2026-10-03","lastClientAt":1735689600`, "2026-10-03 (UTC day)"},
		{"explicit null", `"activityDay":null,"lastClientAt":1735689600`, "none recorded"},
		{"legacy stamp", `"lastClientAt":1735689600`, lastClientOr(&stamp)},
		{"legacy null", `"lastClientAt":null`, lastClientOr(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/accounts" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				fmt.Fprintf(w, `{"accounts":[{"id":"id1","name":"Acme",%s}]}`, tc.fields)
			}))
			defer srv.Close()
			desc := accountsDesc()
			rows, _, err := desc.fetch(context.Background(), newTestClient(t, srv.URL), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].cells[len(rows[0].cells)-1] != tc.want {
				t.Errorf("list = %+v, want activity %q", rows, tc.want)
			}
			if len(rows) != 1 {
				return
			}
			for _, field := range desc.detail(rows[0].item) {
				if field.k == "last client" {
					if field.v != tc.want {
						t.Errorf("detail activity = %q, want %q", field.v, tc.want)
					}
					return
				}
			}
			t.Fatal("missing last client detail")
		})
	}
}
