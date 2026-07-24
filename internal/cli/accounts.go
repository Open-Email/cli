package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newAccountsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "Manage tenant accounts (system callers only for create/list)",
	}
	cmd.AddCommand(newAccountCreateCmd(a), newAccountListCmd(a), newAccountGetCmd(a))
	return cmd
}

func newAccountCreateCmd(a *app) *cobra.Command {
	var maxMailboxes int
	var withKey bool
	var keyName string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant account (system callers only)",
		Long: "Create a tenant account. A fresh account has no credentials — pass --with-key\n" +
			"to also mint its first account API key (the token prints ONCE).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			var max *int64
			if cmd.Flags().Changed("max-mailboxes") {
				v := int64(maxMailboxes)
				max = &v
			}
			acc, err := client.CreateAccount(cmd.Context(), args[0], max)
			if err != nil {
				return err
			}
			if !withKey {
				a.out.Emit(acc, func(w io.Writer) {
					a.out.Successf("Created account %s", acc.ID)
					printAccount(w, a.out, acc)
				})
				return nil
			}
			key, kerr := client.CreateAPIKey(cmd.Context(), keyName, coreapi.PrincipalAccount, acc.ID)
			if kerr != nil {
				// The account exists; report the partial state loudly rather
				// than pretending the whole call failed.
				a.out.Emit(acc, func(w io.Writer) {
					a.out.Successf("Created account %s", acc.ID)
					printAccount(w, a.out, acc)
				})
				return fmt.Errorf("account created, but the key mint failed: %w — retry with `openemail keys create %s --account %s`", kerr, keyName, acc.ID)
			}
			out := struct {
				Account *coreapi.Account       `json:"account"`
				Key     *coreapi.CreatedAPIKey `json:"key"`
			}{acc, key}
			a.out.Emit(out, func(w io.Writer) {
				a.out.Successf("Created account %s", acc.ID)
				printAccount(w, a.out, acc)
				a.out.Successf("Created key %q (role %s)", key.Name, key.Role)
				a.out.Msgf("  token (shown once): %s", key.Token)
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&maxMailboxes, "max-mailboxes", 0, "cap on mailboxes (omit = unlimited)")
	cmd.Flags().BoolVar(&withKey, "with-key", false, "also mint the account's first API key and print its token")
	cmd.Flags().StringVar(&keyName, "key-name", "bootstrap", "name for the minted key (with --with-key)")
	return cmd
}

func newAccountListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accounts (system callers only)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Account
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Account], error) {
					return client.ListAccounts(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Account]
				page, err = client.ListAccounts(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"accounts": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, ac := range items {
					rows = append(rows, []string{ac.ID, ac.Name, int64Or(ac.MaxMailboxes, "unlimited"), fmtEpoch(ac.CreatedAt)})
				}
				printTable(w, a.out, []string{"ID", "NAME", "MAX MAILBOXES", "CREATED"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	return cmd
}

func newAccountGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <accountId>",
		Short: "Show an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			acc, err := client.GetAccount(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(acc, func(w io.Writer) { printAccount(w, a.out, acc) })
			return nil
		},
	}
}

func printAccount(w io.Writer, p *Printer, acc *coreapi.Account) {
	printTable(w, p, []string{"FIELD", "VALUE"}, [][]string{
		{"ID", acc.ID},
		{"Name", acc.Name},
		{"Max mailboxes", int64Or(acc.MaxMailboxes, "unlimited")},
		{"Created", fmtEpoch(acc.CreatedAt)},
	})
}
