package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
	tea "github.com/charmbracelet/bubbletea"
)

type ephemeralTransport func(*http.Request) (*http.Response, error)

func (f ephemeralTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ephemeralClient(t *testing.T, handler func(*http.Request) string) *coreapi.Client {
	t.Helper()
	c, err := coreapi.New(coreapi.Config{
		BaseURL: "https://core.example.test", Token: "oek_test",
		HTTPClient: &http.Client{Transport: ephemeralTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(handler(r))), Request: r,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEphemeralMailboxRows(t *testing.T) {
	c := ephemeralClient(t, func(r *http.Request) string {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/identities" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		return `{"identities":[
			{"id":"ephemeral","primaryAddress":"temp@example.test","ephemeral":true,"sendHold":"paused"},
			{"id":"ordinary","primaryAddress":"keep@example.test","ephemeral":false},
			{"id":"pim","primaryAddress":null,"ephemeral":true}
		]}`
	})
	rows, _, err := mailboxesDesc().fetch(t.Context(), c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 mailbox rows, got %d", len(rows))
	}
	for i, want := range []string{"temp@example.test [EPHEMERAL] [PAUSED]", "keep@example.test", "— [EPHEMERAL]"} {
		if got := rows[i].cells[1]; got != want {
			t.Errorf("row %d address = %q; want %q", i, got, want)
		}
	}
}

func TestEphemeralMailboxCreateForm(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(fmt.Sprint(ephemeral), func(t *testing.T) {
			var body map[string]any
			c := ephemeralClient(t, func(r *http.Request) string {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/identities" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				return fmt.Sprintf(`{"id":"new","primaryAddress":"temp@example.test","ephemeral":%t}`, body["ephemeral"] == true)
			})
			p := mailboxFormPane(t.Context(), &Options{Client: c}, nil).(*formPane)
			p.setSize(100, 80)
			p.init()
			if view := strings.Join(strings.Fields(p.view()), " "); !strings.Contains(view, "Ephemeral") || !strings.Contains(view, "cannot be changed later") {
				t.Fatalf("new mailbox form must explain the immutable ephemeral choice: %s", view)
			}
			p.form.GetFocusedField().Focus()
			p.form.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("temp@example.test")})
			p.form.NextField() // quota
			p.form.NextField() // without address, default no
			p.form.NextField() // ephemeral, default no
			if ephemeral {
				p.form.Update(tea.KeyMsg{Type: tea.KeyUp})
			}
			if got := p.form.GetFocusedField().GetValue(); got != ephemeral {
				t.Fatalf("ephemeral form value = %v; want %v", got, ephemeral)
			}
			if _, _, err := p.spec.submit(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if body["primaryAddress"] != "temp@example.test" {
				t.Fatalf("create body = %v", body)
			}
			if ephemeral && body["ephemeral"] != true {
				t.Fatalf("ephemeral choice missing from create request: %v", body)
			}
			if !ephemeral {
				if _, present := body["ephemeral"]; present {
					t.Fatalf("default create must omit ephemeral: %v", body)
				}
			}
		})
	}
}

func TestEphemeralMailboxEditCannotChangeFlag(t *testing.T) {
	var body map[string]any
	c := ephemeralClient(t, func(r *http.Request) string {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/identities/temp" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		return `{"id":"temp","primaryAddress":"temp@example.test","ephemeral":true}`
	})
	address := "temp@example.test"
	p := mailboxFormPane(t.Context(), &Options{Client: c}, &coreapi.Mailbox{
		ID: "temp", PrimaryAddress: &address, Ephemeral: true,
	}).(*formPane)
	p.setSize(100, 80)
	p.init()
	if view := p.view(); strings.Contains(view, "Ephemeral") {
		t.Fatalf("edit must not offer a change to the immutable ephemeral flag: %s", view)
	}
	flash, _, err := p.spec.submit(t.Context(), nil)
	if err != nil || flash != "no changes" {
		t.Fatalf("unchanged ephemeral mailbox edit = %q, %v", flash, err)
	}
	p.form.NextField() // quota
	p.form.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1G")})
	if _, _, err := p.spec.submit(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["quotaBytes"] != float64(1<<30) {
		t.Fatalf("editing quota must not patch the immutable ephemeral flag: %v", body)
	}
}

func TestEphemeralMailboxDeleteConfirmation(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(fmt.Sprint(ephemeral), func(t *testing.T) {
			p := mailboxDeleteConfirm(t.Context(), &Options{}, coreapi.Mailbox{ID: "temp", Ephemeral: ephemeral}).(*confirmPane)
			body := p.spec.body
			if ephemeral {
				for _, want := range []string{"Permanently delete", "ephemeral", "no undo window", "cannot be restored"} {
					if !strings.Contains(body, want) {
						t.Errorf("ephemeral delete confirmation missing %q: %s", want, body)
					}
				}
				if strings.Contains(body, "7 days") || strings.Contains(body, "mailboxes restore") || strings.Contains(body, "system key") {
					t.Fatalf("ephemeral delete must not promise restoration or require a system key: %s", body)
				}
			} else if !strings.Contains(body, "restorable for 7 days") || !strings.Contains(body, "mailboxes restore temp") {
				t.Fatalf("ordinary mailbox delete must retain its restore guidance: %s", body)
			}
		})
	}
}

func TestEphemeralMailboxDeleteResult(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
	}{
		{"purged", `{"deleted":true,"purged":true,"restorable":false}`, "mailbox permanently deleted — no undo"},
		{"restorable", `{"deleted":true,"restorable":true,"restorableUntil":1770000000}`, "mailbox deleted — restorable until "},
		{"not restorable", `{"deleted":true,"restorable":false}`, "mailbox deleted — no longer restorable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := ephemeralClient(t, func(r *http.Request) string {
				calls++
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/identities/temp" || r.URL.RawQuery != "" {
					t.Errorf("normal delete must not request purge: %s %s", r.Method, r.URL)
				}
				return tc.response
			})
			// Use an ordinary snapshot to ensure the success notice follows the
			// server's actual outcome, even if the list did not include ephemeral.
			p := mailboxDeleteConfirm(t.Context(), &Options{Client: c}, coreapi.Mailbox{ID: "temp"}).(*confirmPane)
			flash, err := p.spec.submit(t.Context(), c)
			if err != nil || !strings.HasPrefix(flash, tc.want) || calls != 1 {
				t.Fatalf("delete result = %q, %v (requests %d); want %q", flash, err, calls, tc.want)
			}
		})
	}
}

func TestEphemeralMailboxPurgeRequiresSystemKey(t *testing.T) {
	p := mailboxPurgeConfirm(t.Context(), &Options{}, coreapi.DeletedMailbox{ID: "temp"}).(*confirmPane)
	for _, want := range []string{"requires a system key in every case", "Ephemeral mailboxes are destroyed at deletion and have no tombstone to purge"} {
		if !strings.Contains(p.spec.body, want) {
			t.Errorf("purge confirmation missing %q: %s", want, p.spec.body)
		}
	}
}
