package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newThreadsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "threads",
		Aliases: []string{"thread"},
		Short:   "List and read conversation threads",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newThreadListCmd(a),
		newThreadGetCmd(a),
	)
	return cmd
}

func newThreadListCmd(a *app) *cobra.Command {
	var (
		label  string
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List threads, newest activity first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mbx, err := a.resolveMailbox(ctx, client, "")
			if err != nil {
				return err
			}
			var items []coreapi.ThreadListItem
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.ThreadListItem], error) {
					return client.ListThreads(ctx, mbx, label, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.ThreadListItem]
				page, err = client.ListThreads(ctx, mbx, label, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"threads": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, t := range items {
					rows = append(rows, []string{
						t.ThreadID, fmt.Sprintf("%d", t.MessageCount),
						fmtEpoch(t.LastReceivedAt),
						truncate(t.Exemplar.EnvelopeFrom, 26),
						truncate(msgSubjectDisplay(t.Exemplar), 40),
					})
				}
				printTable(w, a.out, []string{"THREAD", "MSGS", "LAST", "FROM", "SUBJECT"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "only threads with a member in this label")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–100)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newThreadGetCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:   "get <threadId>",
		Short: "Show a thread's messages (oldest first) and reply context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mbx, err := a.resolveMailbox(ctx, client, "")
			if err != nil {
				return err
			}
			tv, next, err := client.GetThread(ctx, mbx, args[0], limit, cursor)
			if err != nil {
				return err
			}
			// --all drains member pages, accumulating into one view.
			if all {
				for next != "" {
					more, n2, merr := client.GetThread(ctx, mbx, args[0], limit, next)
					if merr != nil {
						return merr
					}
					tv.Messages = append(tv.Messages, more.Messages...)
					if n2 == next {
						break
					}
					next = n2
				}
			}
			a.out.Emit(map[string]any{"thread": tv, "nextCursor": next}, func(w io.Writer) {
				a.out.Msgf("Thread %s — %d message(s)", tv.ThreadID, tv.MessageCount)
				if tv.CanonicalThreadID != nil {
					a.out.Warnf("requested id was merged — canonical thread is %s", *tv.CanonicalThreadID)
				}
				printTable(w, a.out, messageListHeaders, messageListRows(tv.Messages))
				if tv.ReplyContext != nil {
					a.out.Msgf("reply subject: %s", strOr(tv.ReplyContext.Subject, "—"))
					a.out.Msgf("reply to:      %s", strOr(tv.ReplyContext.To, "—"))
				}
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every member page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "member pagination cursor")
	return cmd
}
