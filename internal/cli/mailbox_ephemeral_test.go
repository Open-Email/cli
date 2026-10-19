package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

const ephemeralMailboxID = "01KXRS3SHN1N35G4YETVADSN0R"

func ephemeralMailboxJSON(ephemeral bool) string {
	return fmt.Sprintf(`{"id":%q,"primaryAddress":"disposable@acme.test","accountId":"ACC1","createdAt":1700000000,"ephemeral":%t}`, ephemeralMailboxID, ephemeral)
}

func TestMailboxGetShowsEphemeralState(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		for _, jsonMode := range []bool{false, true} {
			for _, withoutMailStore := range []bool{false, true} {
				t.Run(fmt.Sprintf("ephemeral=%t/json=%t/withoutMailStore=%t", ephemeral, jsonMode, withoutMailStore), func(t *testing.T) {
					core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
						if r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities/"+ephemeralMailboxID {
							if withoutMailStore {
								return 200, fmt.Sprintf(`{"id":%q,"primaryAddress":null,"ephemeral":%t,"facets":{}}`, ephemeralMailboxID, ephemeral)
							}
							return 200, ephemeralMailboxJSON(ephemeral)
						}
						if r.Method == http.MethodGet && r.URL.Path == "/api/v1/mailboxes/"+ephemeralMailboxID {
							if withoutMailStore {
								t.Error("calendar-only identity must not need a mail store read")
								return 409, `{"error":"mail_store_unavailable"}`
							}
							return 200, ephemeralMailboxJSON(ephemeral)
						}
						return 404, `{"error":"not_found"}`
					})
					args := []string{"mailboxes", "get", ephemeralMailboxID}
					if jsonMode {
						args = append(args, "--json")
					}
					out, errOut, code := runCLI(t, core.srv.URL, args...)
					if code != 0 {
						t.Fatalf("exit %d: %s", code, errOut)
					}
					if jsonMode {
						var got map[string]any
						if err := json.Unmarshal([]byte(out), &got); err != nil || got["ephemeral"] != ephemeral {
							t.Fatalf("stdout = %q (%v), want ephemeral %t", out, err, ephemeral)
						}
						return
					}
					want := "no"
					if ephemeral {
						want = "yes"
					}
					found := false
					for _, line := range strings.Split(out, "\n") {
						fields := strings.Fields(line)
						if len(fields) >= 2 && fields[0] == "Ephemeral" && fields[1] == want {
							found = true
						}
					}
					if !found {
						t.Fatalf("stdout = %q, want an Ephemeral row saying %s", out, want)
					}
				})
			}
		}
	}
}

func TestMailboxListMarksOnlyEphemeralMailboxes(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities" {
					return 200, `{"identities":[{"id":"ordinary","primaryAddress":"ordinary@acme.test","ephemeral":false},{"id":"disposable","primaryAddress":"disposable@acme.test","ephemeral":true}],"nextCursor":""}`
				}
				return 404, `{"error":"not_found"}`
			})
			args := []string{"mailboxes", "list"}
			if jsonMode {
				args = append(args, "--json")
			}
			out, errOut, code := runCLI(t, core.srv.URL, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if jsonMode {
				var got struct {
					Mailboxes []map[string]any `json:"mailboxes"`
				}
				if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Mailboxes) != 2 {
					t.Fatalf("stdout = %q (%v), want two mailbox objects", out, err)
				}
				if got.Mailboxes[0]["ephemeral"] != false || got.Mailboxes[1]["ephemeral"] != true ||
					got.Mailboxes[0]["primaryAddress"] != "ordinary@acme.test" || got.Mailboxes[1]["primaryAddress"] != "disposable@acme.test" {
					t.Fatalf("mailboxes = %v, want unchanged addresses and boolean ephemeral state", got.Mailboxes)
				}
				return
			}
			for _, address := range []string{"ordinary@acme.test", "disposable@acme.test"} {
				found := false
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, address) {
						found = true
						if marked := strings.Contains(line, "[EPHEMERAL]"); marked != (address == "disposable@acme.test") {
							t.Fatalf("row = %q, ephemeral marker belongs only on disposable mailbox", line)
						}
					}
				}
				if !found {
					t.Fatalf("stdout = %q, missing %s", out, address)
				}
			}
		})
	}
}

func TestMailboxDeleteConfirmationReflectsIrreversibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ephemeral bool
		purge     bool
		typed     bool
	}{
		{"ordinary delete", false, false, false},
		{"ephemeral delete", true, false, true},
		{"ordinary purge", false, true, true},
		{"ephemeral purge", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt, typed := mailboxDeleteConfirmation(coreapi.Mailbox{ID: ephemeralMailboxID, Ephemeral: tc.ephemeral}, tc.purge)
			if typed != tc.typed || !strings.Contains(prompt, ephemeralMailboxID) {
				t.Fatalf("confirmation = (%q, %t), want mailbox id and typed=%t", prompt, typed, tc.typed)
			}
			if tc.typed {
				lower := strings.ToLower(prompt)
				permanent := strings.Contains(lower, "irreversible") || (strings.Contains(lower, "permanent") && strings.Contains(lower, "no undo"))
				if !permanent || strings.Contains(prompt, "undoable") {
					t.Fatalf("prompt = %q, irreversible deletion must not promise undo", prompt)
				}
			} else if !strings.Contains(prompt, "undoable for 7 days") {
				t.Fatalf("prompt = %q, ordinary delete must explain its undo window", prompt)
			}
		})
	}
}

func TestMailboxDeleteChecksStateAndAbortsWithoutConfirmation(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(fmt.Sprintf("ephemeral=%t", ephemeral), func(t *testing.T) {
			core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/identities/"+ephemeralMailboxID {
					return 200, ephemeralMailboxJSON(ephemeral)
				}
				return 200, `{"deleted":true,"purged":true,"restorable":false}`
			})
			_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "delete", ephemeralMailboxID)
			if code != 2 || !strings.Contains(errOut, "aborted") {
				t.Fatalf("exit %d, stderr %q; want confirmation abort", code, errOut)
			}
			if typed := strings.Contains(errOut, "type the mailbox id"); typed != ephemeral {
				t.Fatalf("stderr = %q, typed confirmation must apply only to ephemeral delete", errOut)
			}
			calls := core.callsTo()
			if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/api/v1/identities/"+ephemeralMailboxID {
				t.Fatalf("calls = %v, want only the state lookup and no deletion", calls)
			}
		})
	}
}

func TestMailboxDeleteLookupFailureCannotDelete(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return 403, `{"error":"forbidden"}`
		}
		return 200, `{"deleted":true,"purged":true,"restorable":false}`
	})
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "delete", ephemeralMailboxID)
	if code == 0 || !strings.Contains(errOut, "forbidden") {
		t.Fatalf("exit %d, stderr %q; want state lookup failure", code, errOut)
	}
	calls := core.callsTo()
	if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/api/v1/identities/"+ephemeralMailboxID {
		t.Fatalf("calls = %v, a failed state lookup must prevent deletion", calls)
	}
}

func TestMailboxDeleteYesSkipsLookupAndReportsPurgedResult(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
				if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/identities/"+ephemeralMailboxID {
					if r.URL.RawQuery != "" {
						t.Errorf("query = %q, ordinary delete must leave ephemeral handling to Core", r.URL.RawQuery)
					}
					return 200, `{"deleted":true,"purged":true,"restorable":false}`
				}
				return 403, `{"error":"forbidden"}`
			})
			args := []string{"mailboxes", "delete", ephemeralMailboxID, "--yes"}
			if jsonMode {
				args = append(args, "--json")
			}
			out, errOut, code := runCLI(t, core.srv.URL, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			calls := core.callsTo()
			if len(calls) != 1 || calls[0].Method != http.MethodDelete || calls[0].Path != "/api/v1/identities/"+ephemeralMailboxID {
				t.Fatalf("calls = %v, --yes must not add a state lookup", calls)
			}
			if jsonMode {
				var got map[string]any
				if err := json.Unmarshal([]byte(out), &got); err != nil || got["purged"] != true || got["restorable"] != false {
					t.Fatalf("stdout = %q (%v), want Core's irreversible result", out, err)
				}
				return
			}
			if !strings.Contains(errOut, "Purged mailbox "+ephemeralMailboxID) || !strings.Contains(errOut, "not restorable") || strings.Contains(errOut, "restorable until") {
				t.Fatalf("stderr = %q, want purged wording without an undo promise", errOut)
			}
		})
	}
}

func TestMailboxDeletePurgeRequiresNoStateLookup(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/identities/"+ephemeralMailboxID && r.URL.Query().Get("purge") == "true" {
			return 200, `{"deleted":true,"purged":true,"restorable":false}`
		}
		return 403, `{"error":"forbidden"}`
	})
	if _, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "delete", ephemeralMailboxID, "--purge"); code != 2 || !strings.Contains(errOut, "type the mailbox id") {
		t.Fatalf("exit %d, stderr %q; want typed confirmation abort", code, errOut)
	}
	if calls := core.callsTo(); len(calls) != 0 {
		t.Fatalf("calls = %v, explicit purge needs no state lookup and must abort before deletion", calls)
	}
	if _, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "delete", ephemeralMailboxID, "--purge", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if calls := core.callsTo(); len(calls) != 1 || calls[0].Method != http.MethodDelete {
		t.Fatalf("calls = %v, explicit purge must send only its delete request", calls)
	}
}
