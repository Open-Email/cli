package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newInvitationsCmd is a subgroup of `calendars`. Its children are VERBS, never
// a bare argument, so a UID can never shadow a subcommand name. The routes are
// mailbox-scoped rather than per-collection (core looks across every calendar),
// so this group takes no collection reference.
func newInvitationsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "invitations",
		Aliases: []string{"invites"},
		Short:   "Answer meeting invitations that arrived by email",
		Long: "Work with invitations as a mail client does: ask what the mailbox already\n" +
			"knows about an event, and answer one straight from the message part.\n\n" +
			"  openemail messages part <id> <section> | openemail calendars invitations respond accepted",
	}
	cmd.AddCommand(
		newInvitationStatusCmd(a),
		newInvitationRespondCmd(a),
	)
	return cmd
}

func newInvitationStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status <uid>",
		Short: "Is this event already in the mailbox, and what have I answered?",
		Long: "Look an event UID up across EVERY calendar in the mailbox (an auto-filed or\n" +
			"moved copy is found wherever it lives) and report the caller's current\n" +
			"participation status — what a mail client asks before drawing RSVP buttons.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.InvitationStatus(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(st, func(w io.Writer) {
				if !st.Found {
					a.out.Msgf("not filed in any calendar of this mailbox")
					if st.MyAddress != nil {
						a.out.Msgf("you are an attendee as %s — answer it with `calendars invitations respond`", *st.MyAddress)
					}
					return
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Filed", "yes"},
					{"Calendar", strOr(st.CalendarID, "—")},
					{"Href", strOr(st.Href, "—")},
					{"Your address", strOr(st.MyAddress, "— (not an attendee)")},
					{"Your reply", strOr(st.MyPartstat, "— (none yet)")},
				})
			})
			return nil
		},
	}
}

func newInvitationRespondCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:     "respond <accepted|declined|tentative|needs-action>",
		Aliases: []string{"rsvp"},
		Short:   "Answer an invitation from its raw .ics part (files it if new, tells the organizer)",
		Long: "Answer an invitation you received by email. Give it the raw text/calendar part\n" +
			"(--file, or stdin) and a reply: core files an attendee copy into your default\n" +
			"calendar if the event is not stored yet, records the reply, and tells the\n" +
			"organizer — patching their copy when they are local, mailing a METHOD:REPLY\n" +
			"when they are not.\n\n" +
			"  openemail messages part <id> 2 | openemail calendars invitations respond accepted\n\n" +
			"To re-answer an event already in a calendar, use `openemail calendars respond`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			partstat, err := normalizePartstat(args[0])
			if err != nil {
				return usageError(err)
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			raw, err := readRawInput(file)
			if err != nil {
				return err
			}
			ics := strings.TrimSpace(string(raw))
			if ics == "" {
				return usageError(errors.New("no calendar data on stdin — pass --file, or pipe the message part"))
			}
			if !strings.Contains(ics, "BEGIN:VCALENDAR") {
				return usageError(errors.New("that does not look like a text/calendar part (no BEGIN:VCALENDAR)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			res, err := client.RespondToInvitation(cmd.Context(), mbx, ics, partstat)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				what := "invitation"
				if res.Summary != nil && *res.Summary != "" {
					what = fmt.Sprintf("%q", *res.Summary)
				}
				a.out.Successf("RSVP %s to %s as %s", res.Partstat, what, res.MyAddress)
				if res.Filed {
					a.out.Msgf("filed a copy into calendar %s as %s", res.CalendarID, res.Href)
				} else {
					a.out.Msgf("updated your existing copy (%s in calendar %s)", res.Href, res.CalendarID)
				}
				switch {
				case res.OrganizerUpdated:
					a.out.Msgf("organizer's local copy updated")
				case res.ReplySent:
					a.out.Msgf("METHOD:REPLY mailed to the organizer")
				default:
					a.out.Msgf("organizer not notified (your copy is updated)")
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the .ics part from a file (default: stdin)")
	return cmd
}
