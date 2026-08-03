package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
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
		newInvitationShowCmd(a),
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
				printTable(w, a.out, []string{"FIELD", "VALUE"}, invitationStatusRows(st))
				a.out.Msgf("%s", invitationStatusNextStep(st))
			})
			return nil
		},
	}
}

// invitationStatusRows renders the filed copy's standing.
//
// Organizer and "Yours to change" are the pair that answers the question the
// probe exists for: is this object mine to edit? Neither can be computed here.
// Core resolves `amOrganizer` through the directory's identity list AND the full
// routing ladder — the same predicate the iTIP fan-out gate applies — while the
// CLI knows only the address it logged in with, so an event or task organized
// from an alias would read as somebody else's.
func invitationStatusRows(st *coreapi.PimInvitationStatus) [][]string {
	return [][]string{
		{"Filed", "yes"},
		{"Calendar", strOr(st.CalendarID, "—")},
		{"Href", strOr(st.Href, "—")},
		{"Organizer", strings.TrimPrefix(strOr(st.Organizer, "—"), "mailto:")},
		{"Yours to change", boolYN(st.AmOrganizer)},
		{"Your address", strOr(st.MyAddress, "— (not an attendee)")},
		{"Your reply", strOr(st.MyPartstat, "— (none yet)")},
	}
}

// invitationStatusNextStep turns the organizer/attendee split into the one
// command that applies. The respond endpoint refuses an organizer outright
// (403 is_organizer — filing an ICS naming you as organizer would schedule iTIP
// on your behalf), so pointing them at RSVP sends them into a refusal.
func invitationStatusNextStep(st *coreapi.PimInvitationStatus) string {
	switch {
	case st.AmOrganizer:
		return "you organize this — change it with `openemail calendars objects put` " +
			"(or `openemail calendars tasks set` for a to-do); RSVP is refused for organizers"
	case st.MyAddress == nil:
		return "you are not an attendee of this — nothing to answer"
	default:
		return "answer it with `openemail calendars respond <calendar> <href> <partstat>`"
	}
}

// newInvitationShowCmd reads the invitation a message carries. It is the call
// behind a mail client's RSVP banner: core locates the scheduling part, parses
// the iCalendar, and merges in what this mailbox has already filed and answered
// — so nothing here interprets RFC 5545, and the times arrive already resolved
// against the event's timezone.
func newInvitationShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show <messageId>",
		Aliases: []string{"get"},
		Short:   "Show the invitation a message carries, and whether you can answer it",
		Long: "Reads the scheduling part of a received message: the event, who is invited,\n" +
			"which of your addresses it names, and what you have answered so far.\n\n" +
			"The line that matters before you reply is \"can answer\". It is the respond\n" +
			"endpoint's own verdict, not a guess — an organizer, or a message that is\n" +
			"itself a reply or a cancellation, cannot be answered from here.\n\n" +
			"A message with no scheduling part is reported plainly and exits 0: ordinary\n" +
			"mail is not an error.",
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
			inv, err := client.GetMessageInvitation(cmd.Context(), mbx, args[0])
			// `no_invitation` is the ordinary answer for ordinary mail, so it is
			// a result rather than a failure.
			if coreapi.IsNotFound(err) {
				a.out.Emit(map[string]any{"messageId": args[0], "invitation": nil}, func(w io.Writer) {
					a.out.Msgf("%s carries no invitation", args[0])
				})
				return nil
			}
			if err != nil {
				return err
			}
			a.out.Emit(inv, func(w io.Writer) {
				rows := [][]string{
					{invitationSubject(inv), strOr(inv.Summary, "(no summary)")},
					{invitationWhenLabel(inv), invitationWhen(inv)},
					{"Where", strOr(inv.Location, "—")},
					{"Organizer", strOr(inv.Organizer, "—")},
					{"Method", strOr(inv.Method, "—")},
					{"Component", strOr(inv.Component, "—")},
					{"UID", strOr(inv.UID, "—")},
					{"Section", inv.Section},
					{"Attendees", fmt.Sprintf("%d", inv.AttendeeCount)},
					{"Your address", strOr(inv.MyAddress, "— (you are not an attendee)")},
					{"Your reply", strOr(inv.MyPartstat, "not answered")},
					{"In your calendar", boolYN(inv.Found)},
					{"Can answer", boolYN(inv.CanRespond)},
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
				if inv.CanRespond {
					a.out.Msgf("answer it with `openemail calendars invitations respond <partstat> --message %s`", args[0])
					return
				}
				a.out.Warnf("%s", invitationRefusal(inv))
			})
			return nil
		},
	}
}

// invitationSubject names what the message is about. Core schedules to-dos as
// well as events (RFC 5546 §3.4 defines REQUEST/REPLY for a VTODO, and this
// surface's SCHEDULABLE set is {VEVENT, VTODO}), so a mailed to-do reaches
// here — calling it an "Event" makes the row a lie. An absent component keeps
// the pre-existing wording.
func invitationSubject(inv *coreapi.MessageInvitation) string {
	if inv.Component != nil && strings.EqualFold(*inv.Component, "VTODO") {
		return "Task"
	}
	return "Event"
}

// invitationWhenLabel: a to-do's one timestamp is a DEADLINE, not a meeting
// time, so labelling it "When" reads as when to show up.
func invitationWhenLabel(inv *coreapi.MessageInvitation) string {
	if invitationSubject(inv) == "Task" {
		return "Due"
	}
	return "When"
}

// invitationWhen renders the window core already resolved — epoch seconds with
// the event's timezone applied, so there is no VTIMEZONE to interpret here.
//
// A to-do is the one shape with an end and no start: core puts DUE in `dtend`,
// and RFC 5545 §3.6.2 makes DTSTART optional, so the commonest task ("buy milk
// by Friday") carries a deadline alone. Requiring a start would render its only
// date as a dash. An EVENT with no start stays a dash — core does not produce
// one, and inventing a start from an end would be worse than saying nothing.
func invitationWhen(inv *coreapi.MessageInvitation) string {
	if inv.DTStart == nil {
		if inv.DTEnd != nil && invitationSubject(inv) == "Task" {
			return fmtEpoch(*inv.DTEnd)
		}
		return "—"
	}
	when := fmtEpoch(*inv.DTStart)
	if inv.DTEnd != nil {
		when += " → " + fmtEpoch(*inv.DTEnd)
	}
	if inv.RRule != nil {
		if *inv.RRule == "RDATE" {
			when += "  (repeats on set dates)"
		} else {
			when += "  (repeats: " + *inv.RRule + ")"
		}
	}
	return when
}

// invitationRefusal says WHICH of core's reasons applies, so a user who cannot
// answer knows whether to fix something or to go elsewhere.
func invitationRefusal(inv *coreapi.MessageInvitation) string {
	switch {
	case inv.IsOrganizer:
		return "you are the organizer — answer by editing the event in your own calendar (`openemail calendars objects put`)"
	case inv.MyAddress == nil:
		return "this invitation names none of your addresses as an attendee"
	case inv.Method != nil && *inv.Method == "REPLY":
		return "this message is someone else's reply, not an invitation — there is nothing to answer"
	case inv.Method != nil && *inv.Method == "CANCEL":
		return "this message cancels the event — there is nothing to answer"
	default:
		return "this invitation cannot be answered from here"
	}
}

func newInvitationRespondCmd(a *app) *cobra.Command {
	var file, message, section string
	cmd := &cobra.Command{
		Use:     "respond <accepted|declined|tentative|needs-action>",
		Aliases: []string{"rsvp"},
		Short:   "Answer an invitation that arrived by email (files it if new, tells the organizer)",
		Long: "Answer an invitation you received by email. Core files an attendee copy into\n" +
			"your default calendar if the event is not stored yet, records the reply, and\n" +
			"tells the organizer — patching their copy when they are local, mailing a\n" +
			"METHOD:REPLY when they are not.\n\n" +
			"  openemail calendars invitations respond accepted --message <messageId>\n\n" +
			"Name the message and the server reads the scheduling part itself; --section\n" +
			"pins a particular one, but omitted it picks the message's own. Prefer this to\n" +
			"piping the part in: the bytes are already on the server, so the inline form\n" +
			"only makes the client fetch them, decode the charset, and post them back.\n\n" +
			"For an .ics that is not in a mailbox at all, the inline form still applies:\n\n" +
			"  openemail calendars invitations respond accepted --file invite.ics\n\n" +
			"To re-answer an event already in a calendar, use `openemail calendars respond`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			partstat, err := normalizePartstat(args[0])
			if err != nil {
				return usageError(err)
			}
			if message != "" && file != "" {
				return usageError(errors.New("--message and --file name the same invitation two ways — pass one"))
			}
			if message == "" && section != "" {
				return usageError(errors.New("--section only applies to --message"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}

			// The inline form reads stdin when --file is absent, so it must stay
			// behind the --message check: reaching it with a message id would
			// block on a terminal forever rather than doing the obvious thing.
			var ics string
			if message == "" {
				raw, rerr := readRawInput(file)
				if rerr != nil {
					return rerr
				}
				ics = strings.TrimSpace(string(raw))
				if ics == "" {
					return usageError(errors.New("no calendar data on stdin — pass --message <id>, --file, or pipe the message part"))
				}
				if !strings.Contains(ics, "BEGIN:VCALENDAR") {
					return usageError(errors.New("that does not look like a text/calendar part (no BEGIN:VCALENDAR)"))
				}
			}

			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			var res *coreapi.PimRsvpResult
			if message != "" {
				res, err = client.RespondToMessageInvitation(cmd.Context(), mbx, message, section, partstat)
			} else {
				res, err = client.RespondToInvitation(cmd.Context(), mbx, ics, partstat)
			}
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
	cmd.Flags().StringVar(&message, "message", "", "answer the invitation carried by this message (preferred)")
	cmd.Flags().StringVar(&section, "section", "", "with --message: the scheduling part's section (default: the server picks it)")
	cmd.Flags().StringVar(&file, "file", "", "inline form: read the .ics from a file (default: stdin)")
	return cmd
}
