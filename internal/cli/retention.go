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

// Time-based retention (core's docs/store-capacity-design.md Part B): delete
// old mail on a schedule. Two scopes from one implementation — `mailboxes
// retention …` (the mailbox's OWN window, -m) and `accounts retention …` (the
// account DEFAULT, applied to every mailbox without a window of its own,
// EXISTING mailboxes included).
//
// The first thing on the platform that destroys live mail by itself, so the
// commands lean on core's rails rather than adding any: `get --days` previews
// what a window WOULD expunge before it is set (counts core measured), core's
// floor comes back as `retention_too_short` with the number in it, and what a
// window moves is restorable from trash for 14 days. A window is typed as a
// positional integer and refused as a usage error before any credential is
// consulted; `clear` confirms.

type retentionScope string

const (
	retentionMailbox retentionScope = "mailbox"
	retentionAccount retentionScope = "account"
)

// maxPreviewWindows mirrors core's bound on `?days=` (at most 8 windows).
const maxPreviewWindows = 8

// parseRetentionDays parses one window: a positive whole number of days.
func parseRetentionDays(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0, usageError(fmt.Errorf("%q is not a number of days (a positive whole number)", raw))
	}
	return n, nil
}

// parsePreviewDays parses `--days 30,90,365` into the preview ladder.
func parsePreviewDays(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxPreviewWindows {
		return nil, usageError(fmt.Errorf("--days names %d windows; core previews at most %d per call", len(parts), maxPreviewWindows))
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := parseRetentionDays(p)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s retentionScope) target(ctx context.Context, a *app, client *coreapi.Client, flagAccount string) (string, error) {
	if s == retentionAccount {
		id, _, err := suppressionScope(ctx, a, client, flagAccount)
		return id, err
	}
	return a.resolveMailbox(ctx, client, "")
}

func newRetentionCmd(a *app, scope retentionScope) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Delete old mail on a schedule: the time-based retention window",
	}
	if scope == retentionMailbox {
		cmd.Long = "The mailbox's OWN retention window in days. Live mail older than it is moved\n" +
			"to trash on the mailbox's own schedule — aged by when it ARRIVED, never by the\n" +
			"date inside the message — where it stays restorable for 14 days before the\n" +
			"purge frees the space. Off by default; with no window of its own the account\n" +
			"default applies, if one is set.\n\n" +
			"Preview before you enable: `retention get --days 30,90,365` reports what each\n" +
			"window would move to trash. To recover from a wrong window, clear or raise it\n" +
			"FIRST, then restore — a message restored under the same window is due again at\n" +
			"once. Writes take the account key; an app password may only read."
		addMailboxFlag(cmd, a)
	} else {
		cmd.Long = "The account DEFAULT retention window in days, applied to every mailbox the\n" +
			"account owns that has no window of its own — EXISTING mailboxes included, since\n" +
			"core reads it at resolution time rather than copying it at create. Preview it\n" +
			"first: `retention get --days 90` reports, per mailbox, what that default would\n" +
			"move to trash. Mail moved there is restorable for 14 days."
	}
	cmd.AddCommand(
		newRetentionGetCmd(a, scope),
		newRetentionSetCmd(a, scope),
		newRetentionClearCmd(a, scope),
	)
	return cmd
}

func fmtWindow(days *int64) string {
	if days == nil {
		return "off"
	}
	return fmt.Sprintf("%d days", *days)
}

func printRetention(a *app, r *coreapi.Retention) {
	a.out.Emit(r, func(w io.Writer) {
		inForce := fmtWindow(r.RetentionDays)
		switch r.Source {
		case "own":
			inForce += " — set on this mailbox"
		case "account":
			inForce += " — the account default"
		default:
			inForce = "off — mail is kept until someone deletes it"
		}
		printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
			{"Window in force", inForce},
			{"Own window", fmtWindow(r.OwnRetentionDays)},
			{"Account default", fmtWindow(r.AccountRetentionDays)},
			{"Floor", fmt.Sprintf("%d days", r.MinDays)},
			{"Oldest message", fmtEpochPtr(r.OldestReceivedAt)},
			{"Next run", fmtEpochPtr(r.NextRunAt)},
		})
		if len(r.Previews) > 0 {
			a.out.Msgf("")
			rows := make([][]string, 0, len(r.Previews))
			for _, p := range r.Previews {
				rows = append(rows, []string{fmt.Sprintf("%d days", p.Days), fmt.Sprint(p.Messages), fmtBytes(p.Bytes)})
			}
			printTable(w, a.out, []string{"WINDOW", "WOULD MOVE TO TRASH", "SIZE"}, rows)
		}
	})
}

func printAccountRetention(a *app, r *coreapi.AccountRetention) {
	a.out.Emit(r, func(w io.Writer) {
		printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
			{"Account default", fmtWindow(r.RetentionDays)},
			{"Floor", fmt.Sprintf("%d days", r.MinDays)},
		})
		a.out.Msgf("")
		rows := make([][]string, 0, len(r.Mailboxes))
		for _, m := range r.Mailboxes {
			inForce := fmtWindow(m.EffectiveDays)
			if m.Source == "account" {
				inForce += " (default)"
			}
			preview := "—"
			if m.Preview != nil {
				preview = fmt.Sprintf("%d messages (%s) at %d days", m.Preview.Messages, fmtBytes(m.Preview.Bytes), m.Preview.Days)
			}
			own := "—"
			if m.RetentionDays != nil {
				own = fmtWindow(m.RetentionDays)
			}
			rows = append(rows, []string{strOr(m.PrimaryAddress, m.ID), own, inForce, preview})
		}
		printTable(w, a.out, []string{"MAILBOX", "OWN", "IN FORCE", "WOULD MOVE TO TRASH"}, rows)
		if len(r.Unreadable) > 0 {
			a.out.Msgf("")
			a.out.Warnf("%d mailbox(es) could not be read and are left out rather than shown as zero: %s",
				len(r.Unreadable), strings.Join(r.Unreadable, ", "))
		}
		if r.NextCursor != "" {
			a.out.Msgf("")
			a.out.Msgf("more mailboxes: pass --cursor %s", r.NextCursor)
		}
	})
}

func newRetentionGetCmd(a *app, scope retentionScope) *cobra.Command {
	var (
		days        string
		flagAccount string
		limit       int
		cursor      string
	)
	cmd := &cobra.Command{
		Use:     "get",
		Aliases: []string{"show", "status", "preview"},
		Short:   "Show the window in force, and preview what a window would move to trash",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if scope == retentionAccount {
				var window *int
				if days != "" {
					n, err := parseRetentionDays(days)
					if err != nil {
						return err
					}
					window = &n
				}
				id, err := scope.target(cmd.Context(), a, client, flagAccount)
				if err != nil {
					return err
				}
				view, err := client.GetAccountRetention(cmd.Context(), id, window, limit, cursor)
				if err != nil {
					return err
				}
				printAccountRetention(a, view)
				return nil
			}
			ladder, err := parsePreviewDays(days)
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, "")
			if err != nil {
				return err
			}
			view, err := client.GetMailboxRetention(cmd.Context(), id, ladder)
			if err != nil {
				return err
			}
			printRetention(a, view)
			return nil
		},
	}
	if scope == retentionAccount {
		cmd.Flags().StringVar(&days, "days", "", "preview ONE window on every mailbox, e.g. the default you are about to set")
		cmd.Flags().IntVar(&limit, "limit", 0, "mailboxes per page")
		cmd.Flags().StringVar(&cursor, "cursor", "", "continue from a previous page")
		accountFlag(cmd, &flagAccount)
	} else {
		cmd.Flags().StringVar(&days, "days", "", "windows to preview, comma-separated (at most 8), e.g. 30,90,365")
	}
	return cmd
}

func newRetentionSetCmd(a *app, scope retentionScope) *cobra.Command {
	var flagAccount string
	cmd := &cobra.Command{
		Use:     "set <days>",
		Aliases: []string{"put"},
		Short:   "Set the window in days (refused below the platform floor)",
		Long: "Sets the window. Mail older than it is moved to trash on the next wake and\n" +
			"stays restorable there for 14 days. Refused below the platform floor with\n" +
			"`retention_too_short` — the floor is on `retention get`. The answer includes a\n" +
			"preview of what the window will move to trash: read it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A usage error before authentication: a window that is not a number
			// is wrong whatever credentials the caller holds.
			n, err := parseRetentionDays(args[0])
			if err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, flagAccount)
			if err != nil {
				return err
			}
			if scope == retentionAccount {
				view, err := client.SetAccountRetention(cmd.Context(), id, n)
				if err != nil {
					return err
				}
				printAccountRetention(a, view)
				a.out.Msgf("")
				a.out.Msgf("pushed to every mailbox without a window of its own by a background walk; mail moved to trash is restorable for 14 days")
				return nil
			}
			view, err := client.SetMailboxRetention(cmd.Context(), id, n)
			if err != nil {
				return err
			}
			printRetention(a, view)
			a.out.Msgf("")
			a.out.Msgf("older mail moves to trash on the mailbox's next wake and is restorable for 14 days; to undo a wrong window, clear or raise it FIRST, then restore")
			return nil
		},
	}
	if scope == retentionAccount {
		accountFlag(cmd, &flagAccount)
	}
	return cmd
}

func newRetentionClearCmd(a *app, scope retentionScope) *cobra.Command {
	var (
		yes         bool
		flagAccount string
	)
	prompt := "Clear this mailbox's retention window? The account default, if any, applies instead. Nothing already in trash is restored."
	if scope == retentionAccount {
		prompt = "Clear the account's default retention window? Mailboxes with a window of their own keep it; every other mailbox stops deleting by age. Nothing already in trash is restored."
	}
	cmd := &cobra.Command{
		Use:     "clear",
		Aliases: []string{"delete", "remove", "off"},
		Short:   "Clear the window (the answer says which window is now in force)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(prompt) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := scope.target(cmd.Context(), a, client, flagAccount)
			if err != nil {
				return err
			}
			if scope == retentionAccount {
				view, err := client.ClearAccountRetention(cmd.Context(), id)
				if err != nil {
					return err
				}
				printAccountRetention(a, view)
				return nil
			}
			view, err := client.ClearMailboxRetention(cmd.Context(), id)
			if err != nil {
				return err
			}
			printRetention(a, view)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	if scope == retentionAccount {
		accountFlag(cmd, &flagAccount)
	}
	return cmd
}
