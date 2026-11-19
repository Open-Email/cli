package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newAdminSuppressionsCmd exposes the deployment-global suppression list. Rows
// are normally written by the FBL consumer from DSN/ARF reports; `add` is the
// narrow exception, for a complaint that consumer could not PROVE and therefore
// refused to act on (see newSuppressionAddCmd).
//
// Since core #884 a proven complaint is filed against the SENDING account and
// blocks that account alone, so it is not on this list: every verb here reads
// or writes the global rows only, and says so rather than "clear to send".
// Account-scoped rows are `?accountId=` on the same routes, which these verbs
// do not send (core's README points operators at `openemail api` for them).
func newAdminSuppressionsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "suppressions",
		Aliases: []string{"suppression"},
		Short:   "Inspect, add to and lift the global do-not-send list (system-only)",
		Long: "The platform's own evidence that an address must not be mailed. Its scope\n" +
			"follows the evidence: a hard bounce (the address does not exist) is\n" +
			"deployment-global, refused for every account, while a spam complaint is\n" +
			"filed against the account whose mail drew it and blocks that account alone,\n" +
			"so one tenant's complaint never stops another from mailing the same person.\n\n" +
			"These commands read and change the GLOBAL list: hard bounces, blocks entered\n" +
			"by hand, and complaints from before core scoped them, which stay global\n" +
			"until an operator moves them. Account complaints are read and lifted\n" +
			"through the API, with the account's id:\n\n" +
			"  openemail api GET /suppressions --query accountId=ACCOUNT_ID\n" +
			"  openemail api DELETE /suppressions/ADDRESS --query accountId=ACCOUNT_ID\n\n" +
			"Rows are normally written by the feedback-loop consumer, and that remains the\n" +
			"way an address should get here. `add` is the exception for one specific gap:\n" +
			"the consumer only acts on a complaint it can prove we sent, so a genuine\n" +
			"complaint about mail with no correlatable submission is logged and dropped.\n" +
			"`lift` is for an address whose delivery problem has actually been fixed.",
	}
	cmd.AddCommand(
		newSuppressionListCmd(a),
		newSuppressionGetCmd(a),
		newSuppressionAddCmd(a),
		newSuppressionLiftCmd(a),
	)
	return cmd
}

func newSuppressionListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the addresses on the global suppression list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Suppression
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Suppression], error) {
					return client.ListSuppressions(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Suppression]
				page, err = client.ListSuppressions(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"suppressions": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, s := range items {
					rows = append(rows, []string{
						s.Address, s.Reason, fmt.Sprintf("%d", s.EventCount),
						fmtEpoch(s.FirstEventAt), fmtEpoch(s.LastEventAt),
						truncate(strOr(s.Detail, "—"), 40),
					})
				}
				printTable(w, a.out, []string{"ADDRESS", "REASON", "EVENTS", "FIRST", "LAST", "DETAIL"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newSuppressionGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <address>",
		Aliases: []string{"check"},
		Short:   "Check whether one address is on the global list",
		Long: "Answers whether this address is on the deployment-global suppression list.\n" +
			"A \"not suppressed\" answer is a normal result, not an error, but it covers\n" +
			"the global list only: account complaints and address lists can still\n" +
			"refuse the address for one account. Check an account complaint\n" +
			"with `openemail api GET /suppressions/ADDRESS --query accountId=ACCOUNT_ID`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := client.GetSuppression(cmd.Context(), args[0])
			// Core answers 404 when the address has no GLOBAL row. That is the
			// ANSWER to the question asked, so report it as one rather than
			// failing, but not as "clear to send": core's own words are that it
			// is no verdict on an account's complaints or address lists.
			if coreapi.IsNotFound(err) {
				a.out.Emit(map[string]any{"address": args[0], "suppressed": false}, func(w io.Writer) {
					a.out.Successf("%s is not on the global suppression list", args[0])
					a.out.Msgf("  account complaints are not checked here: openemail api GET /suppressions/%s --query accountId=ACCOUNT_ID", args[0])
				})
				return nil
			}
			if err != nil {
				return err
			}
			a.out.Emit(s, func(w io.Writer) {
				a.out.Warnf("%s is suppressed (%s)", s.Address, s.Reason)
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Address", s.Address},
					{"Reason", s.Reason},
					{"Events", fmt.Sprintf("%d", s.EventCount)},
					{"First seen", fmtEpoch(s.FirstEventAt)},
					{"Last seen", fmtEpoch(s.LastEventAt)},
					{"Detail", strOr(s.Detail, "—")},
					{"Source delivery", strOr(s.SourceDeliveryID, "—")},
				})
			})
			return nil
		},
	}
}

// newSuppressionAddCmd is the operator's stand-in for evidence the pipeline
// could not produce.
//
// Core's FBL consumer suppresses only an address it can PROVE we sent to — a
// VERP-decoded envelope recipient, or one a recorded submission correlates to —
// because the report body is attacker-controlled and this list is global, so
// acting on a body-claimed address would be a denial-of-delivery primitive. Only
// JMAP submissions record a correlatable row, so a real complaint about mail
// sent via REST /send, group expansion, Sieve redirect or forwarding is refused
// with an `fbl_suppression_refused` log line and NOTHING happens — the platform
// keeps mailing someone who pressed "this is spam". This verb closes that loop
// by hand, which is why it confirms by default and why core logs every call.
func newSuppressionAddCmd(a *app) *cobra.Command {
	var (
		reason string
		note   string
		yes    bool
	)
	cmd := &cobra.Command{
		Use:     "add <address>",
		Aliases: []string{"create", "suppress"},
		Short:   "Suppress an address by hand",
		Long: "Stops the platform sending to this address, for every account. That is wider\n" +
			"than a complaint the consumer proves, which core files against the sending\n" +
			"account alone.\n\n" +
			"Prefer letting the feedback-loop consumer do this: a row it writes carries the\n" +
			"report as evidence. Reach for `add` when a complaint arrived that the consumer\n" +
			"could not attribute to a recorded submission (look for `fbl_suppression_refused`\n" +
			"in the logs) and the platform is therefore still mailing a complainant.\n\n" +
			"Repeat calls converge on the same row — no duplicate entry, no inflated tally.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Reject a bad value here rather than spending a round trip on a 400
			// that says the same thing less clearly.
			if reason != "" && reason != "complaint" && reason != "hard_bounce" {
				return usageError(fmt.Errorf("--reason must be complaint or hard_bounce, got %q", reason))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes {
				// Say plainly that the blast radius is the whole deployment: this
				// is the one write to the list that no report backs.
				a.out.Warnf("This suppresses %s for EVERY account on this deployment.", args[0])
				if !confirm(fmt.Sprintf("Suppress %s?", args[0])) {
					return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
				}
			}
			s, err := client.AddSuppression(cmd.Context(), coreapi.SuppressionCreate{
				Address: args[0], Reason: reason, Note: note,
			})
			if err != nil {
				return err
			}
			a.out.Emit(s, func(w io.Writer) {
				a.out.Successf("%s is suppressed (%s) — the platform will not send to it", s.Address, s.Reason)
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Address", s.Address},
					{"Reason", s.Reason},
					{"Events", fmt.Sprintf("%d", s.EventCount)},
					{"Detail", strOr(s.Detail, "—")},
				})
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "complaint (default) or hard_bounce")
	cmd.Flags().StringVar(&note, "note", "", "why this was entered by hand; stored on the row so 'get' can answer it later")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	registerEnumFlagCompletion(cmd, "reason", "complaint", "hard_bounce")
	return cmd
}

func newSuppressionLiftCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "lift <address>",
		Aliases: []string{"delete", "rm"},
		Short:   "Lift an address's global suppression",
		Long: "Removes the address's GLOBAL row so mail to it is attempted again. An\n" +
			"account complaint for it, if there is one, stays; lift that with\n" +
			"`openemail api DELETE /suppressions/ADDRESS --query accountId=ACCOUNT_ID`.\n\n" +
			"Lift only when the underlying problem is actually fixed. A hard bounce that\n" +
			"is still a hard bounce will simply re-suppress on the next attempt, and a\n" +
			"lifted complaint means mailing someone who asked you to stop.\n\n" +
			"An ACCOUNT key may call this too, for hard_bounce rows only — the tenant\n" +
			"self-service lift (`openemail suppressions remove` folds it in). A complaint\n" +
			"row answers an account key 404, indistinguishable from an absent row; only\n" +
			"a system key lifts those.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes {
				// Show what the evidence was before asking; a complaint and a
				// bad-address bounce warrant very different decisions.
				if s, gerr := client.GetSuppression(cmd.Context(), args[0]); gerr == nil {
					a.out.Msgf("%s — %s, %d event(s), last %s", s.Address, s.Reason, s.EventCount, fmtEpoch(s.LastEventAt))
					if s.Detail != nil {
						a.out.Msgf("  %s", *s.Detail)
					}
				}
				if !confirm(fmt.Sprintf("Lift the suppression on %s?", args[0])) {
					return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
				}
			}
			deleted, err := client.LiftSuppression(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"address": args[0], "deleted": deleted}, func(w io.Writer) {
				// Not "clear to send": an account complaint is a separate row
				// this lift does not touch.
				a.out.Successf("Global suppression on %s lifted", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
