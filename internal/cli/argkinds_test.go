package cli

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every placeholder in every Use line must be in argKinds — free-form ones
// included. The table is what turns "we did not think about it" into "we
// decided not to check it", and a new command with a `<fooId>` argument gets
// classified here rather than silently passing anything through to a 404.
func TestArgPlaceholdersAreClassified(t *testing.T) {
	root := newRootCmd(&app{})
	unknown := map[string][]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if _, missing := usePositionals(c.Use); len(missing) > 0 {
			for _, m := range missing {
				unknown[m] = append(unknown[m], c.CommandPath())
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	if len(unknown) == 0 {
		return
	}
	names := make([]string, 0, len(unknown))
	for n := range unknown {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Errorf("placeholder <%s> has no entry in argKinds (used by %s)", n, strings.Join(unknown[n], ", "))
	}
}

func TestUsePositionals(t *testing.T) {
	cases := []struct {
		use  string
		want []positional
	}{
		{"get <mailboxId>", []positional{{"mailboxId", kindULID, false}}},
		// a placeholder after a flag is that flag's value, never positional
		{"check --to <address>", nil},
		{"move <messageId> --from <label> --to <label>", []positional{{"messageId", kindULID, false}}},
		// a parenthesised flag group is skipped whole
		{"hold <domain|mailbox|account> <id> (--pause | --stop)", []positional{{"domain|mailbox|account", kindFree, false}, {"id", kindFree, false}}},
		// [x...] is a variadic tail
		{"restore <messageId> [messageId...]", []positional{{"messageId", kindULID, false}, {"messageId", kindULID, true}}},
		{"import <list-id> <pattern>...", []positional{{"list-id", kindFree, false}, {"pattern", kindFree, true}}},
		{"list", nil},
	}
	for _, tc := range cases {
		got, _ := usePositionals(tc.use)
		if len(got) != len(tc.want) {
			t.Errorf("%q: got %+v, want %+v", tc.use, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q slot %d: got %+v, want %+v", tc.use, i, got[i], tc.want[i])
			}
		}
	}
}

func TestCheckArg(t *testing.T) {
	const ulid = "01KXRS3SHN1N35G4YETVADSN0R"
	cases := []struct {
		name, value string
		kind        argKind
		want        string // substring of the error, "" = accepted
	}{
		{"mailboxId", ulid, kindULID, ""},
		{"mailboxId", "alice", kindULID, "is not an id"},
		{"mailboxId", "alice@example.com", kindULID, "is an address"},
		{"mailboxId", strings.ToLower(ulid), kindULID, "did you mean " + ulid},
		{"patternId", "42", kindInt, ""},
		{"patternId", "forty", kindInt, "must be a number"},
		{"domain", "example.com", kindDomain, ""},
		{"domain", "xn--bcher-kva.example", kindDomain, ""},
		{"domain", "alice@example.com", kindDomain, "did you mean example.com"},
		{"domain", "example", kindDomain, "not a domain name"},
		{"address", "alice@example.com", kindAddress, ""},
		{"address", "alice", kindAddress, "not an email address"},
		{"address", ulid, kindAddress, "is an id"},
		{"mailboxId|address", ulid, kindULIDOrAddress, ""},
		{"mailboxId|address", "alice@example.com", kindULIDOrAddress, ""},
		{"mailboxId|address", "alice", kindULIDOrAddress, "neither an id"},
		{"name", "anything at all", kindFree, ""},
	}
	for _, tc := range cases {
		err := checkArg(tc.name, tc.value, tc.kind)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("<%s> %q: unexpected %v", tc.name, tc.value, err)
		case tc.want != "" && err == nil:
			t.Errorf("<%s> %q: accepted, want error containing %q", tc.name, tc.value, tc.want)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("<%s> %q: got %q, want it to contain %q", tc.name, tc.value, err, tc.want)
		}
		if err != nil {
			var ee *ExitError
			if !errors.As(err, &ee) || ee.Code != 2 {
				t.Errorf("<%s> %q: a shape refusal must be a usage error (exit 2), got %v", tc.name, tc.value, err)
			}
		}
	}
}

// The check runs from the root's pre-run hook, before config or network, so
// a wrong-shaped id is refused with exit 2 and the placeholder's name.
func TestArgKindsRefuseBeforeAnyRequest(t *testing.T) {
	root := newRootCmd(&app{})
	root.SetArgs([]string{"mailboxes", "get", "alice@example.com"})
	err := root.Execute()
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 2 {
		t.Fatalf("want usage error (exit 2), got %v", err)
	}
	if !strings.Contains(err.Error(), "<mailboxId>") || !strings.Contains(err.Error(), "is an address") {
		t.Errorf("message should name the slot and the mistake, got %q", err)
	}
}
