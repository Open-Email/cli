package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newLabelsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "labels",
		Aliases: []string{"label"},
		Short:   "Manage a mailbox's labels (IMAP folders)",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newLabelListCmd(a),
		newLabelCreateCmd(a),
		newLabelRenameCmd(a),
		newLabelDeleteCmd(a),
		newLabelMessagesCmd(a),
		newLabelUidsCmd(a),
		newLabelExpungeCmd(a),
	)
	return cmd
}

func newLabelListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List labels with counts, uidValidity, uidNext",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			labels, err := client.ListLabels(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"labels": labels}, func(w io.Writer) {
				rows := make([][]string, 0, len(labels))
				for _, l := range labels {
					kind := "user"
					if l.Role != nil {
						kind = "system:" + *l.Role
					}
					rows = append(rows, []string{
						l.Name, kind,
						fmt.Sprintf("%d", l.MessageCount), fmt.Sprintf("%d", l.UnseenCount),
						fmt.Sprintf("%d", l.UIDValidity), fmt.Sprintf("%d", l.UIDNext),
					})
				}
				printTable(w, a.out, []string{"NAME", "KIND", "MSGS", "UNSEEN", "UIDVALIDITY", "UIDNEXT"}, rows)
			})
			return nil
		},
	}
}

func newLabelCreateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a user label",
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
			l, err := client.CreateLabel(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(l, func(w io.Writer) {
				a.out.Successf("Created label %q (uidValidity %d)", l.Name, l.UIDValidity)
			})
			return nil
		},
	}
}

func newLabelRenameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <label> <newName>",
		Short: "Rename a user label (UIDs preserved; system labels cannot be renamed)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.RenameLabel(cmd.Context(), mbx, args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"renamed": true, "from": args[0], "to": args[1]}, func(w io.Writer) {
				a.out.Successf("Renamed label %q → %q", args[0], args[1])
			})
			return nil
		},
	}
}

func newLabelDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <label>",
		Aliases: []string{"rm"},
		Short:   "Delete a user label (messages keep their other labels; system labels cannot be deleted)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete label %q?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.DeleteLabel(cmd.Context(), mbx, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "label": args[0]}, func(w io.Writer) {
				a.out.Successf("Deleted label %q", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newLabelMessagesCmd(a *app) *cobra.Command {
	var uidMin, uidMax, limit int64
	cmd := &cobra.Command{
		Use:   "messages <label>",
		Short: "List a label's messages UID-ascending (optionally a UID range)",
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
			msgs, uidValidity, err := client.LabelMessages(cmd.Context(), mbx, args[0], uidMin, uidMax, limit)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"messages": msgs, "uidValidity": uidValidity}, func(w io.Writer) {
				rows := make([][]string, 0, len(msgs))
				for _, m := range msgs {
					uid := "—"
					if m.UID != nil {
						uid = fmt.Sprintf("%d", *m.UID)
					}
					rows = append(rows, []string{
						uid, m.ID, truncate(m.EnvelopeFrom, 26),
						truncate(msgSubjectDisplay(m), 38), fmtEpoch(m.ReceivedAt), flagsDisplay(m.Flags),
					})
				}
				printTable(w, a.out, []string{"UID", "ID", "FROM", "SUBJECT", "DATE", "FLAGS"}, rows)
				if len(msgs) > 0 && (limit == 0 || int64(len(msgs)) == limit) {
					if last := msgs[len(msgs)-1].UID; last != nil {
						a.out.Msgf("more may follow — re-run with --uid-min %d", *last+1)
					}
				}
			})
			return nil
		},
	}
	cmd.Flags().Int64Var(&uidMin, "uid-min", 0, "lowest UID to include (inclusive)")
	cmd.Flags().Int64Var(&uidMax, "uid-max", 0, "highest UID to include (inclusive)")
	cmd.Flags().Int64Var(&limit, "limit", 0, "max messages to return (1–1000)")
	return cmd
}

func newLabelUidsCmd(a *app) *cobra.Command {
	var uidMin, uidMax, limit int64
	cmd := &cobra.Command{
		Use:   "uids <label>",
		Short: "List a label's UID index (uid, modseq, flags, size, date) — the folder-sync projection, up to 10000 a page",
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
			rows, uidValidity, err := client.LabelUids(cmd.Context(), mbx, args[0], uidMin, uidMax, limit)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"messages": rows, "uidValidity": uidValidity}, func(w io.Writer) {
				table := make([][]string, 0, len(rows))
				for _, m := range rows {
					flags := m.Flags
					if len(m.Keywords) > 0 {
						flags = append(append([]string{}, m.Flags...), m.Keywords...)
					}
					table = append(table, []string{
						fmt.Sprintf("%d", m.UID), m.ID, fmt.Sprintf("%d", m.ModSeq),
						fmtBytes(m.Size), fmtEpoch(m.ReceivedAt), flagsDisplay(flags),
					})
				}
				printTable(w, a.out, []string{"UID", "ID", "MODSEQ", "SIZE", "DATE", "FLAGS"}, table)
				if len(rows) > 0 && (limit == 0 || int64(len(rows)) == limit) {
					a.out.Msgf("more may follow — re-run with --uid-min %d", rows[len(rows)-1].UID+1)
				}
			})
			return nil
		},
	}
	cmd.Flags().Int64Var(&uidMin, "uid-min", 0, "lowest UID to include (inclusive)")
	cmd.Flags().Int64Var(&uidMax, "uid-max", 0, "highest UID to include (inclusive)")
	cmd.Flags().Int64Var(&limit, "limit", 0, "max rows to return (1–10000)")
	return cmd
}

func newLabelExpungeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "expunge <label>",
		Short: "Detach \\Deleted-flagged messages from a label (IMAP EXPUNGE)",
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
			expunged, count, err := client.ExpungeLabel(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"expunged": expunged, "messagesExpunged": count}, func(w io.Writer) {
				a.out.Successf("Expunged %d UID(s) from %q; %d message(s) moved to trash", len(expunged), args[0], count)
			})
			return nil
		},
	}
}
