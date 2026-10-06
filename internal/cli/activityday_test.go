package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The day is the only activity the CLI shows. A retired lastClientAt that an
// older core still sends must never surface, not even where activityDay is
// null or absent: that would be the fallback this change removes.
//
// The identity view reads as an account key here, so an absent day is core
// saying nothing and renders as the plain dash; the shared-mailbox wording
// needs a bearer known to be reading someone else's mailbox
// (TestIdentityActivityShared).
func TestActivityDayCLIRendering(t *testing.T) {
	cases := []struct {
		name, fields, account, identity string
	}{
		{"fresh day", `"activityDay":"2026-10-03"`, "2026-10-03 UTC", "2026-10-03 UTC"},
		{"explicit null", `"activityDay":null`, "No recorded activity", "No recorded activity"},
		{"absent", `"createdAt":0`, "—", "—"},
		{"retired stamp beside a day", `"activityDay":"2026-10-03","lastClientAt":1735689600`, "2026-10-03 UTC", "2026-10-03 UTC"},
		{"retired stamp beside null", `"activityDay":null,"lastClientAt":1735689600`, "No recorded activity", "No recorded activity"},
		{"retired stamp alone", `"lastClientAt":1735689600`, "—", "—"},
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
					if view == "identity detail" && r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/whoami" {
						fmt.Fprint(w, `{"type":"account","accountId":"acc1"}`)
						return
					}
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
				if strings.Contains(out.String(), fmtEpoch(1735689600)) {
					t.Fatalf("retired lastClientAt rendered:\n%s", out.String())
				}
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.HasPrefix(line, "Last activity") {
						if got := strings.TrimSpace(strings.TrimPrefix(line, "Last activity")); got != want {
							t.Fatalf("last activity = %q, want %q", got, want)
						}
						return
					}
				}
				t.Fatalf("missing Last activity row:\n%s", out.String())
			})
		}
	}
}

// An absent identity day is withheld for more than one reason, so the
// shared-mailbox wording is earned only by a row known to be shared: a mailbox
// bearer reading an identity that is not its own. The bearer's own identity
// reads null as "No recorded activity" and an absent day as a plain dash (a
// credential below full scope is withheld the same field), and a whoami that
// fails leaves the row unclassified rather than guessed at.
func TestIdentityActivityShared(t *testing.T) {
	const shared = "— (a mailbox shared with you)"
	cases := []struct {
		name, args, whoami, fields, want string
		whoamiCalls                      int
	}{
		{"own identity, null", "", `{"type":"mailbox","mailboxId":"id1"}`, `"activityDay":null`, "No recorded activity", 1},
		{"own identity, day", "", `{"type":"mailbox","mailboxId":"id1"}`, `"activityDay":"2026-10-03"`, "2026-10-03 UTC", 1},
		{"own identity, absent", "", `{"type":"mailbox","mailboxId":"id1"}`, `"createdAt":0`, "—", 1},
		{"own identity by id, null", "id1", `{"type":"mailbox","mailboxId":"id1"}`, `"activityDay":null`, "No recorded activity", 0},
		{"own identity by id, absent", "id1", `{"type":"mailbox","mailboxId":"id1"}`, `"createdAt":0`, "—", 1},
		{"shared mailbox, absent", "id1", `{"type":"mailbox","mailboxId":"own1"}`, `"createdAt":0`, shared, 1},
		{"account key, absent", "id1", `{"type":"account","accountId":"acc1"}`, `"createdAt":0`, "—", 1},
		{"whoami fails, absent", "id1", "", `"createdAt":0`, "—", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			whoamiCalls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/whoami":
					whoamiCalls++
					if tc.whoami == "" {
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprint(w, `{"error":"internal"}`)
						return
					}
					fmt.Fprint(w, tc.whoami)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities/id1":
					fmt.Fprintf(w, `{"id":"id1",%s}`, tc.fields)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
			cmd := newIdentityGetCmd(a)
			if tc.args != "" {
				cmd.SetArgs([]string{tc.args})
			} else {
				cmd.SetArgs([]string{})
			}
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if whoamiCalls != tc.whoamiCalls {
				t.Fatalf("whoami called %d times, want %d", whoamiCalls, tc.whoamiCalls)
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(line, "Last activity") {
					if got := strings.TrimSpace(strings.TrimPrefix(line, "Last activity")); got != tc.want {
						t.Fatalf("last activity = %q, want %q", got, tc.want)
					}
					return
				}
			}
			t.Fatalf("missing Last activity row:\n%s", out.String())
		})
	}
}
