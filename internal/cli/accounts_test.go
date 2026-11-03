package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// --max-mailboxes has the send caps' three states (core migration 0157): a
// number, 'unlimited' as 0 (no limit), and 'default' as null, which follows
// the account's plan. It used to have two and refused 0 and 'unlimited' — the
// one value the migration leaves Free and Pro accounts at — so both are pinned
// as accepted here, and as 0, never quietly turned into null.
func TestParseMaxMailboxesFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    *int64 // nil = JSON null = follow the account's plan
		wantErr bool
	}{
		{in: "default", want: nil},
		{in: "DEFAULT", want: nil},
		{in: "", want: nil}, // an empty --flag= is the same ask
		{in: "unlimited", want: ptr(0)},
		{in: "none", want: ptr(0)},
		{in: "0", want: ptr(0)}, // the wire form of no limit, spelled directly
		{in: "5", want: ptr(5)},
		{in: " 5 ", want: ptr(5)},
		{in: "-1", wantErr: true},
		{in: "lots", wantErr: true},
		{in: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseMaxMailboxesFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseMaxMailboxesFlag(%q): expected an error, got %v", tc.in, deref64(got))
			} else if !strings.Contains(err.Error(), "--max-mailboxes") {
				t.Errorf("parseMaxMailboxesFlag(%q): error %q must name the flag", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMaxMailboxesFlag(%q): unexpected error %v", tc.in, err)
			continue
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseMaxMailboxesFlag(%q) = %d; want nil (follow the account's plan)", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseMaxMailboxesFlag(%q) = nil; want %d", tc.in, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("parseMaxMailboxesFlag(%q) = %d; want %d", tc.in, *got, *tc.want)
		}
	}
}

// The same three states on the wire, through the real command tree. On update
// 'default' is an explicit null (the ceiling follows the account's plan again);
// on create, where there is nothing to hand back, it leaves the field out like
// omitting the flag does, and the account starts on the new accounts' number.
func TestAccountsMaxMailboxesOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"update", cliAccountID, "--max-mailboxes", "40"}, `{"maxMailboxes":40}`},
		{[]string{"update", cliAccountID, "--max-mailboxes", "unlimited"}, `{"maxMailboxes":0}`},
		{[]string{"update", cliAccountID, "--max-mailboxes", "0"}, `{"maxMailboxes":0}`},
		{[]string{"update", cliAccountID, "--max-mailboxes", "default"}, `{"maxMailboxes":null}`},
		{[]string{"create", "Acme", "--max-mailboxes", "40"}, `{"maxMailboxes":40,"name":"Acme"}`},
		{[]string{"create", "Acme", "--max-mailboxes", "unlimited"}, `{"maxMailboxes":0,"name":"Acme"}`},
		{[]string{"create", "Acme", "--max-mailboxes", "default"}, `{"name":"Acme"}`},
		{[]string{"create", "Acme"}, `{"name":"Acme"}`},
	} {
		core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, cliAccount })
		if _, errOut, code := runCLI(t, core.srv.URL, append([]string{"accounts"}, tc.args...)...); code != 0 {
			t.Fatalf("%v: exit %d: %s", tc.args, code, errOut)
		}
		calls := core.callsTo()
		if len(calls) != 1 || bodyJSON(t, calls[0].Body) != tc.want {
			t.Errorf("%v: calls = %+v, want one body %s", tc.args, calls, tc.want)
		}
	}
}

// A malformed ceiling is a usage error (exit 2) that never reaches core, on
// either command — and a negative number is malformed, not "unlimited".
func TestAccountsRefuseABadMailboxCap(t *testing.T) {
	core := newMailCore(t, systemWhoami, nil)
	for _, args := range [][]string{
		{"update", cliAccountID, "--max-mailboxes", "-1"},
		{"update", cliAccountID, "--max-mailboxes", "lots"},
		{"create", "Acme", "--max-mailboxes", "-1"},
		{"create", "Acme", "--max-mailboxes", "1.5"},
	} {
		_, errOut, code := runCLI(t, core.srv.URL, append([]string{"accounts"}, args...)...)
		if code != 2 || !strings.Contains(errOut, "--max-mailboxes") {
			t.Errorf("%v: exit %d (%s), want 2 naming --max-mailboxes", args, code, errOut)
		}
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("no request for a malformed value")
	}
}

// tableValue returns the VALUE cell of a FIELD/VALUE table row.
func tableValue(out, field string) string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), field+" "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// A 0 ceiling is no limit and reads "unlimited" — a bare "0" reads as a
// ceiling of nothing — in the detail view and the list alike. A column with
// no number (only a purged account's row) reads "not set", never "unlimited".
func TestAccountsShowTheMailboxCapStates(t *testing.T) {
	for _, tc := range []struct {
		field, want string
	}{
		{`"maxMailboxes":0`, "unlimited"},
		{`"maxMailboxes":25`, "25"},
		{`"maxMailboxes":null`, "not set"},
	} {
		account := `{"id":"` + cliAccountID + `","name":"Acme",` + tc.field + `,"sendMsgsPerDay":0,"createdAt":1757000000}`
		core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, account })
		out, errOut, code := runCLI(t, core.srv.URL, "accounts", "get", cliAccountID)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", tc.field, code, errOut)
		}
		if got := tableValue(out, "Max mailboxes"); got != tc.want {
			t.Errorf("%s: Max mailboxes = %q, want %q\n%s", tc.field, got, tc.want, out)
		}
		// The send caps beside it share the vocabulary.
		if got := tableValue(out, "Messages/day"); got != "unlimited" {
			t.Errorf("%s: Messages/day = %q, want unlimited", tc.field, got)
		}
		if got := tableValue(out, "Recipients/day"); got != "not set" {
			t.Errorf("%s: Recipients/day = %q, want not set", tc.field, got)
		}
	}

	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) {
		return 200, `{"accounts":[{"id":"ACC1","name":"Free","maxMailboxes":0,"createdAt":1757000000},` +
			`{"id":"ACC2","name":"Gone","maxMailboxes":null,"createdAt":1757000000}],"nextCursor":null}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "accounts", "list")
	if code != 0 {
		t.Fatalf("list: exit %d: %s", code, errOut)
	}
	want := map[string]string{"ACC1": "unlimited", "ACC2": "—"}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || want[f[0]] == "" {
			continue
		}
		if f[3] != want[f[0]] {
			t.Errorf("list row %s: MAX MAILBOXES = %q, want %q\n%s", f[0], f[3], want[f[0]], out)
		}
		delete(want, f[0])
	}
	if len(want) != 0 {
		t.Errorf("list rows missing for %v:\n%s", want, out)
	}
}

// A null day is a finding about recorded activity, not "never": the words an
// offboarding decision reads must not overclaim. Absence is core saying
// nothing; for an identity it names a shared mailbox only when the caller knows
// the row is one, since core also withholds the day from a scoped credential's
// own identity.
func TestFmtActivityDay(t *testing.T) {
	day := "2026-10-03"
	if got := fmtActivityDay(coreapi.ActivityDay{Present: true}); got != "No recorded activity" {
		t.Fatalf("null = %q", got)
	}
	if got := fmtActivityDay(coreapi.ActivityDay{Present: true, Day: &day}); got != "2026-10-03 UTC" {
		t.Fatalf("set = %q", got)
	}
	if got := fmtActivityDay(coreapi.ActivityDay{}); got != "—" {
		t.Fatalf("absent = %q", got)
	}
	for _, word := range []string{"Never", "Inactive", "90d"} {
		if got := fmtActivityDay(coreapi.ActivityDay{Present: true}); strings.Contains(got, word) {
			t.Fatalf("null = %q overclaims with %q", got, word)
		}
	}
	if got := fmtIdentityActivity(&coreapi.Identity{}, true); !strings.Contains(got, "shared with you") {
		t.Fatalf("shared identity absent = %q, should name the shared-mailbox reading", got)
	}
	if got := fmtIdentityActivity(&coreapi.Identity{}, false); got != "—" {
		t.Fatalf("unclassified identity absent = %q, want a plain dash", got)
	}
	if got := fmtIdentityActivity(&coreapi.Identity{ActivityDay: coreapi.ActivityDay{Present: true}}, false); got != "No recorded activity" {
		t.Fatalf("own identity null = %q", got)
	}
	if got := fmtIdentityActivity(&coreapi.Identity{ActivityDay: coreapi.ActivityDay{Present: true}}, true); got != "No recorded activity" {
		t.Fatalf("null is a finding even on a row thought shared, got %q", got)
	}
}

// The account freeze is the widest abuse control the CLI exposes, so its
// rendering must be unmissable when disabled and quiet when not — and it must say
// the SCOPE, since the reason to reach for it over the per-mailbox freeze is
// that it also covers mailboxes the tenant has not created yet.
func TestFmtAccountSendState(t *testing.T) {
	if got := fmtAccountSendState(nil); got != "enabled" {
		t.Errorf("fmtAccountSendState(live) = %q; want %q", got, "enabled")
	}
	disabled := fmtAccountSendState(strPtr("disabled"))
	if !strings.Contains(disabled, "DISABLED") {
		t.Errorf("fmtAccountSendState(disabled) = %q; want it to shout DISABLED", disabled)
	}
	for _, want := range []string{"every mailbox", "every domain", "relay"} {
		if !strings.Contains(disabled, want) {
			t.Errorf("fmtAccountSendState(disabled) = %q; want it to mention %q", disabled, want)
		}
	}

	// The HOLD must read as a DIFFERENT state, and must say what happens to the
	// queued mail: an operator choosing between the two is choosing between
	// bouncing a tenant's mail and holding it, which is the whole distinction.
	paused := fmtAccountSendState(strPtr("paused"))
	if !strings.Contains(paused, "PAUSED") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to shout PAUSED", paused)
	}
	if strings.Contains(paused, "DISABLED") {
		t.Errorf("fmtAccountSendState(paused) = %q; must not read as a freeze", paused)
	}
	if !strings.Contains(paused, "held") {
		t.Errorf("fmtAccountSendState(paused) = %q; want it to say the mail is held", paused)
	}

	// Both set resolves toward the FREEZE, matching what core actually does — so
	// the CLI can never describe behavior the tenant is not getting.
	if got := fmtAccountSendState(strPtr("disabled")); !strings.Contains(got, "DISABLED") {
		t.Errorf("fmtAccountSendState(both) = %q; want the permanent answer to win", got)
	}
}

// The hold verbs moved to `admin hold`/`admin release`; exactly one mode is
// required there, and the usage error fires BEFORE authentication, whatever
// credentials the caller holds.
func TestAdminHoldRequiresExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{
		{"account", "ACC1", "--pause", "--stop"},
		{"account", "ACC1"},
	} {
		cmd := newAdminHoldCmd(&app{out: newPrinter(false, true)})
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%v: error = %v; want the exactly-one usage error", args, err)
		}
	}
}
