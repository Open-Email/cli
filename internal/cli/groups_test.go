package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	root := newRootCmd(&app{})
	var subOut bytes.Buffer
	root.SetOut(&subOut)
	root.SetArgs([]string{"labels"})
	if err := root.Execute(); err != nil {
		t.Fatalf("labels: %v", err)
	}
	if !strings.Contains(subOut.String(), "Available Commands:") {
		t.Errorf("labels: expected Available Commands:, got %q", subOut.String())
	}

	root = newRootCmd(&app{})
	var rootOut bytes.Buffer
	root.SetOut(&rootOut)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("root: %v", err)
	}
	for _, wantGroup := range []string{
		"Authentication & Session:",
		"Mail, Threads & Sending:",
		"Filtering, Forwarding & Lists:",
		"Calendars, Contacts & Preferences:",
		"Directory & Tenant Management:",
		"Console, Live Stream & Operator Tools:",
	} {
		if !strings.Contains(rootOut.String(), wantGroup) {
			t.Errorf("root help missing group %q in:\n%s", wantGroup, rootOut.String())
		}
	}
}

func TestRootCommandsHaveGroups(t *testing.T) {
	root := newRootCmd(&app{})
	groups := map[string]bool{}
	for _, g := range root.Groups() {
		groups[g.ID] = true
	}
	if len(groups) == 0 {
		t.Fatal("expected root command to define command groups")
	}
	for _, sub := range root.Commands() {
		if sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		if sub.GroupID == "" {
			t.Errorf("root subcommand %q has no GroupID", sub.Name())
		} else if !groups[sub.GroupID] {
			t.Errorf("root subcommand %q has unknown GroupID %q", sub.Name(), sub.GroupID)
		}
	}
}

func TestNoFlagUsageContainsBackticks(t *testing.T) {
	// pflag.UnquoteUsage treats `backticked` words inside Flag.Usage as the
	// flag's placeholder type name, mangling help output whenever prose uses
	// backticks.
	root := newRootCmd(&app{})
	var walk func(cmdPath string, c interface {
		CommandPath() string
	})
	_ = walk
	var checkCmd func(c *cobra.Command)
	checkCmd = func(c *cobra.Command) {
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s --%s: flag usage contains backticks (mangles pflag.UnquoteUsage): %q", c.CommandPath(), f.Name, f.Usage)
			}
		})
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s --%s: persistent flag usage contains backticks: %q", c.CommandPath(), f.Name, f.Usage)
			}
		})
		for _, sub := range c.Commands() {
			checkCmd(sub)
		}
	}
	checkCmd(root)
}
