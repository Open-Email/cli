package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActivityDayCLIRendering(t *testing.T) {
	stamp := int64(1735689600)
	cases := []struct {
		name, fields, account, identity string
	}{
		{"fresh day", `"activityDay":"2026-10-03","lastClientAt":null`, "2026-10-03 UTC", "2026-10-03 UTC"},
		{"conflicting stamp", `"activityDay":"2026-10-03","lastClientAt":1735689600`, "2026-10-03 UTC", "2026-10-03 UTC"},
		{"explicit null", `"activityDay":null,"lastClientAt":1735689600`, "none recorded", "none recorded"},
		{"legacy stamp", `"lastClientAt":1735689600`, fmtLastClient(&stamp), fmtLastClientIdentity(&stamp)},
		{"legacy null", `"lastClientAt":null`, fmtLastClient(nil), fmtLastClientIdentity(nil)},
	}
	for _, view := range []string{"account list", "account detail", "identity detail"} {
		for _, tc := range cases {
			t.Run(view+"/"+tc.name, func(t *testing.T) {
				path := "/api/v1/accounts"
				body := fmt.Sprintf(`{"id":"id1","name":"Acme",%s}`, tc.fields)
				want := tc.account
				if view == "account list" {
					body = `{"accounts":[` + body + `]}`
				} else if view == "account detail" {
					path += "/id1"
				} else {
					path = "/api/v1/identities/id1"
					want = tc.identity
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != path {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprint(w, body)
				}))
				defer srv.Close()
				var out bytes.Buffer
				a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
				cmd := newAccountListCmd(a)
				if view == "account detail" {
					cmd = newAccountGetCmd(a)
					cmd.SetArgs([]string{"id1"})
				} else if view == "identity detail" {
					cmd = newIdentityGetCmd(a)
					cmd.SetArgs([]string{"id1"})
				}
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				if view == "account list" {
					lines := strings.Split(strings.TrimSpace(out.String()), "\n")
					if len(lines) != 2 || !strings.HasSuffix(strings.TrimSpace(lines[1]), want) {
						t.Fatalf("list must end in %q:\n%s", want, out.String())
					}
					return
				}
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.HasPrefix(line, "Last client") {
						if got := strings.TrimSpace(strings.TrimPrefix(line, "Last client")); got != want {
							t.Fatalf("last client = %q, want %q", got, want)
						}
						return
					}
				}
				t.Fatalf("missing Last client row:\n%s", out.String())
			})
		}
	}
}
