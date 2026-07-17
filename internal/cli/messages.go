package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newMessagesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"message", "msg"},
		Short:   "List, read, append, flag, label, and delete a mailbox's messages",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newMessageListCmd(a),
		newMessageGetCmd(a),
		newMessageRawCmd(a),
		newMessageAppendCmd(a),
		newMessageFlagCmd(a),
		newMessageLabelCmd(a),
		newMessageMoveCmd(a),
		newMessageDeleteCmd(a),
		newMessageRestoreCmd(a),
		newMessageTrashCmd(a),
		newMessageMimeCmd(a),
	)
	return cmd
}

func newMessageListCmd(a *app) *cobra.Command {
	var (
		label  string
		trash  bool
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List messages newest-first (or the trash with --trash)",
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
			if trash {
				if label != "" {
					return usageError(errors.New("--label cannot be combined with --trash (expunged messages carry no labels)"))
				}
				return a.listTrash(ctx, client, mbx, all, limit, cursor)
			}
			var items []coreapi.MessageMeta
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.MessageMeta], error) {
					return client.ListMessages(ctx, mbx, label, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.MessageMeta]
				page, err = client.ListMessages(ctx, mbx, label, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"messages": items, "nextCursor": next}, func(w io.Writer) {
				printTable(w, a.out, messageListHeaders, messageListRows(items))
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "filter to one label (live messages only)")
	cmd.Flags().BoolVar(&trash, "trash", false, "list soft-deleted (expunged) messages")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func (a *app) listTrash(ctx context.Context, client *coreapi.Client, mbx string, all bool, limit int, cursor string) error {
	var items []coreapi.ExpungedMessageMeta
	next := ""
	var err error
	if all {
		items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.ExpungedMessageMeta], error) {
			return client.ListTrash(ctx, mbx, limit, cur)
		})
	} else {
		var page coreapi.Page[coreapi.ExpungedMessageMeta]
		page, err = client.ListTrash(ctx, mbx, limit, cursor)
		items, next = page.Items, page.NextCursor
	}
	if err != nil {
		return err
	}
	a.out.Emit(map[string]any{"messages": items, "nextCursor": next}, func(w io.Writer) {
		rows := make([][]string, 0, len(items))
		for _, m := range items {
			rows = append(rows, []string{
				m.ID, truncate(m.EnvelopeFrom, 26), truncate(msgSubjectDisplay(m.MessageMeta), 34),
				fmtEpoch(m.ExpungedAt), fmtEpoch(m.PurgeAfter),
			})
		}
		printTable(w, a.out, []string{"ID", "FROM", "SUBJECT", "EXPUNGED", "PURGE AFTER"}, rows)
		a.moreHint(next)
	})
	return nil
}

func newMessageGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <messageId>",
		Short: "Show one live message's metadata",
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
			m, err := client.GetMessage(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(m, func(w io.Writer) { printMessageMeta(w, a.out, m) })
			return nil
		},
	}
}

func newMessageRawCmd(a *app) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "raw <messageId>",
		Short: "Stream a message's raw RFC822 body to stdout or a file",
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
			rc, err := client.GetMessageRaw(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			defer rc.Close()
			var dst io.Writer = os.Stdout
			if output != "" {
				f, ferr := os.Create(output)
				if ferr != nil {
					return ferr
				}
				defer f.Close()
				dst = f
			}
			if _, err := io.Copy(dst, rc); err != nil {
				return err
			}
			if output != "" {
				a.out.Successf("Wrote %s", output)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this file instead of stdout")
	return cmd
}

func newMessageAppendCmd(a *app) *cobra.Command {
	var (
		file, label, envFrom, envTo, deliveryID string
		flags                                   []string
		internalDate                            int64
		filter                                  bool
	)
	cmd := &cobra.Command{
		Use:   "append",
		Short: "Append a raw MIME message from a file or stdin (IMAP APPEND, bypasses routing)",
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
			if err := validateFlags(flags); err != nil {
				return usageError(err)
			}
			getBody, length, cleanup, err := openRawBody(file)
			if err != nil {
				return err
			}
			defer cleanup()

			opts := coreapi.AppendOptions{
				Label: label, Flags: flags, Filter: filter,
				EnvelopeFrom: envFrom, EnvelopeTo: envTo,
				// Always carry a delivery id so a post-commit 5xx retry converges
				// instead of appending a duplicate.
				DeliveryID: firstNonEmpty(deliveryID, newDeliveryID()),
			}
			if cmd.Flags().Changed("internaldate") {
				opts.InternalDate = &internalDate
			}
			res, raw, err := client.AppendMessage(cmd.Context(), mbx, opts, getBody, length)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) {
				if res.Status == "filtered" {
					a.out.Successf("Filtered by Sieve (redirected=%v) — nothing stored", res.Redirected)
					return
				}
				if res.Duplicate {
					a.out.Successf("Duplicate append (already stored) — message %s", res.MessageID)
					return
				}
				a.out.Successf("Appended message %s", res.MessageID)
				if res.UID != nil {
					a.out.Msgf("  uid %d  label %s", *res.UID, strOrEmpty(label, "INBOX"))
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "message file to append (default: stdin)")
	cmd.Flags().StringVar(&label, "label", "", "target label (default: inbox)")
	cmd.Flags().StringSliceVar(&flags, "flags", nil, "initial flags, comma-separated (seen,answered,flagged,draft,deleted)")
	cmd.Flags().Int64Var(&internalDate, "internaldate", 0, "IMAP INTERNALDATE as epoch seconds (default: now)")
	cmd.Flags().BoolVar(&filter, "filter", false, "run the mailbox's active Sieve script on the append")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID)")
	cmd.Flags().StringVar(&envFrom, "envelope-from", "", "X-Envelope-From (recorded verbatim)")
	cmd.Flags().StringVar(&envTo, "envelope-to", "", "X-Envelope-To (recorded verbatim)")
	return cmd
}

func newMessageFlagCmd(a *app) *cobra.Command {
	var set, clear []string
	cmd := &cobra.Command{
		Use:   "flag <messageId>",
		Short: "Set or clear message flags (STORE)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(set) == 0 && len(clear) == 0 {
				return usageError(errors.New("nothing to do — pass --set and/or --clear"))
			}
			if err := validateFlags(set); err != nil {
				return usageError(err)
			}
			if err := validateFlags(clear); err != nil {
				return usageError(err)
			}
			return a.patchMessage(cmd, args[0], coreapi.PatchInput{FlagsSet: set, FlagsClear: clear})
		},
	}
	cmd.Flags().StringSliceVar(&set, "set", nil, "flags to set, comma-separated")
	cmd.Flags().StringSliceVar(&clear, "clear", nil, "flags to clear, comma-separated")
	return cmd
}

func newMessageLabelCmd(a *app) *cobra.Command {
	var add, remove []string
	cmd := &cobra.Command{
		Use:   "label <messageId>",
		Short: "Add or remove labels (removing the last label moves the message to trash)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(add) == 0 && len(remove) == 0 {
				return usageError(errors.New("nothing to do — pass --add and/or --remove"))
			}
			return a.patchMessage(cmd, args[0], coreapi.PatchInput{LabelsAdd: add, LabelsRemove: remove})
		},
	}
	cmd.Flags().StringSliceVar(&add, "add", nil, "labels to add, comma-separated")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "labels to remove, comma-separated")
	return cmd
}

func newMessageMoveCmd(a *app) *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "move <messageId> --from <label> --to <label>",
		Short: "Move a message between labels (adds --to, removes --from atomically)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return usageError(errors.New("both --from and --to are required"))
			}
			return a.patchMessage(cmd, args[0], coreapi.PatchInput{LabelsAdd: []string{to}, LabelsRemove: []string{from}})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source label to remove")
	cmd.Flags().StringVar(&to, "to", "", "destination label to add")
	return cmd
}

// patchMessage applies a PATCH and renders the union response.
func (a *app) patchMessage(cmd *cobra.Command, messageID string, in coreapi.PatchInput) error {
	client, err := a.authedClient()
	if err != nil {
		return err
	}
	mbx, err := a.resolveMailbox(cmd.Context(), client, "")
	if err != nil {
		return err
	}
	res, err := client.PatchMessage(cmd.Context(), mbx, messageID, in)
	if err != nil {
		return err
	}
	if res.Expunged {
		a.out.Emit(map[string]any{"expunged": true, "id": messageID, "expungedAt": res.ExpungedAt, "purgeAfter": res.PurgeAfter}, func(w io.Writer) {
			a.out.Warnf("message %s had its last label removed — moved to trash", messageID)
			if res.PurgeAfter != nil {
				a.out.Msgf("  restorable until %s", fmtEpoch(*res.PurgeAfter))
			}
		})
		return nil
	}
	m := res.MessageMeta
	a.out.Emit(&m, func(w io.Writer) {
		a.out.Successf("Updated message %s", messageID)
		printMessageMeta(w, a.out, &m)
	})
	return nil
}

func newMessageDeleteCmd(a *app) *cobra.Command {
	var (
		label string
		purge bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:     "delete <messageId>",
		Aliases: []string{"rm"},
		Short:   "Expunge a message to trash (default), detach one label (--label), or purge (--purge)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id := args[0]
			if label != "" && purge {
				return usageError(errors.New("--label and --purge cannot be combined"))
			}
			if purge && !yes {
				if !confirmTyped(fmt.Sprintf("PURGE message %s — permanent, bypasses trash.", id), id) {
					return usageError(errors.New("aborted (type the message id to confirm, or pass --yes)"))
				}
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			res, err := client.DeleteMessage(cmd.Context(), mbx, id, label, purge)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				switch {
				case res.Purged:
					a.out.Successf("Purged message %s (blobOrphaned=%v)", id, res.BlobOrphaned)
				case res.RemovedFromLabel != nil:
					if res.Expunged {
						a.out.Successf("Detached message %s from %s — no labels left, moved to trash", id, *res.RemovedFromLabel)
					} else {
						a.out.Successf("Detached message %s from %s", id, *res.RemovedFromLabel)
					}
				default:
					a.out.Successf("Expunged message %s to trash", id)
					if res.PurgeAfter != nil {
						a.out.Msgf("  restorable until %s", fmtEpoch(*res.PurgeAfter))
					}
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "detach from this one label instead of expunging")
	cmd.Flags().BoolVar(&purge, "purge", false, "permanently purge (bypasses trash, irreversible)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newMessageRestoreCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <messageId>",
		Short: "Restore an expunged message from trash",
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
			res, err := client.RestoreMessage(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Restored message %s", res.Message.ID)
				a.out.Msgf("  labels: %s", labelMembershipDisplay(&res.Message))
			})
			return nil
		},
	}
}

func newMessageTrashCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "Trash operations (empty)",
	}
	var yes bool
	empty := &cobra.Command{
		Use:   "empty",
		Short: "Permanently purge every message in the trash",
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
			if !yes && !confirmTyped(fmt.Sprintf("EMPTY the trash for mailbox %s — permanent.", mbx), mbx) {
				return usageError(errors.New("aborted (type the mailbox id to confirm, or pass --yes)"))
			}
			purged, err := client.EmptyTrash(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"purged": purged}, func(w io.Writer) {
				a.out.Successf("Purged %d message(s) from trash", purged)
			})
			return nil
		},
	}
	empty.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.AddCommand(empty)
	return cmd
}

func newMessageMimeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "mime <messageId> [messageId...]",
		Short: "Fetch cached ENVELOPE/BODYSTRUCTURE metadata for up to 200 messages (--json power-user)",
		Args:  cobra.RangeArgs(1, 200),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			mime, err := client.MimeBatch(cmd.Context(), mbx, args)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"mime": mime}, func(w io.Writer) {
				rows := make([][]string, 0, len(args))
				for _, id := range args {
					entry, ok := mime[id]
					status := "no cache"
					ver := "—"
					if ok && entry != nil {
						status = "cached"
						ver = fmt.Sprintf("%d", entry.Ver)
					}
					rows = append(rows, []string{id, status, ver})
				}
				printTable(w, a.out, []string{"ID", "MIME CACHE", "VER"}, rows)
			})
			return nil
		},
	}
}

// strOrEmpty returns v when non-empty, else the fallback.
func strOrEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
