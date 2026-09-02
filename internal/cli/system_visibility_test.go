package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func findSubcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func writeTestConfigWithRole(t *testing.T, role string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "openemail")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "default_profile = \"default\"\n[profiles.default]\nrole = \"" + role + "\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestSystemVisibilityGating(t *testing.T) {
	t.Run("account profile hides operator commands and flags", func(t *testing.T) {
		writeTestConfigWithRole(t, "account")

		a := &app{}
		root := newRootCmd(a)

		// Admin command should be hidden.
		if a.adminCmd == nil || !a.adminCmd.Hidden {
			t.Errorf("adminCmd should be hidden for account role")
		}

		// Accounts subcommands create, list, update, restore should be hidden.
		accCmd := findSubcommand(root, "accounts")
		if accCmd == nil {
			t.Fatal("accounts command not found")
		}
		for _, name := range []string{"create", "list", "update", "restore"} {
			sub := findSubcommand(accCmd, name)
			if sub == nil {
				t.Errorf("accounts %s subcommand not found", name)
				continue
			}
			if !sub.Hidden {
				t.Errorf("accounts %s should be hidden for account role", name)
			}
		}

		// Accounts delete --purge flag should be hidden.
		delCmd := findSubcommand(accCmd, "delete")
		if delCmd == nil {
			t.Fatal("accounts delete command not found")
		}
		if flag := delCmd.Flags().Lookup("purge"); flag == nil || !flag.Hidden {
			t.Errorf("accounts delete --purge should be hidden for account role")
		}

		// Domains create --platform, --send-verified, --account should be hidden.
		domCmd := findSubcommand(root, "domains")
		if domCmd == nil {
			t.Fatal("domains command not found")
		}
		domCreate := findSubcommand(domCmd, "create")
		if domCreate == nil {
			t.Fatal("domains create command not found")
		}
		for _, f := range []string{"platform", "send-verified", "account"} {
			flag := domCreate.Flags().Lookup(f)
			if flag == nil || !flag.Hidden {
				t.Errorf("domains create --%s should be hidden for account role", f)
			}
		}

		// Keys create --role, --account should be hidden.
		keysCmd := findSubcommand(root, "keys")
		if keysCmd == nil {
			t.Fatal("keys command not found")
		}
		keysCreate := findSubcommand(keysCmd, "create")
		if keysCreate == nil {
			t.Fatal("keys create command not found")
		}
		for _, f := range []string{"role", "account"} {
			flag := keysCreate.Flags().Lookup(f)
			if flag == nil || !flag.Hidden {
				t.Errorf("keys create --%s should be hidden for account role", f)
			}
		}

		// Do-not-send --account should be hidden.
		dnsCmd := findSubcommand(root, "do-not-send")
		if dnsCmd == nil {
			t.Fatal("do-not-send command not found")
		}
		dnsList := findSubcommand(dnsCmd, "list")
		if dnsList != nil {
			if flag := dnsList.Flags().Lookup("account"); flag == nil || !flag.Hidden {
				t.Errorf("do-not-send list --account should be hidden for account role")
			}
		}
	})

	t.Run("system profile keeps operator commands and flags visible", func(t *testing.T) {
		writeTestConfigWithRole(t, "system")

		a := &app{}
		root := newRootCmd(a)

		if a.adminCmd == nil || a.adminCmd.Hidden {
			t.Errorf("adminCmd should NOT be hidden for system role")
		}

		accCmd := findSubcommand(root, "accounts")
		if accCmd == nil {
			t.Fatal("accounts command not found")
		}
		for _, name := range []string{"create", "list", "update", "restore"} {
			sub := findSubcommand(accCmd, name)
			if sub == nil {
				t.Errorf("accounts %s subcommand not found", name)
				continue
			}
			if sub.Hidden {
				t.Errorf("accounts %s should NOT be hidden for system role", name)
			}
		}

		delCmd := findSubcommand(accCmd, "delete")
		if delCmd == nil {
			t.Fatal("accounts delete command not found")
		}
		if flag := delCmd.Flags().Lookup("purge"); flag == nil || flag.Hidden {
			t.Errorf("accounts delete --purge should NOT be hidden for system role")
		}

		domCmd := findSubcommand(root, "domains")
		if domCmd == nil {
			t.Fatal("domains command not found")
		}
		domCreate := findSubcommand(domCmd, "create")
		if domCreate == nil {
			t.Fatal("domains create command not found")
		}
		for _, f := range []string{"platform", "send-verified", "account"} {
			flag := domCreate.Flags().Lookup(f)
			if flag == nil || flag.Hidden {
				t.Errorf("domains create --%s should NOT be hidden for system role", f)
			}
		}

		keysCmd := findSubcommand(root, "keys")
		if keysCmd == nil {
			t.Fatal("keys command not found")
		}
		keysCreate := findSubcommand(keysCmd, "create")
		if keysCreate == nil {
			t.Fatal("keys create command not found")
		}
		for _, f := range []string{"role", "account"} {
			flag := keysCreate.Flags().Lookup(f)
			if flag == nil || flag.Hidden {
				t.Errorf("keys create --%s should NOT be hidden for system role", f)
			}
		}
	})
}

func TestFlagErgonomics(t *testing.T) {
	a := &app{}
	root := newRootCmd(a)

	// Verify vacation set has --body-file registered as alias for --text-file
	vacCmd := findSubcommand(root, "vacation")
	if vacCmd == nil {
		t.Fatal("vacation command not found")
	}
	vacSet := findSubcommand(vacCmd, "set")
	if vacSet == nil {
		t.Fatal("vacation set command not found")
	}
	if f := vacSet.Flags().Lookup("body-file"); f == nil {
		t.Errorf("vacation set should have --body-file registered")
	}

	// Verify lists check has -m / --mailbox
	listsCmd := findSubcommand(root, "lists")
	if listsCmd == nil {
		t.Fatal("lists command not found")
	}
	listsCheck := findSubcommand(listsCmd, "check")
	if listsCheck == nil {
		t.Fatal("lists check command not found")
	}
	if f := listsCheck.Flags().Lookup("mailbox"); f == nil {
		t.Errorf("lists check should have --mailbox registered")
	}
	if f := listsCheck.Flags().ShorthandLookup("m"); f == nil {
		t.Errorf("lists check should have -m shorthand registered")
	}
}
