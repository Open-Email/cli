package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newSuppressionsCmd is the TENANT-facing do-not-send surface — a shortcut for
// ONE address list: the account-scoped, outbound, `block` list (core migration
// 0040, which replaced the per-account suppression table of 0033 and copied
// every row into a list named exactly this).
//
// It exists beside `openemail lists` because "stop mailing this person" is the
// one combination almost everybody wants and nobody should have to spell out
// three axes to reach. Everything else — inbound blocks, spam-filter
// exemptions, per-domain and per-mailbox scopes — lives there.
//
// The deployment-global evidence list stays under `admin suppressions`; the one
// global verb an account key holds is the hard-bounce lift, which `remove`
// folds in (see newAccountSuppressionRemoveCmd).
func newSuppressionsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "do-not-send",
		Aliases: []string{"suppressions", "suppression", "dns-list"},
		Short:   "Addresses this account will never mail",
		Long: "Addresses this account will never mail, from any of its domains and\n" +
			"mailboxes — an unsubscribe, a legal request, an import from a previous\n" +
			"provider. Enforced at the last hop before mail leaves the platform, so a\n" +
			"listed recipient is accepted at submission and then refused terminally\n" +
			"(the delivery log shows `relay_suppressed`).\n\n" +
			"This is one ADDRESS LIST — account scope, outbound, block — kept as its own\n" +
			"command because it is the combination people want most. `openemail lists`\n" +
			"has the rest: refusing INCOMING senders, exempting a sender from the spam\n" +
			"filter, and narrowing any of it to one domain or one mailbox.\n\n" +
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

// DoNotSendListName is the name core's migration gives the seeded list, and the
// name this command creates one under when an account has none yet. It is a
// contract between the two: pick a different string here and a tenant whose
// rows core already migrated would get a SECOND, empty list.
const DoNotSendListName = "Do not send"

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

// doNotSendList finds the account's seeded do-not-send list. `create` decides
// what happens when there is none: a READ answers "nothing is suppressed"
// rather than minting a list nobody asked for, while a WRITE creates it.
//
// Matched on (scope, direction, verdict, name) rather than on name alone, so a
// tenant who happens to have named some other list "Do not send" through
// `openemail lists` cannot have this command write into it.
func doNotSendList(ctx context.Context, client *coreapi.Client, accountID string, create bool) (coreapi.AddressLists, *coreapi.AddressList, error) {
	fam := coreapi.AccountLists(accountID)
	page, err := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
		return client.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
			ScopeKind: "account", ScopeID: accountID, Direction: "outbound", Verdict: "block",
		}, 0, cur)
	})
	if err != nil {
		return fam, nil, err
	}
	for i := range page {
		if page[i].Name == DoNotSendListName {
			return fam, &page[i], nil
		}
	}
	if !create {
		return fam, nil, nil
	}
	l, err := client.CreateAddressList(ctx, fam, coreapi.AddressListCreate{
		Name: DoNotSendListName, ScopeKind: "account", ScopeID: accountID,
		Direction: "outbound", Verdict: "block",
	})
	if err != nil {
		// A 409 means the name is already taken at this scope — either a
		// concurrent `add` won the create race (in which case the list now
		// exists and we adopt it), or the tenant made a DIFFERENT list named
		// "Do not send" through `openemail lists` (in which case we must not
		// write outbound-block entries into an inbound/allow list — say so
		// plainly instead of looping on a create that can never land). Core's
		// uniqueness is (scope, name) regardless of direction/verdict, so one
		// re-read by name at this scope tells the two apart.
		if coreapi.Status(err) == 409 {
			all, e2 := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
				return client.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
					ScopeKind: "account", ScopeID: accountID,
				}, 0, cur)
			})
			if e2 != nil {
				return fam, nil, err
			}
			for i := range all {
				if all[i].Name != DoNotSendListName {
					continue
				}
				if all[i].Direction == "outbound" && all[i].Verdict == "block" {
					return fam, &all[i], nil // the race resolved; adopt the winner
				}
				return fam, nil, fmt.Errorf(
					"a list named %q already exists on this account for a different purpose (%s/%s); rename it or manage it with `openemail lists`",
					DoNotSendListName, all[i].Direction, all[i].Verdict)
			}
		}
		return fam, nil, err
	}
	return fam, l, nil
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
			// A read never creates the list: an account that has suppressed
			// nobody has no list, and that is the honest empty answer.
			fam, l, err := doNotSendList(ctx, client, acct, false)
			if err != nil {
				return err
			}
			var items []coreapi.AddressListEntry
			next := ""
			if l != nil {
				if all {
					items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressListEntry], error) {
						return client.ListAddressListEntries(ctx, fam, l.ID, limit, cur)
					})
				} else {
					var page coreapi.Page[coreapi.AddressListEntry]
					page, err = client.ListAddressListEntries(ctx, fam, l.ID, limit, cursor)
					items, next = page.Items, page.NextCursor
				}
				if err != nil {
					return err
				}
			}
			a.out.Emit(map[string]any{"suppressions": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, e := range items {
					rows = append(rows, []string{
						e.Pattern, fmtEpoch(e.CreatedAt), truncate(strOr(e.Note, "—"), 50),
					})
				}
				printTable(w, a.out, []string{"ADDRESS", "ADDED", "NOTE"}, rows)
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
		Short:   "Check whether this account would refuse to mail an address",
		Long: "Runs the SAME evaluator the relay runs, so it answers for EVERY outbound\n" +
			"list this account has — not only the do-not-send one — and names the\n" +
			"pattern that decided.\n\n" +
			"An address can still be refused by the platform's own bounce/complaint\n" +
			"list, which an account key cannot read; those refusals show per message in\n" +
			"`domains events` as `relay_suppressed`.",
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
			v, err := client.EvaluateAddressLists(ctx, coreapi.AccountLists(acct), coreapi.AddressListEvaluateInput{
				Direction: "outbound", Address: args[0],
			})
			if err != nil {
				return err
			}
			a.out.Emit(v, func(w io.Writer) {
				switch v.Verdict {
				case "block":
					a.out.Warnf("%s is on the do-not-send list (pattern %s)", args[0], v.Pattern)
				case "allow":
					a.out.Successf("%s is explicitly allowed past any broader block (pattern %s)", args[0], v.Pattern)
				default:
					a.out.Successf("%s is not on this account's do-not-send list", args[0])
				}
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
		note    string
	)
	cmd := &cobra.Command{
		Use:     "add <address>",
		Aliases: []string{"create", "block"},
		Short:   "Stop this account mailing an address",
		Long: "Use it when somebody asks to stop hearing from you. Scoped to this account —\n" +
			"other tenants are unaffected — so unlike the platform list there is nothing\n" +
			"here that needs an operator.\n\n" +
			"Takes a whole domain too (`@acme.example`, or `@.acme.example` for its\n" +
			"subdomains as well). Repeat calls converge on the same entry: the note\n" +
			"refreshes, the original added-at stays.",
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
			// A write creates the list when the account has none — an account
			// core never migrated rows for starts here.
			fam, l, err := doNotSendList(ctx, client, acct, true)
			if err != nil {
				return err
			}
			e, err := client.AddAddressListEntry(ctx, fam, l.ID, coreapi.AddressListEntryInput{
				Pattern: args[0], Note: note,
			})
			if err != nil {
				return err
			}
			a.out.Emit(e, func(w io.Writer) {
				// The STORED spelling: core normalizes `@acme.example` to
				// `*@acme.example`, and a user needs to recognize it in `list`.
				a.out.Successf("%s is on the do-not-send list — this account will not mail it", e.Pattern)
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	cmd.Flags().StringVar(&note, "note", "", "why — stored on the entry so `list` can show it later")
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
		Long: "Removes the address from this account's own do-not-send list AND, if the\n" +
			"platform had it suppressed over a hard bounce, lifts that too (the\n" +
			"self-healing case: if the address still does not exist, the next bounce\n" +
			"re-suppresses it). An address the platform suppressed over a spam COMPLAINT\n" +
			"stays refused — only an operator can lift that, because re-mailing a\n" +
			"complainant spends every tenant's shared sending reputation.",
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
			fam, l, err := doNotSendList(ctx, client, acct, false)
			if err != nil {
				return err
			}
			ownRemoved := false
			if l != nil {
				if _, err := client.RemoveAddressListEntry(ctx, fam, l.ID, args[0]); err == nil {
					ownRemoved = true
				} else if !coreapi.IsNotFound(err) {
					return err
				}
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
