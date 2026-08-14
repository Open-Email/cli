package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newSuppressionsCmd is the TENANT-facing do-not-send surface: the account's
// own suppression list (core migration 0033). Policy, not evidence — "my
// account must never mail this address" — with full CRUD on an ordinary
// account key. The deployment-global evidence list stays under
// `admin suppressions`; the one global verb an account key holds is the
// hard-bounce lift, which `remove` folds in (see newAccountSuppressionRemoveCmd).
func newSuppressionsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "suppressions",
		Aliases: []string{"suppression", "do-not-send"},
		Short:   "Your account's do-not-send list",
		Long: "Addresses this account will never mail, from any of its domains and\n" +
			"mailboxes — an unsubscribe, a legal request, an import from a previous\n" +
			"provider. Enforced at the last hop before mail leaves the platform, so a\n" +
			"listed recipient is accepted at submission and then refused terminally\n" +
			"(the delivery log shows `relay_suppressed`).\n\n" +
			"This list binds mail the account SUBMITS (API sends, SMTP/JMAP submission,\n" +
			"auto-replies, calendar invitations). It does not judge forwarding rules or\n" +
			"external group members; the platform's own bounce/complaint list covers\n" +
			"those legs too. That platform list is separate and operator-managed\n" +
			"(`admin suppressions`); its refusals appear per message in `domains events`.",
	}
	cmd.AddCommand(
		newAccountSuppressionListCmd(a),
		newAccountSuppressionCheckCmd(a),
		newAccountSuppressionAddCmd(a),
		newAccountSuppressionRemoveCmd(a),
	)
	return cmd
}

// suppressionScope resolves which account's list to act on, and the caller's
// principal type. Account keys default to their own account; a system key has
// none, so it must name the tenant with --account.
func suppressionScope(ctx context.Context, a *app, client *coreapi.Client, flagAccount string) (accountID, role string, err error) {
	if a.tokenSource == "profile" && a.profile.Role != "" {
		role = a.profile.Role
		accountID = a.profile.AccountID
	} else {
		id, rerr := client.Resolve(ctx)
		if rerr != nil {
			return "", "", rerr
		}
		role, accountID = id.Type, id.AccountID
	}
	if flagAccount != "" {
		accountID = flagAccount
	}
	if accountID == "" {
		return "", "", usageError(errors.New("this key has no account of its own — pass --account <id> to name the tenant"))
	}
	return accountID, role, nil
}

func accountFlag(cmd *cobra.Command, dest *string) {
	cmd.Flags().StringVar(dest, "account", "", "account id to act on (defaults to the key's own account; required for system keys)")
}

func newAccountSuppressionListCmd(a *app) *cobra.Command {
	var (
		account string
		all     bool
		limit   int
		cursor  string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the addresses this account will not mail",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			acct, _, err := suppressionScope(ctx, a, client, account)
			if err != nil {
				return err
			}
			var items []coreapi.AccountSuppression
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AccountSuppression], error) {
					return client.ListAccountSuppressions(ctx, acct, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.AccountSuppression]
				page, err = client.ListAccountSuppressions(ctx, acct, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"suppressions": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, s := range items {
					rows = append(rows, []string{
						s.Address, s.Reason, fmtEpoch(s.CreatedAt), truncate(strOr(s.Note, "—"), 40),
					})
				}
				printTable(w, a.out, []string{"ADDRESS", "REASON", "ADDED", "NOTE"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newAccountSuppressionCheckCmd(a *app) *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:     "check <address>",
		Aliases: []string{"get"},
		Short:   "Check whether an address is on this account's list",
		Long: "Answers for THIS ACCOUNT's list only. An address can still be refused by the\n" +
			"platform's own bounce/complaint list, which an account key cannot read —\n" +
			"those refusals show per message in `domains events` as `relay_suppressed`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			acct, _, err := suppressionScope(ctx, a, client, account)
			if err != nil {
				return err
			}
			s, err := client.GetAccountSuppression(ctx, acct, args[0])
			if coreapi.IsNotFound(err) {
				a.out.Emit(map[string]any{"address": args[0], "suppressed": false}, func(w io.Writer) {
					a.out.Successf("%s is not on this account's do-not-send list", args[0])
				})
				return nil
			}
			if err != nil {
				return err
			}
			a.out.Emit(s, func(w io.Writer) {
				a.out.Warnf("%s is on the do-not-send list (%s)", s.Address, s.Reason)
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Address", s.Address},
					{"Reason", s.Reason},
					{"Added", fmtEpoch(s.CreatedAt)},
					{"Note", strOr(s.Note, "—")},
				})
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	return cmd
}

func newAccountSuppressionAddCmd(a *app) *cobra.Command {
	var (
		account string
		reason  string
		note    string
	)
	cmd := &cobra.Command{
		Use:     "add <address>",
		Aliases: []string{"create", "block"},
		Short:   "Stop this account mailing an address",
		Long: "Use it when somebody asks to stop hearing from you. Scoped to this account —\n" +
			"other tenants are unaffected — so unlike the platform list there is nothing\n" +
			"here that needs an operator.\n\n" +
			"Repeat calls converge on the same row (reason and note refresh; the original\n" +
			"added-at stays).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason != "" && reason != "manual" && reason != "complaint" && reason != "hard_bounce" {
				return usageError(fmt.Errorf("--reason must be manual, complaint or hard_bounce, got %q", reason))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			acct, _, err := suppressionScope(ctx, a, client, account)
			if err != nil {
				return err
			}
			s, err := client.AddAccountSuppression(ctx, acct, coreapi.AccountSuppressionCreate{
				Address: args[0], Reason: reason, Note: note,
			})
			if err != nil {
				return err
			}
			a.out.Emit(s, func(w io.Writer) {
				a.out.Successf("%s is on the do-not-send list — this account will not mail it", s.Address)
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	cmd.Flags().StringVar(&reason, "reason", "", "manual (default), or complaint/hard_bounce when importing a list that recorded why")
	cmd.Flags().StringVar(&note, "note", "", "why — stored on the row so `check` can answer it later")
	return cmd
}

func newAccountSuppressionRemoveCmd(a *app) *cobra.Command {
	var (
		account string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:     "remove <address>",
		Aliases: []string{"rm", "delete", "allow"},
		Short:   "Let mail flow to an address again, as far as this account can",
		Long: "Removes the address from this account's own list AND, if the platform had it\n" +
			"suppressed over a hard bounce, lifts that too (the self-healing case: if the\n" +
			"address still does not exist, the next bounce re-suppresses it). An address\n" +
			"the platform suppressed over a spam COMPLAINT stays refused — only an\n" +
			"operator can lift that, because re-mailing a complainant spends every\n" +
			"tenant's shared sending reputation.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			acct, role, err := suppressionScope(ctx, a, client, account)
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Allow mail from this account to %s again?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			ownRemoved := false
			if _, err := client.RemoveAccountSuppression(ctx, acct, args[0]); err == nil {
				ownRemoved = true
			} else if !coreapi.IsNotFound(err) {
				return err
			}
			// The global hard-bounce lift — but only on an account key. A system
			// key's DELETE lifts ANY global row, complaints included, and that
			// decision belongs to the explicit `admin suppressions lift`, not to a
			// tenant-scoped remove that happens to be run by an operator.
			bounceLifted := false
			if role == coreapi.PrincipalAccount {
				if _, err := client.LiftSuppression(ctx, args[0]); err == nil {
					bounceLifted = true
				} else if !coreapi.IsNotFound(err) {
					return err
				}
			}
			if !ownRemoved && !bounceLifted {
				return fmt.Errorf("%s was not suppressed by anything this account can lift", args[0])
			}
			a.out.Emit(map[string]any{
				"address": args[0], "removed": ownRemoved, "bounceLifted": bounceLifted,
			}, func(w io.Writer) {
				switch {
				case ownRemoved && bounceLifted:
					a.out.Successf("%s removed from the account list, and the platform's bounce block lifted", args[0])
				case ownRemoved:
					a.out.Successf("%s removed from the account's do-not-send list", args[0])
				default:
					a.out.Successf("The platform's bounce block on %s is lifted", args[0])
				}
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
