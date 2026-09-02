package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Open-Email/cli/internal/coreapi"
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
		newMessageContentCmd(a),
		newMessagePartCmd(a),
		newMessageAppendCmd(a),
		newMessageComposeCmd(a),
		newMessageSubmitCmd(a),
		newMessageLearnCmd(a, "junk", "spam"),
		newMessageLearnCmd(a, "not-junk", "ham"),
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
		order  string
		sortBy string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List messages newest-first (or the trash with --trash)",
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
			if trash {
				if label != "" {
					return usageError(errors.New("--label cannot be combined with --trash (expunged messages carry no labels)"))
				}
				return a.listTrash(ctx, client, mbx, all, coreapi.ListOpts{
					Limit: limit, Cursor: cursor, Order: order, SortBy: sortBy,
				})
			}
			opts := coreapi.ListOpts{Label: label, Limit: limit, Cursor: cursor, Order: order, SortBy: sortBy}
			var items []coreapi.MessageMeta
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.MessageMeta], error) {
					return client.ListMessages(ctx, mbx, opts.WithCursor(cur))
				})
			} else {
				var page coreapi.Page[coreapi.MessageMeta]
				page, err = client.ListMessages(ctx, mbx, opts)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"messages": items, "nextCursor": next}, func(w io.Writer) {
				printTable(w, a.out, messageListHeaders, messageListRows(items, sortBy))
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
	addListOrderFlags(cmd, &order, &sortBy)
	return cmd
}

func (a *app) listTrash(ctx context.Context, client *coreapi.Client, mbx string, all bool, opts coreapi.ListOpts) error {
	var items []coreapi.ExpungedMessageMeta
	next := ""
	var err error
	if all {
		items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.ExpungedMessageMeta], error) {
			return client.ListTrash(ctx, mbx, opts.WithCursor(cur))
		})
	} else {
		var page coreapi.Page[coreapi.ExpungedMessageMeta]
		page, err = client.ListTrash(ctx, mbx, opts)
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

func newMessageContentCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "content <messageId>",
		Aliases: []string{"view"},
		Short:   "Show the structured content view: decoded headers, text/html bodies, and attachment list",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			c, err := client.GetContent(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(c, func(w io.Writer) { printContent(w, a.out, args[0], c) })
			return nil
		},
	}
}

func newMessagePartCmd(a *app) *cobra.Command {
	var (
		output string
		raw    bool
	)
	cmd := &cobra.Command{
		Use:   "part <messageId> <section>",
		Short: "Download one MIME part by section number (decoded by default; --raw for the encoded slice)",
		Long: "Download one MIME part by its IMAP section number (from `message content` attachments). " +
			"Decoded by default; --raw serves the stored encoded bytes. Writes to stdout, to a file (-o file), " +
			"or into a directory (-o dir/) under the server-suggested filename.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			rc, hdr, err := client.GetPart(cmd.Context(), mbx, args[0], args[1], raw)
			if err != nil {
				return err
			}
			defer rc.Close()

			// Resolve the destination: stdout, an explicit file, or (when -o is an
			// existing directory) the server-suggested filename inside it.
			dst := io.Writer(os.Stdout)
			path := output
			if output != "" {
				if info, statErr := os.Stat(output); statErr == nil && info.IsDir() {
					path = filepath.Join(output, partFilename(hdr, args[1]))
				}
				f, ferr := os.Create(path)
				if ferr != nil {
					return ferr
				}
				defer f.Close()
				dst = f
			}
			n, err := io.Copy(dst, rc)
			if err != nil {
				return err
			}
			if output != "" {
				a.out.Successf("Wrote %s (%s, %d bytes)", path, strOrEmpty(hdr.Get("Content-Type"), "application/octet-stream"), n)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this file, or into this directory (uses the server filename)")
	cmd.Flags().BoolVar(&raw, "raw", false, "serve the stored encoded slice instead of decoding the transfer encoding")
	return cmd
}

// partFilename derives a save name from the response's Content-Disposition
// filename, falling back to part-<section>.
func partFilename(hdr http.Header, section string) string {
	if cd := hdr.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if name := filepath.Base(params["filename"]); name != "" && name != "." && name != "/" {
				return name
			}
		}
	}
	return "part-" + section
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
			if err := normalizeFlags(flags); err != nil {
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

// newMessageComposeCmd files a structured message WITHOUT sending it. It is the
// sibling of `openemail compose`: same fields, same server-side MIME assembly,
// but routing, relay and the Sent copy are all bypassed.
func newMessageComposeCmd(a *app) *cobra.Command {
	var (
		body                 bodyFlags
		from, subject        string
		to, cc, bcc, replyTo []string
		inReplyTo            string
		references           []string
		label, deliveryID    string
		flags                []string
		internalDate         int64
		filter, draft        bool
	)
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "File a message into this mailbox from fields, without sending it",
		Long: "Build a message from fields and store it — drafts, imports, seeded threads.\n" +
			"Nothing leaves the platform: routing, relay and the Sent copy are all bypassed.\n\n" +
			"  openemail messages compose --from me@x --to you@y --subject Hi --text '…' --draft\n" +
			"  openemail messages compose --from a@x --label Archive --body-file old.txt\n\n" +
			"Use `openemail compose` when the message is meant to go somewhere, and\n" +
			"`openemail messages append` when you already hold raw RFC 5322 bytes rather\n" +
			"than fields.\n\n" +
			"--draft means what a mail client means by a draft: the message is flagged\n" +
			"draft and seen (so it is not counted unread in its own author's mailbox) and\n" +
			"filed into Drafts rather than the inbox. An explicit --label wins.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if from == "" {
				return usageError(errors.New("--from is required (it becomes the message's From header)"))
			}
			// Core refuses a message addressed to nobody (400 no_recipients)
			// even though nothing is sent — say so here rather than after a
			// round trip that may have already staged attachments.
			if len(to)+len(cc)+len(bcc) == 0 {
				return usageError(errors.New("need at least one recipient — pass --to, --cc, or --bcc"))
			}
			fromAddr, err := parseSendAddress(from)
			if err != nil {
				return usageError(fmt.Errorf("--from: %w", err))
			}
			req := coreapi.SendRequest{From: *fromAddr, Subject: subject, InReplyTo: inReplyTo, References: references}
			for _, spec := range [...]struct {
				flag string
				vals []string
				dst  *[]coreapi.SendAddress
			}{
				{"--to", to, &req.To}, {"--cc", cc, &req.Cc},
				{"--bcc", bcc, &req.Bcc}, {"--reply-to", replyTo, &req.ReplyTo},
			} {
				list, perr := parseSendAddresses(spec.vals)
				if perr != nil {
					return usageError(fmt.Errorf("%s: %w", spec.flag, perr))
				}
				*spec.dst = list
			}
			// A stored message needs no body — an empty draft is a real thing to
			// file — so the body is optional here, unlike on the sending verbs.
			if req.Text, req.HTML, err = body.bodies(cmd, false); err != nil {
				return err
			}
			if req.Headers, err = body.headers(); err != nil {
				return err
			}
			if draft {
				flags = append(flags, "draft", "seen")
				// A draft filed into the inbox is not where any mail client
				// looks for it. Drafts is a system label, present on every
				// mailbox, so this cannot fail with unknown_label.
				if !cmd.Flags().Changed("label") {
					label = "Drafts"
				}
			}
			if err := normalizeFlags(flags); err != nil {
				return usageError(err)
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if req.Attachments, err = body.stage(cmd, a, client, mbx); err != nil {
				return err
			}

			opts := coreapi.ComposeOptions{
				Label: label, Flags: flags, Filter: filter,
				// Always carry a delivery id so a post-commit 5xx retry converges
				// instead of filing the message twice.
				DeliveryID: firstNonEmpty(deliveryID, newDeliveryID()),
			}
			if cmd.Flags().Changed("internaldate") {
				opts.InternalDate = &internalDate
			}
			res, raw, err := client.ComposeMessage(cmd.Context(), mbx, req, opts)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) {
				if res.Status == "filtered" {
					a.out.Successf("Filtered by Sieve (redirected=%v) — nothing stored", res.Redirected)
					return
				}
				if res.Duplicate {
					a.out.Successf("Duplicate compose (already stored) — message %s", res.MessageID)
					return
				}
				a.out.Successf("Stored message %s", res.MessageID)
				// The response carries no label, and with --filter an active
				// Sieve `fileinto` can put the message somewhere other than the
				// one asked for — so report the uid and let the caller look
				// rather than assert a location that may be wrong.
				if res.UID != nil {
					a.out.Msgf("  uid %d (uidvalidity %s)", *res.UID, int64Or(res.UIDValidity, "—"))
				}
				if filter {
					a.out.Msgf("  filtered on the way in — check the label with `openemail messages get %s`", res.MessageID)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "From address: address or \"Name <addr>\" (required)")
	cmd.Flags().StringArrayVar(&to, "to", nil, "To recipient, repeatable")
	cmd.Flags().StringArrayVar(&cc, "cc", nil, "Cc recipient, repeatable")
	cmd.Flags().StringArrayVar(&bcc, "bcc", nil, "Bcc recipient, repeatable")
	cmd.Flags().StringArrayVar(&replyTo, "reply-to", nil, "Reply-To address, repeatable")
	cmd.Flags().StringVar(&subject, "subject", "", "subject")
	body.register(cmd)
	cmd.Flags().StringVar(&inReplyTo, "in-reply-to", "", "In-Reply-To message-id header")
	cmd.Flags().StringArrayVar(&references, "references", nil, "References message-id, repeatable")
	cmd.Flags().StringVar(&label, "label", "", "target label (default: inbox)")
	cmd.Flags().StringSliceVar(&flags, "flags", nil, "initial flags, comma-separated (seen,answered,flagged,draft,deleted)")
	cmd.Flags().BoolVar(&draft, "draft", false, "file as a draft: flags draft,seen into the Drafts label")
	cmd.Flags().Int64Var(&internalDate, "internaldate", 0, "receivedAt as epoch seconds (default: now)")
	cmd.Flags().BoolVar(&filter, "filter", false, "run the mailbox's active filter on the stored message")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID)")
	return cmd
}

// newMessageLearnCmd builds one of the two spam-training verbs. They are
// separate commands rather than `learn --class`, because the two directions are
// what a user actually means and a mistyped class silently trains backwards.
func newMessageLearnCmd(a *app, use, class string) *cobra.Command {
	short := "Report messages as junk (trains this mailbox's spam filter)"
	long := "Teach the filter that one or more messages are spam. The samples train THIS\n" +
		"mailbox's personal overlay, so your idea of junk never becomes anyone else's.\n\n" +
		"This is training only: it does not move, flag or delete the messages. Pair it\n" +
		"with `openemail messages move` if you also want them out of the inbox."
	if class == "ham" {
		short = "Report messages as NOT junk (trains this mailbox's spam filter)"
		long = "Teach the filter that one or more messages are legitimate: the correction for\n" +
			"something that was wrongly classified as spam. The samples train THIS\n" +
			"mailbox's personal overlay.\n\n" +
			"This is training only: it does not move or unflag the messages."
	}
	return &cobra.Command{
		Use:   use + " <messageId> [messageId...]",
		Short: short,
		Long: long + "\n\nAccepted fire-and-forget: a success means the sample was submitted to the\n" +
			"filter, not that it has already been learned. Repeated calls on the same\n" +
			"message dedupe filter-side, so re-running is harmless.\n\n" +
			"Several ids are ONE call: core resolves them in a single read and submits\n" +
			"the samples under one background budget. Up to 200 at a time. Past that,\n" +
			"send several batches, because core trains a prefix and reports a truncation\n" +
			"rather than stretching one budget to fit.\n\n" +
			"An id that is not in the live tier is reported `not_found` on its own row\n" +
			"and does not stop the others; the command exits non-zero so a script\n" +
			"notices without parsing the table.",
		Args: cobra.RangeArgs(1, 200),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if len(args) > 1 {
				return a.learnBatch(cmd, client, mbx, args, class)
			}
			status, err := client.LearnMessage(cmd.Context(), mbx, args[0], class)
			if err != nil {
				// A deployment with no filter configured is a fact about the
				// platform, not a mistake the user made.
				if ae, ok := coreapi.AsAPIError(err); ok && ae.Code == "learning_unavailable" {
					a.out.Warnf("this deployment has no spam filter configured — nothing to train")
				}
				return err
			}
			a.out.Emit(map[string]any{"status": status, "class": class}, func(w io.Writer) {
				verb := "junk"
				if class == "ham" {
					verb = "not junk"
				}
				a.out.Successf("Reported %s as %s — submitted to the filter", args[0], verb)
			})
			return nil
		},
	}
}

// learnBatch is the many-ids half of the two spam-training verbs. Split out of
// the command body rather than inlined because both verbs share it, and because
// the single-id path deliberately stays on the per-message route so its --json
// shape ({status, class}) keeps meaning what it always has.
func (a *app) learnBatch(cmd *cobra.Command, client *coreapi.Client, mbx string, ids []string, class string) error {
	verb := "junk"
	if class == "ham" {
		verb = "not junk"
	}
	res, err := client.LearnMessages(cmd.Context(), mbx, ids, class)
	if err != nil {
		// The feature being unconfigured is a property of the deployment, not a
		// mistake the user made. Core answers it before resolving any id, so the
		// whole batch is untrained and there is nothing partial to report.
		if ae, ok := coreapi.AsAPIError(err); ok && ae.Code == "learning_unavailable" {
			a.out.Warnf("this deployment has no spam filter configured — nothing to train")
		}
		return err
	}
	byID := make(map[string]coreapi.BatchLearnEntry, len(res.Results))
	for _, e := range res.Results {
		byID[e.ID] = e
	}
	a.out.Emit(res, func(w io.Writer) {
		// Ranged over the REQUEST, not the response, so an id the server said
		// nothing about still gets a row rather than vanishing.
		rows := make([][]string, 0, len(ids))
		for _, id := range ids {
			e, ok := byID[id]
			if !ok {
				rows = append(rows, []string{id, "no answer"})
				continue
			}
			rows = append(rows, []string{id, e.Status})
		}
		printTable(w, a.out, []string{"MESSAGE", "STATUS"}, rows)
	})
	accepted, missing := 0, 0
	for _, id := range ids {
		if e, ok := byID[id]; ok && e.Status == "accepted" {
			accepted++
			continue
		}
		missing++
	}
	if missing == 0 {
		a.out.Successf("Reported %d message(s) as %s in one call", accepted, verb)
		return nil
	}
	a.out.Warnf("Reported %d of %d as %s; %d could not be found in the live tier", accepted, len(ids), verb, missing)
	return silentExit(1)
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
			if err := normalizeFlags(set); err != nil {
				return usageError(err)
			}
			if err := normalizeFlags(clear); err != nil {
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
					a.out.Successf("Purged message %s", id)
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
		Use:   "restore <messageId> [messageId...]",
		Short: "Restore expunged messages from trash (many ids land in one atomic call)",
		Long: "Restores messages from the trash, re-attaching each under the labels it had\n" +
			"before it was expunged (or INBOX if none survive), with fresh UIDs as IMAP\n" +
			"requires.\n\n" +
			"Several ids are ONE call against the mailbox, which is the point: undoing a\n" +
			"bulk delete lands in a single commit rather than as an arbitrary partial\n" +
			"subset if something fails halfway. Up to 200 at a time.\n\n" +
			"An id that is missing, already live, or already purged is reported\n" +
			"`not_found` on its own row and does not stop the others; the command exits\n" +
			"non-zero so a script notices without parsing the table.",
		Args: cobra.RangeArgs(1, 200),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			// A single id keeps the per-message route, so its --json shape stays
			// what it has always been; only the batch answer is new.
			if len(args) == 1 {
				res, rerr := client.RestoreMessage(cmd.Context(), mbx, args[0])
				if rerr != nil {
					return rerr
				}
				a.out.Emit(res, func(w io.Writer) {
					a.out.Successf("Restored message %s", res.Message.ID)
					a.out.Msgf("  labels: %s", labelMembershipDisplay(&res.Message))
				})
				return nil
			}

			res, err := client.RestoreMessages(cmd.Context(), mbx, args)
			if err != nil {
				return err
			}
			byID := make(map[string]coreapi.BatchRestoreEntry, len(res.Results))
			for _, e := range res.Results {
				byID[e.ID] = e
			}
			restored, missing := 0, 0
			a.out.Emit(res, func(w io.Writer) {
				// Ranged over the REQUEST, not the response, so an id the server
				// said nothing about still gets a row rather than vanishing.
				rows := make([][]string, 0, len(args))
				for _, id := range args {
					e, ok := byID[id]
					if !ok {
						rows = append(rows, []string{id, "no answer", "—"})
						continue
					}
					labels := "—"
					if e.Message != nil {
						labels = labelMembershipDisplay(e.Message)
					}
					rows = append(rows, []string{id, e.Status, labels})
				}
				printTable(w, a.out, []string{"MESSAGE", "STATUS", "LABELS"}, rows)
			})
			for _, id := range args {
				if e, ok := byID[id]; ok && e.Status == "restored" {
					restored++
					continue
				}
				missing++
			}
			if missing == 0 {
				a.out.Successf("Restored %d message(s) in one call", restored)
				return nil
			}
			a.out.Warnf("Restored %d of %d — %d could not be found (already live, or purged)", restored, len(args), missing)
			return silentExit(1)
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
			a.out.Emit(map[string]any{"purgedCount": purged}, func(w io.Writer) {
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
