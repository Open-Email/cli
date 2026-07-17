package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newMailboxesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mailboxes",
		Aliases: []string{"mailbox", "mb"},
		Short:   "Manage mailboxes",
	}
	cmd.AddCommand(
		newMailboxCreateCmd(a),
		newMailboxListCmd(a),
		newMailboxGetCmd(a),
		newMailboxUpdateCmd(a),
		newMailboxDeleteCmd(a),
		newMailboxRestoreCmd(a),
		newMailboxUseCmd(a),
	)
	return cmd
}

func newMailboxUseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "use <mailboxId|address>",
		Short: "Set the default mailbox for the current profile (used by message/label/sieve commands)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			// Resolve (validating an address; trusting a ULID) to the canonical id.
			id, err := a.resolveMailbox(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			a.profile.DefaultMailbox = id
			if err := a.saveProfile(); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"profile": a.profileName, "defaultMailbox": id}, func(w io.Writer) {
				a.out.Successf("Default mailbox for profile %q set to %s", a.profileName, id)
			})
			return nil
		},
	}
}

func newMailboxCreateCmd(a *app) *cobra.Command {
	var address, quota, account string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a mailbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			in := coreapi.MailboxCreateInput{}
			if cmd.Flags().Changed("address") {
				in.PrimaryAddress = &address
			}
			if cmd.Flags().Changed("quota") {
				v, isNull, perr := parseQuotaFlag(quota)
				if perr != nil {
					return usageError(perr)
				}
				if !isNull { // unlimited = omit (core defaults null to unlimited)
					in.QuotaBytes = v
				}
			}
			if cmd.Flags().Changed("account") {
				in.AccountID = &account
			}
			mb, err := client.CreateMailbox(cmd.Context(), in)
			if err != nil {
				return err
			}
			a.out.Emit(mb, func(w io.Writer) {
				a.out.Successf("Created mailbox %s", mb.ID)
				printMailbox(w, a.out, mb)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&address, "address", "", "primary address (must have a matching route to receive)")
	cmd.Flags().StringVar(&quota, "quota", "", "quota in bytes, or 'unlimited'")
	cmd.Flags().StringVar(&account, "account", "", "owning account id (system callers only)")
	return cmd
}

func newMailboxListCmd(a *app) *cobra.Command {
	var (
		deleted bool
		all     bool
		limit   int
		cursor  string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List mailboxes (or restorable tombstones with --deleted)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			if deleted {
				return a.listDeletedMailboxes(ctx, client, all, limit, cursor)
			}

			var items []coreapi.Mailbox
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Mailbox], error) {
					return client.ListMailboxes(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Mailbox]
				page, err = client.ListMailboxes(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"mailboxes": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, m := range items {
					rows = append(rows, []string{
						m.ID, strOr(m.PrimaryAddress, "—"), fmtQuota(m.QuotaBytes),
						strOr(m.AccountID, "—"), fmtEpoch(m.CreatedAt),
					})
				}
				printTable(w, a.out, []string{"ID", "ADDRESS", "QUOTA", "ACCOUNT", "CREATED"}, rows)
				if next != "" {
					a.out.Msgf("more results — pass --cursor %s (or --all)", next)
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleted, "deleted", false, "list restorable deleted mailboxes instead")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func (a *app) listDeletedMailboxes(ctx context.Context, client *coreapi.Client, all bool, limit int, cursor string) error {
	var items []coreapi.DeletedMailbox
	next := ""
	var err error
	if all {
		items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.DeletedMailbox], error) {
			return client.ListDeletedMailboxes(ctx, limit, cur)
		})
	} else {
		var page coreapi.Page[coreapi.DeletedMailbox]
		page, err = client.ListDeletedMailboxes(ctx, limit, cursor)
		items, next = page.Items, page.NextCursor
	}
	if err != nil {
		return err
	}
	a.out.Emit(map[string]any{"mailboxes": items, "nextCursor": next}, func(w io.Writer) {
		rows := make([][]string, 0, len(items))
		for _, m := range items {
			restorable := "no"
			if m.Restorable {
				restorable = "yes"
			}
			rows = append(rows, []string{
				m.ID, strOr(m.PrimaryAddress, "—"), fmtEpoch(m.DeletedAt),
				restorable, fmtEpochPtr(m.RestorableUntil),
			})
		}
		printTable(w, a.out, []string{"ID", "ADDRESS", "DELETED", "RESTORABLE", "UNTIL"}, rows)
		if next != "" {
			a.out.Msgf("more results — pass --cursor %s (or --all)", next)
		}
	})
	return nil
}

func newMailboxGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <mailboxId>",
		Short: "Show a mailbox with usage stats",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mb, err := client.GetMailbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(mb, func(w io.Writer) { printMailbox(w, a.out, mb) })
			return nil
		},
	}
}

func newMailboxUpdateCmd(a *app) *cobra.Command {
	var address, quota string
	cmd := &cobra.Command{
		Use:   "update <mailboxId>",
		Short: "Update a mailbox's quota or primary address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("address") {
				patch["primaryAddress"] = address
			}
			if cmd.Flags().Changed("quota") {
				v, isNull, perr := parseQuotaFlag(quota)
				if perr != nil {
					return usageError(perr)
				}
				if isNull {
					patch["quotaBytes"] = nil // explicit null = unlimited
				} else {
					patch["quotaBytes"] = *v
				}
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass --address and/or --quota"))
			}
			mb, err := client.UpdateMailbox(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(mb, func(w io.Writer) {
				a.out.Successf("Updated mailbox %s", mb.ID)
				printMailbox(w, a.out, mb)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&address, "address", "", "new primary address")
	cmd.Flags().StringVar(&quota, "quota", "", "new quota in bytes, or 'unlimited'")
	return cmd
}

func newMailboxDeleteCmd(a *app) *cobra.Command {
	var purge, yes bool
	cmd := &cobra.Command{
		Use:     "delete <mailboxId>",
		Aliases: []string{"rm"},
		Short:   "Delete a mailbox (soft, 7-day undo; --purge to waive it)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id := args[0]
			if purge {
				if !yes && !confirmTyped(
					fmt.Sprintf("PURGE mailbox %s — this waives the 7-day undo entirely and is irreversible.", id), id) {
					return usageError(errors.New("aborted (type the mailbox id to confirm, or pass --yes)"))
				}
			} else {
				if !yes && !confirm(fmt.Sprintf("Delete mailbox %s? (undoable for 7 days)", id)) {
					return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
				}
			}
			res, err := client.DeleteMailbox(cmd.Context(), id, purge)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				if res.Purged {
					a.out.Successf("Purged mailbox %s (not restorable)", id)
					return
				}
				a.out.Successf("Deleted mailbox %s", id)
				if res.RestorableUntil != nil {
					a.out.Msgf("  restorable until %s — undo with `openemail mailboxes restore %s`", fmtEpoch(*res.RestorableUntil), id)
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "permanently purge: waive the 7-day undo window (irreversible)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newMailboxRestoreCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <mailboxId>",
		Short: "Undo a mailbox deletion within the 7-day window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.RestoreMailbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Restored mailbox %s", res.ID)
				if res.PrimaryAddress != nil && *res.PrimaryAddress != "" {
					if res.AddressChanged {
						a.out.Warnf("original address was taken — bound as %s instead", *res.PrimaryAddress)
					} else {
						a.out.Msgf("  address: %s", *res.PrimaryAddress)
					}
				} else {
					a.out.Warnf("restored without an address — re-bind a route explicitly")
				}
			})
			return nil
		},
	}
}

// parseQuotaFlag parses a --quota value: "unlimited"/"none" → (nil, true), a
// positive integer → (&n, false), anything else → error.
func parseQuotaFlag(s string) (*int64, bool, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "unlimited" || t == "none" || t == "" {
		return nil, true, nil
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return nil, false, fmt.Errorf("invalid quota %q: expected a positive byte count or 'unlimited'", s)
	}
	return &n, false, nil
}

func printMailbox(w io.Writer, p *Printer, m *coreapi.Mailbox) {
	rows := [][]string{
		{"ID", m.ID},
		{"Address", strOr(m.PrimaryAddress, "—")},
		{"Quota", fmtQuota(m.QuotaBytes)},
		{"Account", strOr(m.AccountID, "—")},
		{"Created", fmtEpoch(m.CreatedAt)},
	}
	if m.MessageCount != nil {
		rows = append(rows, []string{"Messages", int64Or(m.MessageCount, "0")})
	}
	if m.UsedBytes != nil {
		rows = append(rows, []string{"Used", fmtBytes(*m.UsedBytes)})
	}
	if m.ExpungedCount != nil {
		rows = append(rows, []string{"In trash", fmt.Sprintf("%s (%s)", int64Or(m.ExpungedCount, "0"), fmtBytes(deref(m.ExpungedBytes)))})
	}
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
