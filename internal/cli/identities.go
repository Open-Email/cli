package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newIdentitiesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "identities",
		Aliases: []string{"identity"},
		Short:   "Inspect identities — the durable owner of the mail and calendar/contact stores",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(newIdentityGetCmd(a))
	return cmd
}

func newIdentityGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get [identityId]",
		Short: "Show an identity with per-facet usage (mail store, PIM store)",
		Long: "Show an identity and which durable stores it owns. An identity's id is the\n" +
			"same ULID the mailbox API uses; a calendar-only identity has no mail facet\n" +
			"yet logs in and holds credentials. Defaults to -m / the profile default\n" +
			"mailbox, or — for an app-password login — the token's own identity.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			// Explicit arg > -m/profile default > the bearer's own identity ("me",
			// which only a mailbox principal has a referent for).
			ref := "me"
			switch {
			case len(args) == 1:
				ref = args[0]
			case a.flagMailbox != "" || a.profile.DefaultMailbox != "":
				ref, err = a.resolveMailbox(cmd.Context(), client, "")
				if err != nil {
					return err
				}
			}
			id, err := client.GetIdentity(cmd.Context(), ref)
			if err != nil {
				return err
			}
			a.out.Emit(id, func(w io.Writer) {
				rows := [][]string{
					{"ID", id.ID},
					{"Address", strOr(id.PrimaryAddress, "— (mail-less identity)")},
					{"Account", strOr(id.AccountID, "—")},
					{"Quota", fmtQuota(id.QuotaBytes)},
					{"Created", fmtEpoch(id.CreatedAt)},
					{"Sending", fmtSendState(id.SendDisabled)},
				}
				if id.SendMsgsPerDay != nil || id.SendRcptsPerDay != nil {
					rows = append(rows,
						[]string{"  msgs/day", fmtSendCap(id.SendMsgsPerDay)},
						[]string{"  rcpts/day", fmtSendCap(id.SendRcptsPerDay)},
					)
				}
				if m := id.Facets.Mail; m != nil {
					rows = append(rows,
						[]string{"Mail store", m.StoreID + provisionedNote(m.Provisioned)},
						[]string{"  messages", fmt.Sprintf("%d (%s)", m.MessageCount, fmtBytes(m.UsedBytes))},
						[]string{"  trash", fmt.Sprintf("%d (%s)", m.ExpungedCount, fmtBytes(m.ExpungedBytes))},
					)
				} else {
					rows = append(rows, []string{"Mail store", "— (not bound: cannot speak the mail protocols)"})
				}
				if p := id.Facets.Pim; p != nil {
					rows = append(rows,
						[]string{"PIM store", p.StoreID + provisionedNote(p.Provisioned)},
						[]string{"  collections", fmt.Sprintf("%d", p.Collections)},
						[]string{"  objects", fmt.Sprintf("%d (%s)", p.Objects, fmtBytes(p.Bytes))},
					)
				} else {
					rows = append(rows, []string{"PIM store", "— (not bound: no calendars or addressbooks)"})
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
			})
			return nil
		},
	}
}

func provisionedNote(provisioned bool) string {
	if provisioned {
		return ""
	}
	return " (bound, not yet provisioned)"
}
