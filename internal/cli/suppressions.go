package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newAdminSuppressionsCmd exposes the deployment-global do-not-send list. Rows
// are written by the FBL consumer from DSN/ARF reports; nothing here creates
// one, which is deliberate — a suppression is evidence a receiver rejected or
// complained about mail, not an operator preference.
func newAdminSuppressionsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "suppressions",
		Aliases: []string{"suppression"},
		Short:   "Inspect and lift the do-not-send list (system-only)",
		Long: "The suppression list is deployment-global: an address on it is refused for\n" +
			"every account, because the evidence is a receiver's own hard bounce or spam\n" +
			"complaint rather than a per-tenant setting.\n\n" +
			"Rows are only ever written by the feedback-loop consumer, so there is no\n" +
			"`add` verb — the only operator action is `lift`, for an address whose\n" +
			"delivery problem has actually been fixed.",
	}
	cmd.AddCommand(
		newSuppressionListCmd(a),
		newSuppressionGetCmd(a),
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
		Short:   "List suppressed recipient addresses",
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
		Short:   "Check whether one address is suppressed",
		Long: "Answers whether this address can currently be sent to. A \"not suppressed\"\n" +
			"answer is a normal result, not an error: it means the address is clear.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := client.GetSuppression(cmd.Context(), args[0])
			// Core answers 404 for "clear to send". That is the ANSWER to the
			// question asked, so report it as one rather than failing.
			if coreapi.IsNotFound(err) {
				a.out.Emit(map[string]any{"address": args[0], "suppressed": false}, func(w io.Writer) {
					a.out.Successf("%s is not suppressed — clear to send", args[0])
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

func newSuppressionLiftCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "lift <address>",
		Aliases: []string{"delete", "rm"},
		Short:   "Clear an address to send again",
		Long: "Removes the suppression so mail to this address is attempted again.\n\n" +
			"Lift only when the underlying problem is actually fixed. A hard bounce that\n" +
			"is still a hard bounce will simply re-suppress on the next attempt, and a\n" +
			"lifted complaint means mailing someone who asked you to stop.",
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
				a.out.Successf("Suppression lifted — %s is clear to send", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
