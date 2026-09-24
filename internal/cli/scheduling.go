package cli

import (
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newAdminSchedulingCmd is what support runs when someone says a calendar
// invitation never arrived: the mailbox's scheduling jobs still being tried,
// its latest finished ones, and who was not reached and why. Jobs live only in
// the mailbox's own calendar store, so this is the only place to see them.
func newAdminSchedulingCmd(a *app) *cobra.Command {
	var (
		uid   string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "scheduling <mailboxId|address>",
		Short: "Show a mailbox's calendar invitations still being sent, and the ones that failed",
		Long: "Reads one mailbox's calendar scheduling from its store (system-only): jobs still\n" +
			"running or retrying, with attempts, next try, deadline and progress; the latest\n" +
			"finished jobs; and the latest deliveries that failed, with the status the\n" +
			"organizer's calendar shows and the reason. --uid narrows all three to one event.\n" +
			"Failure rows are kept 14 days.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mailboxID, err := a.resolveMailbox(ctx, client, args[0])
			if err != nil {
				return err
			}
			data, err := client.GetMailboxScheduling(ctx, mailboxID, uid, limit)
			if err != nil {
				return err
			}
			a.out.Emit(data, func(w io.Writer) { renderMailboxScheduling(w, a.out, data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&uid, "uid", "", "only this event (its iCalendar UID)")
	cmd.Flags().IntVar(&limit, "limit", 0, "rows per list (1-200, default 50)")
	return cmd
}

func renderMailboxScheduling(w io.Writer, p *Printer, data *coreapi.MailboxScheduling) {
	dash := func(s *string) string { return strOr(s, "-") }
	when := func(sec *int64) string {
		if sec == nil {
			return "-"
		}
		return fmtEpoch(*sec)
	}
	progress := func(j coreapi.SchedulingJob) string {
		if j.Completed == nil || j.Recipients == nil {
			return "-"
		}
		return fmt.Sprintf("%d/%d", *j.Completed, *j.Recipients)
	}

	fmt.Fprintln(w, "PENDING")
	if len(data.Pending) == 0 {
		fmt.Fprintln(w, "  nothing pending")
	} else {
		rows := make([][]string, 0, len(data.Pending))
		for _, j := range data.Pending {
			rows = append(rows, []string{j.UID, fmt.Sprintf("%d", j.Attempts), progress(j), when(j.DueAt),
				fmtEpoch(j.ExpiresAt), dash(j.LastError)})
		}
		printTable(w, p, []string{"UID", "ATTEMPTS", "DONE", "NEXT TRY", "DEADLINE", "LAST ERROR"}, rows)
	}

	fmt.Fprintln(w, "\nRECENT")
	if len(data.Recent) == 0 {
		fmt.Fprintln(w, "  no finished jobs")
	} else {
		rows := make([][]string, 0, len(data.Recent))
		for _, j := range data.Recent {
			rows = append(rows, []string{j.UID, j.State, fmt.Sprintf("%d", j.Attempts), fmtEpoch(j.CreatedAt), dash(j.LastError)})
		}
		printTable(w, p, []string{"UID", "STATE", "ATTEMPTS", "ADMITTED", "LAST ERROR"}, rows)
	}

	fmt.Fprintln(w, "\nFAILURES")
	if len(data.Failures) == 0 {
		fmt.Fprintln(w, "  no failures in the last 14 days")
	} else {
		rows := make([][]string, 0, len(data.Failures))
		for _, f := range data.Failures {
			rows = append(rows, []string{fmtEpoch(f.CreatedAt), f.UID, f.Counterpart, f.Method, dash(f.Outcome),
				dash(f.Status), truncate(dash(f.Reason), 60)})
		}
		printTable(w, p, []string{"WHEN", "UID", "TO", "METHOD", "OUTCOME", "STATUS", "REASON"}, rows)
	}
}
