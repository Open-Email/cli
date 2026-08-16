package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
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
		newThreadReplyCmd(a),
	)
	return cmd
}

func newThreadListCmd(a *app) *cobra.Command {
	var (
		label  string
		all    bool
		limit  int
		cursor string
		order  string
		sortBy string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List threads, newest activity first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateListOrder(order, sortBy); err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mbx, err := a.resolveMailbox(ctx, client, "")
			if err != nil {
				return err
			}
			opts := coreapi.ListOpts{Label: label, Limit: limit, Cursor: cursor, Order: order, SortBy: sortBy}
			var items []coreapi.ThreadListItem
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.ThreadListItem], error) {
					return client.ListThreads(ctx, mbx, opts.WithCursor(cur))
				})
			} else {
				var page coreapi.Page[coreapi.ThreadListItem]
				page, err = client.ListThreads(ctx, mbx, opts)
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
						// The LAST column is the date the page was ORDERED by, so
						// the column and the row order cannot disagree: under
						// sortBy=date that is the exemplar's own sent date (the
						// exemplar being the newest-dated member), not the
						// thread's MAX(receivedAt).
						fmtEpoch(threadActivityAt(t, sortBy)),
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
	addListOrderFlags(cmd, &order, &sortBy)
	return cmd
}

// threadActivityAt is the epoch the thread row's LAST column shows, matching
// the key the listing was sorted by. Under sortBy=date core makes the exemplar
// the newest-DATED member, so the exemplar's own date IS the sort position;
// LastReceivedAt (MAX over live members) is the arrival-mode answer.
func threadActivityAt(t coreapi.ThreadListItem, sortBy string) int64 {
	if sortBy == "date" && t.Exemplar.SentAt != nil {
		return *t.Exemplar.SentAt
	}
	if sortBy == "date" {
		return t.Exemplar.ReceivedAt
	}
	return t.LastReceivedAt
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
				printTable(w, a.out, messageListHeaders, messageListRows(tv.Messages, ""))
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

func newThreadReplyCmd(a *app) *cobra.Command {
	var (
		body                        bodyFlags
		subject, fromName           string
		deliveryID                  string
		noSave, bounce, showContext bool
	)
	cmd := &cobra.Command{
		Use:   "reply <threadId>",
		Short: "Reply to a thread — the server derives recipient, subject and threading",
		Long: "Send only what you actually wrote. The recipient, the \"Re: …\" subject, and the\n" +
			"In-Reply-To/References chain are all derived from the conversation, so the\n" +
			"reply threads correctly in every client that receives it.\n\n" +
			"  openemail threads reply 01H… --text 'On it, thanks.'\n" +
			"  openemail threads reply 01H… --body-file reply.txt --attach notes.pdf\n\n" +
			"Prefer this over `openemail compose` whenever a reply is what you mean:\n" +
			"rebuilding threading headers by hand is the classic mail-client bug, and a\n" +
			"broken chain is invisible until someone else's client fails to group the\n" +
			"conversation. A thread with nobody to answer (a null-sender bounce, say) is\n" +
			"refused with no_reply_target rather than sent to nowhere.\n\n" +
			"--subject overrides the derived subject, which renames the thread for the\n" +
			"recipient — pass it only when you mean that.",
		Args: cobra.ExactArgs(1),
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
			req := coreapi.ThreadReplyRequest{Subject: subject, FromName: fromName}
			if req.Text, req.HTML, err = body.bodies(cmd, true); err != nil {
				return err
			}
			if req.Headers, err = body.headers(); err != nil {
				return err
			}
			// --show-context prints what the server will derive, so a user can
			// see who is about to be answered before the message leaves.
			if showContext {
				tv, _, gerr := client.GetThread(ctx, mbx, args[0], 1, "")
				if gerr != nil {
					return gerr
				}
				if tv.ReplyContext != nil {
					a.out.Msgf("replying to: %s", strOr(tv.ReplyContext.To, "—"))
					a.out.Msgf("subject:     %s", strOr(tv.ReplyContext.Subject, "—"))
				}
			}
			if req.Attachments, err = body.stage(cmd, a, client, mbx); err != nil {
				return err
			}

			// Always carry a delivery id, as the sibling send verbs do: a
			// post-commit timeout retried by hand must converge on the same
			// submission rather than mailing the recipient twice.
			opts := coreapi.SendOptions{DeliveryID: firstNonEmpty(deliveryID, newDeliveryID())}
			if noSave {
				no := false
				opts.Save = &no
			}
			opts.Bounce = boolPtrIfChanged(cmd, "bounce", bounce)

			res, raw, err := client.ReplyToThread(ctx, mbx, args[0], req, opts)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) { renderSendResult(a.out, res) })
			for _, r := range res.Recipients {
				if r.Status == "failed" {
					return silentExit(1)
				}
			}
			return nil
		},
	}
	body.register(cmd)
	cmd.Flags().StringVar(&subject, "subject", "", "override the derived \"Re: …\" subject (renames the thread)")
	cmd.Flags().StringVar(&fromName, "from-name", "", "display name for the From header (the address is always this mailbox's)")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID; reused on retry)")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "do not keep a Sent copy (kept by default)")
	cmd.Flags().BoolVar(&bounce, "bounce", false, "deliver a DSN into this mailbox on terminal relay failure")
	cmd.Flags().BoolVar(&showContext, "show-context", false, "print the derived recipient and subject before sending")
	return cmd
}
