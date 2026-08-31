package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newForwardingCmd manages verified forwarding.
//
// The one thing to understand before using it: naming a destination does not
// start forwarding. Core mails that address a code, and mail moves only once
// the code comes back — consent belongs to whoever receives the forwarded mail,
// not to whoever configured it. So `add` is step one of two, and every command
// that points something at a destination refuses an unproven one.
func newForwardingCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "forwarding",
		Aliases: []string{"forward"},
		Short:   "Forward mail to another address, with the recipient's consent",
		Long: "Send a copy of this mailbox's mail to an address elsewhere.\n\n" +
			"Adding a destination does NOT start forwarding. Core mails the address an\n" +
			"eight-character code, good for 24 hours, and nothing is forwarded until you\n" +
			"confirm it — the person who receives the mail is the one who consents to\n" +
			"receiving it. Two steps:\n\n" +
			"  openemail forwarding add me@elsewhere.example\n" +
			"  openemail forwarding verify me@elsewhere.example ABCD1234\n\n" +
			"Then point forward-everything at it:\n\n" +
			"  openemail forwarding all me@elsewhere.example\n\n" +
			"The recipient can stop it from their end at any time, using the disable link\n" +
			"core puts in the forwarded mail — such a destination reads as\n" +
			"`revoked_by_recipient` here, and re-proving it means a fresh code.\n\n" +
			"To stop temporarily, `pause` — it keeps the destination verified, so\n" +
			"`resume` costs no second ceremony. `off` gives up the setting entirely.",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newForwardingShowCmd(a),
		newForwardingAddCmd(a),
		newForwardingVerifyCmd(a),
		newForwardingRemoveCmd(a),
		newForwardingAllCmd(a),
		newForwardingPauseCmd(a, true),
		newForwardingPauseCmd(a, false),
		newForwardingOffCmd(a),
	)
	return cmd
}

// forwardingClient is the (client, mailbox) pair every subcommand opens with.
func (a *app) forwardingClient(cmd *cobra.Command) (*coreapi.Client, string, error) {
	client, err := a.authedClient()
	if err != nil {
		return nil, "", err
	}
	mbx, err := a.resolveMailbox(cmd.Context(), client, "")
	if err != nil {
		return nil, "", err
	}
	return client, mbx, nil
}

// resolveDestination accepts either a destination ULID or the address itself.
//
// Core keys every route on the id precisely so no address is path-encoded, but
// an address is what a person has in hand — so this looks one up rather than
// making them copy an id out of `show` first. An id is passed through
// unresolved: it is already the handle, and a lookup would only add a call and
// a way to fail.
func resolveDestination(cmd *cobra.Command, client *coreapi.Client, mailboxID, ref string) (string, error) {
	if !strings.Contains(ref, "@") {
		return ref, nil
	}
	fwd, err := client.GetForwarding(cmd.Context(), mailboxID)
	if err != nil {
		return "", err
	}
	want := strings.ToLower(strings.TrimSpace(ref))
	for _, d := range fwd.Destinations {
		if strings.ToLower(d.Address) == want {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("no forwarding destination for %q — add it first with `openemail forwarding add %s`", ref, ref)
}

func newForwardingShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show",
		Aliases: []string{"get", "list", "ls", "status"},
		Short:   "Show forwarding destinations and where mail is going",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			fwd, err := client.GetForwarding(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(fwd, func(w io.Writer) {
				all := fwd.ForwardAll
				switch {
				case all.DestinationID == nil:
					a.out.Msgf("Forwarding all mail: off")
				case all.Paused:
					a.out.Warnf("Forwarding all mail: PAUSED (was going to %s)", strOr(all.Address, "—"))
				default:
					a.out.Successf("Forwarding all mail to %s", strOr(all.Address, "—"))
				}
				a.out.Msgf("")
				if len(fwd.Destinations) == 0 {
					a.out.Msgf("no destinations yet — add one with `openemail forwarding add <address>`")
					return
				}
				rows := make([][]string, 0, len(fwd.Destinations))
				for _, d := range fwd.Destinations {
					rows = append(rows, []string{
						d.Address,
						forwardingStateLabel(d),
						fmtEpochPtr(d.VerifiedAt),
						d.ID,
					})
				}
				printTable(w, a.out, []string{"ADDRESS", "STATE", "VERIFIED", "ID"}, rows)
				if forwardingNeedsCode(fwd.Destinations) {
					a.out.Msgf("")
					a.out.Msgf("a destination awaiting its code forwards nothing — confirm it with")
					a.out.Msgf("  openemail forwarding verify <address> <code>")
				}
			})
			return nil
		},
	}
}

// forwardingStateLabel spells the state machine out. `pending` in particular is
// worth saying twice: it looks configured in a listing and forwards nothing.
func forwardingStateLabel(d coreapi.ForwardingDestination) string {
	switch d.State {
	case "verified":
		return "verified"
	case "revoked_by_recipient":
		return "stopped by recipient"
	case "pending":
		if d.CodeExpiresAt != nil {
			return "awaiting code (expires " + fmtEpochPtr(d.CodeExpiresAt) + ")"
		}
		return "awaiting code"
	default:
		return d.State
	}
}

func forwardingNeedsCode(dests []coreapi.ForwardingDestination) bool {
	for _, d := range dests {
		if d.State == "pending" {
			return true
		}
	}
	return false
}

func newForwardingAddCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "add <address>",
		Short: "Name a destination and mail it a confirmation code",
		Long: "Mails the address an eight-character code, good for 24 hours. Nothing is\n" +
			"forwarded until you confirm it with `openemail forwarding verify`.\n\n" +
			"Re-running this for an address that already exists mails a FRESH code rather\n" +
			"than refusing, so it is also how you resend an expired one and how you\n" +
			"re-prove a destination the recipient stopped.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			dest, err := client.AddForwardingDestination(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(dest, func(_ io.Writer) {
				// The address is masked in this answer, so echo the one the user
				// typed — theirs is the spelling they are waiting on mail at.
				a.out.Successf("code mailed to %s — it expires %s", args[0], fmtEpoch(dest.ExpiresAt))
				a.out.Msgf("")
				a.out.Msgf("confirm it with")
				a.out.Msgf("  openemail forwarding verify %s <code>", args[0])
			})
			return nil
		},
	}
}

func newForwardingVerifyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "verify <address|id> <code>",
		Aliases: []string{"confirm"},
		Short:   "Confirm a destination with the code that was mailed to it",
		Long: "Spends the code mailed to the destination. Five wrong codes end the ceremony —\n" +
			"after that the destination needs a fresh code from `openemail forwarding add`.\n\n" +
			"Confirming also invalidates every disable link previously mailed for this\n" +
			"destination.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			destID, err := resolveDestination(cmd, client, mbx, args[0])
			if err != nil {
				return err
			}
			dest, err := client.VerifyForwardingDestination(cmd.Context(), mbx, destID, args[1])
			if err != nil {
				return err
			}
			a.out.Emit(dest, func(_ io.Writer) {
				a.out.Successf("%s is verified", args[0])
				a.out.Msgf("")
				a.out.Msgf("it is not receiving anything yet — point forwarding at it with")
				a.out.Msgf("  openemail forwarding all %s", args[0])
			})
			return nil
		},
	}
}

func newForwardingRemoveCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <address|id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a destination",
		Long: "Removes the destination. If forwarding was pointed at it, that stops too — the\n" +
			"setting cannot outlive its target.\n\n" +
			"To stop forwarding while keeping the destination proven, use `pause` or `off`\n" +
			"instead; both leave it verified, so resuming costs no second ceremony.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			destID, err := resolveDestination(cmd, client, mbx, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove forwarding destination %s?", args[0])) {
				return silentExit(1)
			}
			if err := client.DeleteForwardingDestination(cmd.Context(), mbx, destID); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "id": destID}, func(_ io.Writer) {
				a.out.Successf("removed %s", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newForwardingAllCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "all <address|id>",
		Short: "Forward all mail to a verified destination",
		Long: "Sends a copy of everything this mailbox receives to the named destination,\n" +
			"which must already be verified — core refuses an unproven one.\n\n" +
			"Mail is still delivered here as well; this is a copy, not a redirect.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			destID, err := resolveDestination(cmd, client, mbx, args[0])
			if err != nil {
				return err
			}
			fwd, err := client.SetForwardAll(cmd.Context(), mbx, destID)
			if err != nil {
				return err
			}
			a.out.Emit(fwd, func(_ io.Writer) {
				a.out.Successf("forwarding all mail to %s", strOr(fwd.ForwardAll.Address, args[0]))
			})
			return nil
		},
	}
}

func newForwardingPauseCmd(a *app, pause bool) *cobra.Command {
	use, short, long := "resume", "Resume forwarding", ""
	if pause {
		use, short = "pause", "Pause forwarding without giving up the destination"
		long = "Holds forwarding. The destination stays verified, so `resume` needs no new\n" +
			"code — which is what makes this the right verb for a temporary stop and\n" +
			"`off` the right one for \"not any more\"."
	} else {
		long = "Starts forwarding again to the destination already configured. Never\n" +
			"re-verifies anything."
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			fwd, err := client.PauseForwardAll(cmd.Context(), mbx, pause)
			if err != nil {
				return err
			}
			a.out.Emit(fwd, func(_ io.Writer) {
				if pause {
					a.out.Successf("forwarding paused (destination %s stays verified)", strOr(fwd.ForwardAll.Address, "—"))
					return
				}
				a.out.Successf("forwarding resumed to %s", strOr(fwd.ForwardAll.Address, "—"))
			})
			return nil
		},
	}
}

func newForwardingOffCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "off",
		Aliases: []string{"clear", "stop"},
		Short:   "Stop forwarding all mail",
		Long: "Turns forward-everything off. The destination stays verified and listed, so\n" +
			"pointing at it again later needs no new code.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, mbx, err := a.forwardingClient(cmd)
			if err != nil {
				return err
			}
			if !yes && !confirm("Stop forwarding all mail?") {
				return silentExit(1)
			}
			fwd, err := client.ClearForwardAll(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(fwd, func(_ io.Writer) {
				a.out.Successf("forwarding is off")
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
