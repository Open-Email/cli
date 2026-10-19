package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
)

// --expires speaks the vocabulary the rest of the CLI already does (an age as
// credentials take it, an absolute as the PIM commands take it) and refuses a
// past instant locally, naming what it parsed, because core's own refusal is a
// round trip and a bare invalid_expiry.
func TestParseExpiresFlag(t *testing.T) {
	if got, err := parseExpiresFlag(""); err != nil || got != nil {
		t.Fatalf("empty must mean never (nil, nil), got %v, %v", got, err)
	}
	now := time.Now().Unix()
	for _, tc := range []struct {
		in   string
		from time.Duration
		to   time.Duration
	}{
		{"30d", 30*24*time.Hour - time.Minute, 30*24*time.Hour + time.Minute},
		{"12h", 12*time.Hour - time.Minute, 12*time.Hour + time.Minute},
	} {
		got, err := parseExpiresFlag(tc.in)
		if err != nil || got == nil {
			t.Fatalf("parseExpiresFlag(%q): %v, %v", tc.in, got, err)
		}
		if *got < now+int64(tc.from.Seconds()) || *got > now+int64(tc.to.Seconds()) {
			t.Fatalf("parseExpiresFlag(%q) = %d, want about now+%s", tc.in, *got, tc.to)
		}
	}
	future := now + 86400
	if got, err := parseExpiresFlag(strings.TrimSpace(time.Unix(future, 0).UTC().Format(time.RFC3339))); err != nil || got == nil || *got != future {
		t.Fatalf("RFC3339 round trip: %v, %v", got, err)
	}
	for _, bad := range []string{"2001-01-01", "0", "yesterday", "-3d", "0d"} {
		_, err := parseExpiresFlag(bad)
		if err == nil {
			t.Fatalf("parseExpiresFlag(%q) should have failed", bad)
		}
		if !strings.Contains(err.Error(), "--expires") {
			t.Fatalf("parseExpiresFlag(%q) error should name the flag, got %v", bad, err)
		}
	}
}

// One row shape for both entry listings. The three new columns are the ones
// that make a listing actionable: an entry with an expiry must not display as
// permanent, and an entry that has never decided anything is the one to prune.
func TestEntryCells(t *testing.T) {
	exp := int64(1893456000)
	hit := int64(1757000000)
	note := "vendor allow"
	got := entryCells(coreapi.AddressListEntry{
		Pattern: "*@acme.example", CreatedAt: 1735689600, Note: &note,
		ExpiresAt: &exp, HitCount: 1204, LastHitAt: &hit,
	}, 40)
	want := entryHeaders("PATTERN")
	if len(got) != len(want) {
		t.Fatalf("cells/headers disagree: %d cells for %d headers", len(got), len(want))
	}
	if got[2] != fmtEpoch(exp) {
		t.Fatalf("EXPIRES = %q, want the formatted expiry", got[2])
	}
	if got[3] != "1,204" {
		t.Fatalf("HITS = %q, want thousands-separated 1,204", got[3])
	}
	if got[4] != fmtEpoch(hit) {
		t.Fatalf("LAST HIT = %q", got[4])
	}
	if got[5] != note {
		t.Fatalf("NOTE = %q", got[5])
	}

	bare := entryCells(coreapi.AddressListEntry{Pattern: "x@y.example", CreatedAt: 1}, 40)
	if bare[2] != "—" || bare[4] != "—" || bare[3] != "0" {
		t.Fatalf("a permanent, never-matched entry should read — / 0 / —, got %v", bare[2:5])
	}
}

// `*@*` is how a tenant says "block all, allow some" (core item 14), the one
// pattern that recipe cannot do without, and neither help named it.
func TestListsHelpNamesTheCatchAll(t *testing.T) {
	for _, args := range [][]string{nil, {"add"}} {
		help := helpText(t, newListsCmd(&app{}), args...)
		for _, want := range []string{"`*@*`", "block lists only"} {
			if !strings.Contains(help, want) {
				t.Errorf("lists %v help lacks %q:\n%s", args, want, help)
			}
		}
	}
}

// End to end (cli#53): an app password refused on an outbound list sees core's
// own sentence and a remedy it can act on, never the domain-key explanation.
func TestListsCreateOutboundWithAppPasswordExplainsTheRefusal(t *testing.T) {
	const mbx = "01KXRS3SHN1N35G4YETVADSN0R"
	const coreMessage = "outbound address lists are managed by the account; this credential can change inbound lists only"
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/mailboxes/"+mbx+"/lists" {
			posted = true
			if got := r.Header.Get("Authorization"); got != "Bearer oemp_test" {
				t.Errorf("Authorization = %q, want the app password", got)
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"account_credentials_required","message":"`+coreMessage+`"}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not_found"}`)
	}))
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "--api-key", "oemp_test",
		"lists", "create", "mine", "-m", mbx, "--direction", "outbound", "--verdict", "allow")
	if !posted {
		t.Fatalf("the create never reached core; stderr:\n%s", stderr)
	}
	if code == 0 {
		t.Fatalf("exit 0 on a refused create; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, coreMessage) {
		t.Errorf("stderr lacks core's message:\n%s", stderr)
	}
	if strings.Contains(stderr, "specific domains") {
		t.Errorf("stderr blames a domain restriction:\n%s", stderr)
	}
	if !strings.Contains(stderr, "account key") {
		t.Errorf("stderr gives no remedy:\n%s", stderr)
	}
}

// Since core#533 an app password changes its mailbox's inbound lists only;
// the help says so before anyone meets the 403.
func TestListsHelpSaysAppPasswordsChangeInboundOnly(t *testing.T) {
	help := helpText(t, newListsCmd(&app{}))
	for _, want := range []string{"app password", "inbound lists", "outbound lists are the account's"} {
		if !strings.Contains(help, want) {
			t.Errorf("lists help lacks %q:\n%s", want, help)
		}
	}
}
