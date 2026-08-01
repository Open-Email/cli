package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newVacationCmd is the out-of-office auto-reply.
//
// Top-level rather than under `sieve`: core tags the route Sieve because the
// auto-reply IS the Sieve vacation extension, but `openemail sieve` manages
// filter SCRIPTS and this is not one. People look for "out of office".
func newVacationCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vacation",
		Aliases: []string{"ooo", "autoreply"},
		Short:   "Read and set the out-of-office auto-reply",
		Long: "The auto-reply core sends on your behalf while you are away. One reply per\n" +
			"correspondent per absence — a mailing list or a repeat sender is not answered\n" +
			"again and again.\n\n" +
			"The timing rule is worth knowing before you edit anything: turning the reply\n" +
			"ON starts a NEW absence, which makes every correspondent eligible for one\n" +
			"reply again. Editing the text while you are already away deliberately\n" +
			"re-notifies nobody. So `vacation set` is safe to run mid-absence, and\n" +
			"`vacation off` followed by `vacation on` is NOT the same thing — it re-arms\n" +
			"everyone who already heard from you.\n\n" +
			"Writes are compare-and-swap on the document's state, so two devices cannot\n" +
			"silently overwrite each other.",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newVacationShowCmd(a),
		newVacationSetCmd(a, vacationSet),
		newVacationSetCmd(a, vacationOn),
		newVacationOffCmd(a),
	)
	return cmd
}

// vacationContent are the message fields shared by `set` and `on`. Each is
// applied only when its flag was CHANGED, so an unmentioned field survives a
// write and an explicitly emptied one is cleared — the same convention the
// directory PATCH commands use for nullable fields.
type vacationContent struct {
	subject  string
	text     string
	textFile string
	html     string
	htmlFile string
	from     string
	to       string
}

func (v *vacationContent) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&v.subject, "subject", "", "reply subject (empty clears it)")
	f.StringVar(&v.text, "text", "", "plain-text reply body (empty clears it)")
	f.StringVar(&v.textFile, "text-file", "", "read the plain-text body from a file (- for stdin)")
	f.StringVar(&v.html, "html", "", "HTML reply body (empty clears it)")
	f.StringVar(&v.htmlFile, "html-file", "", "read the HTML body from a file (- for stdin)")
	f.StringVar(&v.from, "from", "", "start of the absence (RFC3339, YYYY-MM-DD, or unix seconds; empty clears it)")
	f.StringVar(&v.to, "to", "", "end of the absence (same formats; empty clears it)")
}

// apply folds the changed flags onto the document just read. It returns false
// when nothing was asked for, so a caller can refuse a pointless write rather
// than resetting anyone's reply period with a no-op PUT.
func (v *vacationContent) apply(cmd *cobra.Command, in *coreapi.VacationInput) (bool, error) {
	changed := false
	str := func(name, value string, dst **string) {
		if !cmd.Flags().Changed(name) {
			return
		}
		changed = true
		if value == "" {
			*dst = nil // an explicitly emptied flag clears the field
			return
		}
		*dst = &value
	}
	str("subject", v.subject, &in.Subject)
	// A body given by file wins over the inline flag, matching `compose`.
	text, err := vacationBody(cmd, "text", v.text, v.textFile)
	if err != nil {
		return false, err
	}
	if text != nil {
		changed = true
		in.TextBody = *text
	}
	html, err := vacationBody(cmd, "html", v.html, v.htmlFile)
	if err != nil {
		return false, err
	}
	if html != nil {
		changed = true
		in.HtmlBody = *html
	}
	for _, spec := range [...]struct {
		flag  string
		value string
		dst   **string
	}{{"from", v.from, &in.FromDate}, {"to", v.to, &in.ToDate}} {
		if !cmd.Flags().Changed(spec.flag) {
			continue
		}
		changed = true
		if spec.value == "" {
			*spec.dst = nil
			continue
		}
		when, derr := parseVacationDate(spec.value)
		if derr != nil {
			return false, usageError(fmt.Errorf("--%s: %w", spec.flag, derr))
		}
		*spec.dst = &when
	}
	return changed, nil
}

// vacationBody resolves one body from its inline flag or its file. The outer
// pointer distinguishes "not asked for" from "asked for, and here is the value
// (possibly nil, meaning clear it)".
func vacationBody(cmd *cobra.Command, name, inline, file string) (**string, error) {
	if file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, err
		}
		s := string(data)
		p := &s
		return &p, nil
	}
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	if inline == "" {
		var p *string
		return &p, nil
	}
	p := &inline
	return &p, nil
}

// parseVacationDate normalizes to the exact UTC instant the schema's pattern
// requires. It shares the absolute forms with search's parser but REFUSES the
// relative one: `7d` means "seven days AGO" there, which is nonsense as the
// start of an absence and would silently backdate it.
func parseVacationDate(s string) (string, error) {
	if searchRelative.MatchString(s) {
		return "", fmt.Errorf("relative dates like %q are not accepted here (they would mean the PAST) — give a date", s)
	}
	when, err := parseSearchDate(s)
	if err != nil {
		return "", err
	}
	return when, nil
}

func newVacationShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show",
		Aliases: []string{"get", "status"},
		Short:   "Show the current auto-reply and whether it is on",
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
			v, err := client.GetVacation(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(v, func(w io.Writer) {
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Enabled", boolYN(v.IsEnabled)},
					{"From", strOr(v.FromDate, "— (no start)")},
					{"Until", strOr(v.ToDate, "— (no end)")},
					{"Subject", strOr(v.Subject, "—")},
					{"State", v.State},
				})
				if body := strOr(v.TextBody, ""); body != "" {
					a.out.Msgf("")
					a.out.Msgf("%s", body)
				}
				if v.HtmlBody != nil && *v.HtmlBody != "" {
					a.out.Msgf("")
					a.out.Msgf("(also has an HTML body, %d bytes)", len(*v.HtmlBody))
				}
				if !v.IsEnabled {
					a.out.Msgf("")
					a.out.Msgf("not replying to anyone — turn it on with `openemail vacation on`")
				}
			})
			return nil
		},
	}
}

// vacationMode distinguishes the two writing verbs, which differ ONLY in what
// they do to isEnabled.
type vacationMode int

const (
	vacationSet vacationMode = iota // preserve isEnabled
	vacationOn                      // enable
)

func newVacationSetCmd(a *app, mode vacationMode) *cobra.Command {
	var (
		content vacationContent
		ifMatch string
		force   bool
	)
	use, short, long := "set", "", ""
	switch mode {
	case vacationOn:
		use = "on"
		short = "Turn the auto-reply on (optionally setting its text in the same call)"
		long = "Starts a new absence. Every correspondent becomes eligible for one reply\n" +
			"again — including people who already got one during a previous absence.\n\n" +
			"  openemail vacation on --subject 'Away until the 15th' --text 'Back on the 15th.'\n\n" +
			"To change the wording while already away WITHOUT re-arming anyone, use\n" +
			"`openemail vacation set` instead."
	default:
		short = "Edit the auto-reply without changing whether it is on"
		long = "Updates the message, and only the parts you name — an unmentioned field keeps\n" +
			"its current value, and a flag given empty clears that field.\n\n" +
			"  openemail vacation set --text 'Back on the 20th, not the 15th.'\n" +
			"  openemail vacation set --to ''            # drop the end date\n\n" +
			"Editing while you are already away re-notifies nobody, which is the point of\n" +
			"having this separate from `vacation on`."
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: long + "\n\nThe write is compare-and-swap on the document's state, so a change made from\n" +
			"another device since you read it is refused rather than overwritten.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if force && ifMatch != "" {
				return usageError(errors.New("--force and --if-match are opposites — pass one"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			cur, err := client.GetVacation(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			in := coreapi.VacationInput{
				IsEnabled: cur.IsEnabled,
				FromDate:  cur.FromDate, ToDate: cur.ToDate,
				Subject: cur.Subject, TextBody: cur.TextBody, HtmlBody: cur.HtmlBody,
			}
			changed, err := content.apply(cmd, &in)
			if err != nil {
				return err
			}
			if mode == vacationOn {
				// Enabling is itself the change, even with no content flags.
				changed = changed || !in.IsEnabled
				in.IsEnabled = true
			}
			if !changed {
				return usageError(errors.New("nothing to change — pass --subject/--text/--html/--from/--to"))
			}
			if in.IsEnabled && in.TextBody == nil && in.HtmlBody == nil {
				return usageError(errors.New("an enabled auto-reply needs a body — pass --text or --html"))
			}

			guard := ifMatch
			if guard == "" && !force {
				guard = cur.State
			}
			v, err := client.PutVacation(cmd.Context(), mbx, in, guard)
			if err != nil {
				return vacationWriteError(a, err)
			}
			a.out.Emit(v, func(w io.Writer) {
				if v.IsEnabled {
					a.out.Successf("Auto-reply is ON%s", vacationWindow(v))
					if mode == vacationOn {
						a.out.Msgf("everyone who writes gets one reply; a new absence re-arms people who already had one")
					}
					return
				}
				a.out.Successf("Auto-reply updated (still off)")
			})
			return nil
		},
	}
	content.register(cmd)
	cmd.Flags().StringVar(&ifMatch, "if-match", "", "write only if the stored state matches this token")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite whatever is stored (no compare-and-swap)")
	return cmd
}

func newVacationOffCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "off",
		Short: "Turn the auto-reply off (keeping its text for next time)",
		Long: "Stops replying. The subject, body and dates are kept, so `vacation on` later\n" +
			"restores the same message — but note that turning it on again starts a NEW\n" +
			"absence and re-arms every correspondent for another reply.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			cur, err := client.GetVacation(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			if !cur.IsEnabled {
				a.out.Emit(cur, func(w io.Writer) {
					a.out.Msgf("Auto-reply is already off")
				})
				return nil
			}
			guard := cur.State
			if force {
				guard = ""
			}
			v, err := client.PutVacation(cmd.Context(), mbx, coreapi.VacationInput{
				IsEnabled: false,
				FromDate:  cur.FromDate, ToDate: cur.ToDate,
				Subject: cur.Subject, TextBody: cur.TextBody, HtmlBody: cur.HtmlBody,
			}, guard)
			if err != nil {
				return vacationWriteError(a, err)
			}
			a.out.Emit(v, func(w io.Writer) {
				a.out.Successf("Auto-reply is OFF — the message is kept for next time")
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite whatever is stored (no compare-and-swap)")
	return cmd
}

// vacationWriteError adds the remedy for the two refusals a user can act on.
func vacationWriteError(a *app, err error) error {
	if ae, ok := coreapi.AsAPIError(err); ok {
		switch {
		case ae.Status == 412:
			a.out.Warnf("the auto-reply changed since you read it — re-run to apply on top of the new version, or pass --force")
		case ae.Code == "invalid_window":
			a.out.Warnf("--from must be before --to")
		}
	}
	return err
}

// vacationWindow renders the absence bounds for the success line, so a user who
// set dates sees them echoed rather than trusting they landed.
func vacationWindow(v *coreapi.Vacation) string {
	switch {
	case v.FromDate != nil && v.ToDate != nil:
		return " from " + *v.FromDate + " until " + *v.ToDate
	case v.FromDate != nil:
		return " from " + *v.FromDate
	case v.ToDate != nil:
		return " until " + *v.ToDate
	}
	return " (no date window — it replies until you turn it off)"
}
