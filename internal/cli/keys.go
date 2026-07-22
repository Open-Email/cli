package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newKeysCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "keys",
		Aliases: []string{"key"},
		Short:   "Manage API keys",
	}
	cmd.AddCommand(newKeysCreateCmd(a), newKeysListCmd(a), newKeysRevokeCmd(a))
	return cmd
}

func newKeysCreateCmd(a *app) *cobra.Command {
	var role, account string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an API key (the token is shown exactly once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			created, err := client.CreateAPIKey(cmd.Context(), args[0], role, account)
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(created, nil)
				return nil
			}
			a.out.Successf("Created key %s (%s), role %s", created.Name, created.ID, created.Role)
			a.out.Warnf("This token is shown only once — store it now.")
			fmt.Fprintln(os.Stdout, created.Token) // token to stdout so it can be piped
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "system|account (system callers only; default account)")
	cmd.Flags().StringVar(&account, "account", "", "account id to own the key (system callers only)")
	return cmd
}

func newKeysListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List API keys (newest first)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			var items []coreapi.APIKey
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.APIKey], error) {
					return client.ListAPIKeys(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.APIKey]
				page, err = client.ListAPIKeys(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}

			a.out.Emit(map[string]any{"apiKeys": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, k := range items {
					status := "active"
					if k.RevokedAt != nil {
						status = "revoked"
					}
					rows = append(rows, []string{
						k.ID, k.Name, k.Role, strOr(k.AccountName, strOr(k.AccountID, "—")),
						fmtEpoch(k.CreatedAt), fmtEpochPtr(k.LastUsedAt), status,
					})
				}
				printTable(w, a.out, []string{"ID", "NAME", "ROLE", "ACCOUNT", "CREATED", "LAST USED", "STATUS"}, rows)
				if next != "" {
					a.out.Msgf("more results — pass --cursor %s (or --all)", next)
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newKeysRevokeCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <apiKeyId>",
		Aliases: []string{"rm", "delete"},
		Short:   "Revoke an API key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id := args[0]
			if !yes && !confirm(fmt.Sprintf("Revoke API key %s?", id)) {
				return usageError(fmt.Errorf("aborted (pass --yes to skip confirmation)"))
			}
			if err := client.RevokeAPIKey(cmd.Context(), id); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"revoked": true, "id": id}, func(w io.Writer) {
				a.out.Successf("Revoked API key %s", id)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
