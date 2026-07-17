package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newSieveCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sieve",
		Short: "Manage a mailbox's Sieve filter scripts (ManageSieve)",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newSieveScriptsCmd(a),
		newSieveActiveCmd(a),
		newSieveActivateCmd(a),
		newSieveDeactivateCmd(a),
		newSieveCheckCmd(a),
		newSieveCapabilitiesCmd(a),
	)
	return cmd
}

func newSieveScriptsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scripts",
		Short: "List, read, upload, rename, and delete scripts",
	}
	cmd.AddCommand(
		newSieveScriptsListCmd(a),
		newSieveScriptGetCmd(a),
		newSieveScriptPutCmd(a),
		newSieveScriptDeleteCmd(a),
		newSieveScriptRenameCmd(a),
	)
	return cmd
}

func newSieveScriptsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List scripts and which one is active",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			scripts, err := client.ListSieveScripts(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"scripts": scripts}, func(w io.Writer) {
				rows := make([][]string, 0, len(scripts))
				for _, s := range scripts {
					rows = append(rows, []string{s.Name, boolYN(s.Active), fmtEpoch(s.UpdatedAt)})
				}
				printTable(w, a.out, []string{"NAME", "ACTIVE", "UPDATED"}, rows)
			})
			return nil
		},
	}
}

func newSieveScriptGetCmd(a *app) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Print a script's source (to stdout or a file)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			s, err := client.GetSieveScript(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(s, nil)
				return nil
			}
			if output != "" {
				if werr := os.WriteFile(output, []byte(s.Script), 0o644); werr != nil {
					return werr
				}
				a.out.Successf("Wrote %s (active=%v)", output, s.Active)
				return nil
			}
			fmt.Fprint(os.Stdout, s.Script)
			if !strings.HasSuffix(s.Script, "\n") {
				fmt.Fprintln(os.Stdout)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the script body to this file")
	return cmd
}

func newSieveScriptPutCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "put <name>",
		Short: "Upload or replace a script from a file or stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			script, err := readScriptSource(file)
			if err != nil {
				return err
			}
			res, err := client.PutSieveScript(cmd.Context(), mbx, args[0], script)
			if err != nil {
				return err // 422 invalid_script renders message + line/col via printError
			}
			a.out.Emit(res, func(w io.Writer) {
				verb := "Replaced"
				if res.Created {
					verb = "Created"
				}
				a.out.Successf("%s script %q", verb, res.Name)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "script file to upload (default: stdin)")
	return cmd
}

func newSieveScriptDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a script",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete Sieve script %q?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.DeleteSieveScript(cmd.Context(), mbx, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "name": args[0]}, func(w io.Writer) {
				a.out.Successf("Deleted script %q", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newSieveScriptRenameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name> <newName>",
		Short: "Rename a script (preserves its active flag)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.RenameSieveScript(cmd.Context(), mbx, args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"renamed": true, "from": args[0], "to": args[1]}, func(w io.Writer) {
				a.out.Successf("Renamed script %q → %q", args[0], args[1])
			})
			return nil
		},
	}
}

func newSieveActiveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "active",
		Short: "Show which script is the active filter",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			name, err := client.SieveActiveName(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"name": nilIfEmpty(name)}, func(w io.Writer) {
				if name == "" {
					a.out.Msgf("no active filter")
					return
				}
				a.out.Msgf("active filter: %s", name)
			})
			return nil
		},
	}
}

func newSieveActivateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "activate <name>",
		Short: "Make a script the active filter",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.ActivateSieve(cmd.Context(), mbx, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"name": args[0], "active": true}, func(w io.Writer) {
				a.out.Successf("Activated script %q", args[0])
			})
			return nil
		},
	}
}

func newSieveDeactivateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "deactivate",
		Short: "Deactivate the active filter (delivery runs unfiltered)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.DeactivateSieve(cmd.Context(), mbx); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"active": false}, func(w io.Writer) {
				a.out.Successf("Deactivated the active filter")
			})
			return nil
		},
	}
}

func newSieveCheckCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Dry-run compile a script from a file or stdin (exits 1 if it does not compile)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			script, err := readScriptSource(file)
			if err != nil {
				return err
			}
			res, err := client.CheckSieve(cmd.Context(), mbx, script)
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(res, nil)
				if !res.Valid {
					return silentExit(1)
				}
				return nil
			}
			if res.Valid {
				a.out.Successf("Script compiles cleanly")
				return nil
			}
			if res.Line > 0 {
				a.out.Warnf("does not compile: %s (line %d, column %d)", res.Message, res.Line, res.Col)
			} else {
				a.out.Warnf("does not compile: %s", res.Message)
			}
			return silentExit(1)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "script file to check (default: stdin)")
	return cmd
}

func newSieveCapabilitiesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "capabilities",
		Aliases: []string{"caps"},
		Short:   "Show the interpreter's supported extensions and limits",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			caps, err := client.SieveCaps(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(caps, func(w io.Writer) {
				rows := [][]string{
					{"Implementation", caps.Implementation},
					{"Max scripts", fmt.Sprintf("%d", caps.MaxScripts)},
					{"Max script size", fmtBytes(caps.MaxScriptSize)},
					{"Extensions", strings.Join(caps.Sieve, ", ")},
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
			})
			return nil
		},
	}
}

// readScriptSource reads a script body from a file or stdin.
func readScriptSource(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
