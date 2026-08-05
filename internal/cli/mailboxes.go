package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
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
		newMailboxPurgeCmd(a),
		newMailboxUseCmd(a),
		newMailboxSendUsageCmd(a),
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
	cmd.Flags().StringVar(&address, "address", "", "claim this address: the route is created atomically with the mailbox, which receives mail immediately (address_taken if already routed)")
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
					address := strOr(m.PrimaryAddress, "—")
					// A frozen mailbox is marked in the LIST, not just in `get`.
					// The question an operator brings to this table during an
					// incident is "which of these did I stop?", and answering it
					// only in a per-mailbox read means opening them one at a time.
					// Inline rather than a column: freezing is rare, and a column
					// that reads "enabled" on every row costs width on every
					// listing to serve the exception.
					// The two modes are marked apart: an operator scanning this
					// during an incident needs to know which mailboxes will BOUNCE
					// mail and which are merely holding it.
					switch {
					case m.SendDisabled:
						address += "  [FROZEN]"
					case m.SendPaused:
						address += "  [PAUSED]"
					}
					rows = append(rows, []string{
						m.ID, address, fmtQuota(m.QuotaBytes),
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

// newMailboxSendUsageCmd exposes the outbound send allowance.
//
// Its reason for existing is that the alternative is discovery by refusal: a
// caller learns its bound one 429 at a time, and an operator investigating a
// spike cannot see consumption at all (the domain traffic aggregate has no
// per-mailbox axis). Safe to poll — core answers this without provisioning a
// store that has never sent.
func newMailboxSendUsageCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "send-usage [mailboxId]",
		Aliases: []string{"usage"},
		Short:   "Show outbound send-allowance usage for the current window",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			// Defaults to the selected mailbox, like every other per-mailbox
			// read: this is a command an owner runs about themselves at least as
			// often as an operator runs it about someone else.
			mailboxID, err := a.resolveMailbox(cmd.Context(), client, ref)
			if err != nil {
				return err
			}
			u, err := client.GetSendUsage(cmd.Context(), mailboxID)
			if err != nil {
				return err
			}
			a.out.Emit(u, func(w io.Writer) {
				window := fmt.Sprintf("%dh", u.WindowSeconds/3600)
				rows := [][]string{
					{"Sending", fmtSendState(u.Disabled, u.Paused)},
					{"Window", "rolling " + window},
					{"Messages", fmt.Sprintf("%d of %s", u.Messages, fmtSendLimit(u.MsgsPerDay))},
					{"Recipients", fmt.Sprintf("%d of %s", u.Recipients, fmtSendLimit(u.RcptsPerDay))},
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
				// Messages counting distinct CONTENT is the one thing about
				// these numbers that surprises people: a fan-out submitted once
				// per recipient (what the SMTP path does) shows as one message
				// and N recipients, so the two rows will not look proportional.
				a.out.Msgf("messages count distinct content — the same message sent to N people is 1 message, N recipients")
			})
			return nil
		},
	}
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
	var address, quota, sendMsgs, sendRcpts string
	var freeze, unfreeze, pause, resume bool
	cmd := &cobra.Command{
		Use:   "update <mailboxId>",
		Short: "Update a mailbox's quota, primary address, or send policy",
		Long: "Update one mailbox. The send controls come in two modes:\n\n" +
			"  --freeze  permanent (403): an SMTP client gets 550 and gives up, and mail\n" +
			"            already queued in the relay is bounced.\n" +
			"  --pause   reversible (429): an SMTP client gets 451 and its own queue holds\n" +
			"            the mail, and the relay backlog is deferred rather than bounced.\n\n" +
			"Pausing destroys no mail; freezing does. Neither affects receiving or reading.\n" +
			"Both need a system key — a tenant that can lift its own hold does not have one.",
		Args: cobra.ExactArgs(1),
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
			// Send policy — SYSTEM-only at core (a tenant that can lift its own
			// freeze or raise its own caps has a suggestion, not a limit), so an
			// account key gets 403 system_credentials_required here.
			//
			// Freeze and unfreeze are separate boolean flags rather than one
			// --send-disabled=true|false because this is the destructive-ish
			// direction of an abuse control: `--freeze` and `--unfreeze` cannot
			// be confused for each other at 2am, and a mistyped value cannot
			// silently mean the opposite of what was meant.
			if cmd.Flags().Changed("freeze") && cmd.Flags().Changed("unfreeze") {
				return usageError(errors.New("--freeze and --unfreeze are mutually exclusive"))
			}
			if cmd.Flags().Changed("pause") && cmd.Flags().Changed("resume") {
				return usageError(errors.New("--pause and --resume are mutually exclusive"))
			}
			if cmd.Flags().Changed("freeze") {
				patch["sendDisabled"] = true
			}
			if cmd.Flags().Changed("unfreeze") {
				patch["sendDisabled"] = false
			}
			// The reversible mode, same shape and same reasoning: two flags so
			// "hold" and "release" can never be confused at 2am. --freeze --pause
			// together is allowed — core resolves it toward the freeze, and
			// escalating a hold to a stop in one call is a real thing to want.
			if cmd.Flags().Changed("pause") {
				patch["sendPaused"] = true
			}
			if cmd.Flags().Changed("resume") {
				patch["sendPaused"] = false
			}
			for _, f := range []struct {
				flag  string
				field string
				val   *string
			}{
				{"send-msgs-per-day", "sendMsgsPerDay", &sendMsgs},
				{"send-rcpts-per-day", "sendRcptsPerDay", &sendRcpts},
			} {
				if !cmd.Flags().Changed(f.flag) {
					continue
				}
				v, perr := parseSendCapFlag(*f.val)
				if perr != nil {
					return usageError(perr)
				}
				patch[f.field] = v // nil marshals as JSON null = clear the override
			}

			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass --address, --quota, --freeze/--unfreeze, --pause/--resume, or a --send-*-per-day cap"))
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
	cmd.Flags().StringVar(&address, "address", "", "new primary address (must already route to this mailbox — bind via 'routes create' first; empty is not a clear)")
	cmd.Flags().StringVar(&quota, "quota", "", "new quota in bytes, or 'unlimited'")
	cmd.Flags().BoolVar(&freeze, "freeze", false, "STOP this mailbox sending: submissions refused permanently and queued mail bounced at the relay (system key required)")
	cmd.Flags().BoolVar(&unfreeze, "unfreeze", false, "allow this mailbox to send again (system key required)")
	cmd.Flags().BoolVar(&pause, "pause", false, "HOLD this mailbox's sending: submissions deferred and queued mail held, not bounced (system key required)")
	cmd.Flags().BoolVar(&resume, "resume", false, "lift this mailbox's send hold (system key required)")
	cmd.Flags().StringVar(&sendMsgs, "send-msgs-per-day", "", "distinct messages per rolling 24h: a number, 'unlimited', or 'default' to drop the override (system key required)")
	cmd.Flags().StringVar(&sendRcpts, "send-rcpts-per-day", "", "envelope recipients per rolling 24h: a number, 'unlimited', or 'default' to drop the override (system key required)")
	return cmd
}

// parseSendCapFlag parses a --send-*-per-day value into the three states core
// distinguishes: "default" → nil (drop the override, inherit the platform
// number), "unlimited" → 0, a non-negative integer → itself.
//
// "default" and "unlimited" are deliberately different words. On quota they
// would mean the same thing, which is exactly why the cap flags cannot reuse
// parseQuotaFlag: there, null IS unlimited; here, null is "whatever the
// platform says" and could be the tightest bound in play.
func parseSendCapFlag(s string) (*int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "", "default":
		return nil, nil
	case "unlimited", "none":
		var zero int64
		return &zero, nil
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("invalid cap %q: expected a non-negative number, 'unlimited', or 'default'", s)
	}
	return &n, nil
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

func newMailboxPurgeCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge <mailboxId>",
		Short: "Purge an already-soft-deleted mailbox now (waive the rest of the undo window)",
		Long: "Expedite a mailbox that was already soft-deleted: waive the rest of its 7-day undo " +
			"window and wipe it at the ~25 min straggler floor. The tombstone becomes non-restorable — " +
			"restore then fails. Irreversible. To purge at delete time instead, use `delete --purge`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id := args[0]
			if !yes && !confirmTyped(
				fmt.Sprintf("PURGE deleted mailbox %s — this waives the remaining undo window and is irreversible.", id), id) {
				return usageError(errors.New("aborted (type the mailbox id to confirm, or pass --yes)"))
			}
			res, err := client.PurgeMailbox(cmd.Context(), id)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Purged mailbox %s (not restorable — wiping shortly)", id)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
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
		{"Sending", fmtSendState(m.SendDisabled, m.SendPaused)},
	}
	// The caps are shown only when this mailbox overrides them. On the common
	// mailbox both read "platform default", which is two rows of noise saying
	// nothing — and would bury the Sending row that does say something.
	if m.SendMsgsPerDay != nil || m.SendRcptsPerDay != nil {
		rows = append(rows,
			[]string{"  msgs/day", fmtSendCap(m.SendMsgsPerDay)},
			[]string{"  rcpts/day", fmtSendCap(m.SendRcptsPerDay)},
		)
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
