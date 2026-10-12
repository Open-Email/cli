package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestCalendarOnlyMailboxGet(t *testing.T) {
	mailReads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/identities/pim" {
			fmt.Fprint(w, `{"id":"pim","facets":{"pim":{}}}`)
			return
		}
		mailReads++
		fmt.Fprint(w, `{"id":"pim","messageCount":0,"usedBytes":0}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
	cmd := newMailboxGetCmd(a)
	cmd.SetArgs([]string{"pim"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No mail store") || strings.Contains(out.String(), "Messages") {
		t.Fatalf("calendar-only identity rendered as mailbox usage:\n%s", out.String())
	}
	if mailReads != 0 {
		t.Fatalf("made %d mail reads for a calendar-only identity", mailReads)
	}
}

func TestCalendarOnlyMailboxGetRetainsBoundAndUnknownUsage(t *testing.T) {
	for _, facets := range []string{`"facets":{"mail":{"provisioned":false}}`, `"facets":null`, `"legacy":true`} {
		t.Run(facets, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/identities/bound" {
					fmt.Fprintf(w, `{"id":"bound",%s}`, facets)
					return
				}
				fmt.Fprint(w, `{"id":"bound","messageCount":4,"usedBytes":123}`)
			}))
			defer srv.Close()
			var out bytes.Buffer
			a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
			cmd := newMailboxGetCmd(a)
			cmd.SetArgs([]string{"bound"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Messages") || !strings.Contains(out.String(), "123 B") || strings.Contains(out.String(), "No mail store") {
				t.Fatalf("lost existing mail usage: %s", out.String())
			}
		})
	}
}

func TestCalendarOnlyDefaultMailboxRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/identities/pim" {
			fmt.Fprint(w, `{"id":"pim","facets":{}}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not_found"}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
	a.profile.DefaultMailbox = "pim"
	cmd := newRulesListCmd(a)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no mail store") {
		t.Fatalf("want a no-mail explanation instead of empty rules, got error %v, output %q", err, out.String())
	}
}

func TestCalendarOnlyErrorHints(t *testing.T) {
	for _, code := range []string{"no_mail_store", "grantee_has_no_mail_store", "notice_mailbox_no_mail_store"} {
		t.Run(code, func(t *testing.T) {
			if hint := errorHint(&coreapi.APIError{Code: code}); !strings.Contains(hint, "mail store") {
				t.Fatalf("missing actionable mail-store hint for %s: %q", code, hint)
			}
		})
	}
}
