package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Cobra refuses an unknown subcommand only at the root; under a group it used
// to print the group's help and exit 0. A wrong word anywhere in the command
// path must be a usage error with suggestions, and a bare group must still be
// its own help.
func TestUnknownSubcommandIsAUsageError(t *testing.T) {
	cases := []struct {
		args    []string
		wantMsg []string
	}{
		{[]string{"labels", "get", "01KXRS3SHN1N35G4YETVADSN0R"}, []string{`unknown command "get" for "openemail labels"`, "openemail labels --help"}},
		{[]string{"mailboxes", "lst"}, []string{`unknown command "lst" for "openemail mailboxes"`, "Did you mean this?", "list"}},
		{[]string{"nonsense"}, []string{`unknown command "nonsense" for "openemail"`}},
	}
	for _, tc := range cases {
		root := newRootCmd(&app{})
		root.SetArgs(tc.args)
		err := root.Execute()
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Code != 2 {
			t.Errorf("%v: want usage error (exit 2), got %v", tc.args, err)
			continue
		}
		for _, w := range tc.wantMsg {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: message %q lacks %q", tc.args, err, w)
			}
		}
	}
}

func TestBareGroupPrintsHelp(t *testing.T) {
	for _, args := range [][]string{{"labels"}, {}} {
		root := newRootCmd(&app{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "Available Commands:") {
			t.Errorf("%v: expected help, got %q", args, out.String())
		}
	}
}
