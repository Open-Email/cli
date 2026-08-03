package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/compose"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// The `tasks` group under `calendars`.
//
// There is deliberately NO top-level `tasks` tree. A to-do is a VTODO in a
// calendar: same collection, same href, same ETag, same sharing, same feed
// tokens, same sync diff, same iTIP scheduling. A parallel tree would duplicate
// every one of those for zero new capability, and a second spelling of the same
// endpoints is how two surfaces come to disagree about one object.
//
// What the generic commands genuinely cannot express is:
//
//   - a to-do's schedule is a DEADLINE. Core stores DUE in the `dtend` extract
//     and leaves `dtstart` empty for the commonest task ("buy milk"), so the
//     event table printed its due date under END and its start as a dash.
//   - completion lives in STATUS + PERCENT-COMPLETE + COMPLETED, none of which
//     the event columns showed — a finished task and an open one rendered
//     identically.
//   - marking one done, or moving its deadline, is a read-modify-write of a
//     JSCalendar Task. Through `objects put` that means authoring the JSON by
//     hand and re-sending the whole body, which drops every property the user's
//     editor did not reproduce.
//
// The document rules themselves live in internal/coreapi/pimtasks.go, shared
// with the console — so the two cannot disagree about what "done" means.

// openTasksOnly drops the finished to-dos. Core has no status query (STATUS is a
// stored extract, not a query dimension), so this runs over the rows that were
// fetched — which is why `--open` pairs with `--all` on anything but a small
// calendar.
func openTasksOnly(objs []coreapi.PimObject) []coreapi.PimObject {
	out := make([]coreapi.PimObject, 0, len(objs))
	for _, o := range objs {
		if coreapi.TaskIsClosed(o) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// printPimTasks renders to-dos under headers that are true of a to-do: `dtend`
// is the DUE date, and STATUS is the field that says whether it is done.
func printPimTasks(a *app, w io.Writer, objs []coreapi.PimObject) {
	rows := make([][]string, 0, len(objs))
	for _, o := range objs {
		rows = append(rows, []string{
			o.Href, fmtEpochPtr(o.Dtend), strOr(o.EventStatus, "—"),
			truncate(strings.TrimPrefix(derefOr(o.Organizer, "—"), "mailto:"), 28),
			boolYN(o.Rrule != nil), fmtEpoch(o.UpdatedAt),
		})
	}
	printTable(w, a.out, []string{"HREF", "DUE", "STATUS", "ORGANIZER", "RECURS", "UPDATED"}, rows)
}

// parseTaskDue turns a --due value into an instant plus the whole-day flag.
// It accepts the same spellings as --start/--end; a BARE DATE additionally means
// a DATE-valued DUE ("due that day"), which is what a DAV client writes and what
// core's `showWithoutTime` expresses — not midnight on it.
func parseTaskDue(raw string) (*coreapi.TaskDue, error) {
	trimmed := strings.TrimSpace(raw)
	if t, err := time.Parse("2006-01-02", trimmed); err == nil {
		return &coreapi.TaskDue{Unix: t.Unix(), AllDay: true}, nil
	}
	sec, err := parseTimeArg(trimmed)
	if err != nil {
		return nil, err
	}
	return &coreapi.TaskDue{Unix: sec}, nil
}

// taskEditsFromFlags reads the flag set into an edit intent. `Changed` is what
// separates "set to empty" (clear) from "not mentioned" (leave alone) — a
// distinction the zero value alone cannot carry.
func taskEditsFromFlags(cmd *cobra.Command, raw taskFlags) (coreapi.TaskEdits, error) {
	e := coreapi.TaskEdits{
		Title: raw.title, TitleSet: cmd.Flags().Changed("title"),
		Description: raw.description, DescSet: cmd.Flags().Changed("description"),
		Priority: raw.priority, PrioritySet: cmd.Flags().Changed("priority"),
		Percent: raw.percent, PercentSet: cmd.Flags().Changed("percent"),
		Progress: raw.progress, ProgressSet: cmd.Flags().Changed("progress"),
	}
	if cmd.Flags().Changed("due") {
		e.DueSet = true
		if strings.TrimSpace(raw.due) != "" {
			due, err := parseTaskDue(raw.due)
			if err != nil {
				return e, fmt.Errorf("--due: %w", err)
			}
			e.Due = due
		}
	}
	return e, nil
}

// taskFlags is the raw flag storage one command instance binds.
type taskFlags struct {
	title       string
	description string
	due         string
	priority    int
	percent     int
	progress    string
}

// ── commands ─────────────────────────────────────────────────────────────────

func newPimTasksCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tasks",
		Aliases: []string{"task", "todos", "todo"},
		Short:   "Work with a calendar's to-dos (VTODO) under task-shaped headers",
		Long: "To-dos live in calendars, exactly like events: same collection, same href,\n" +
			"same ETag, same iTIP scheduling. This group exists because a to-do's schedule\n" +
			"is a DEADLINE rather than a span — core stores DUE where an event keeps its\n" +
			"end, and most to-dos have no start at all — and because marking one done means\n" +
			"writing PERCENT-COMPLETE and COMPLETED alongside STATUS.\n\n" +
			"Everything else stays on `calendars`: sharing, feed tokens, sync, import,\n" +
			"export, and RSVP (`calendars respond`) all work on a to-do unchanged. Core\n" +
			"schedules to-dos too (RFC 5546 §3.4), so an assigned one arrives as mail and\n" +
			"answers to `calendars invitations`.",
	}
	cmd.AddCommand(
		newPimTaskListCmd(a, f, scope),
		newPimTaskAddCmd(a, f, scope),
		newPimTaskSetCmd(a, f, scope),
		newPimTaskDoneCmd(a, f, scope),
	)
	return cmd
}

func newPimTaskListCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var limit int
	var cursor, uid string
	var all, open bool
	cmd := &cobra.Command{
		Use:     "list <calendar>",
		Aliases: []string{"ls"},
		Short:   "List a calendar's to-dos with their deadlines and status",
		Long: "List the VTODOs in a calendar.\n\n" +
			"--open hides the finished ones. Core has no status query (STATUS is a stored\n" +
			"extract, not a query dimension), so the filter runs over the rows that were\n" +
			"fetched — pair it with --all on a calendar bigger than one page.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			opts := coreapi.PimObjectListOpts{
				Limit: limit, Cursor: cursor, UID: uid,
				Component: coreapi.TaskComponent, Fields: "meta",
			}
			var objs []coreapi.PimObject
			next := ""
			for {
				page, lerr := client.ListPimObjects(cmd.Context(), s, f.kind, args[0], opts)
				if lerr != nil {
					return lerr
				}
				objs = append(objs, page.Objects...)
				next = page.NextCursor
				if !all || next == "" {
					break
				}
				opts.Cursor = next
			}
			if open {
				objs = openTasksOnly(objs)
			}
			a.out.Emit(map[string]any{"objects": objs, "nextCursor": next}, func(w io.Writer) {
				printPimTasks(a, w, objs)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max to-dos per page (1–5000)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "resume from a previous nextCursor")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().StringVar(&uid, "uid", "", "point lookup by iCalendar UID")
	cmd.Flags().BoolVar(&open, "open", false, "hide completed and cancelled to-dos (client-side filter)")
	return cmd
}

func newPimTaskAddCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var raw taskFlags
	var href string
	cmd := &cobra.Command{
		Use:     "add <calendar> <title>",
		Aliases: []string{"new", "create"},
		Short:   "Create a to-do",
		Long: "Create a VTODO from a title and, optionally, a deadline and priority.\n\n" +
			"The body is written as a JSCalendar Task and converted by core, so this takes\n" +
			"the same write path `calendars objects put` does — ETag, sync token and\n" +
			"scheduling behave identically. The href derives from a fresh UID unless --href\n" +
			"names one, and the create is exclusive (If-None-Match: *), so it can never\n" +
			"overwrite an existing object.\n\n" +
			"  openemail calendars tasks add tasks \"Buy milk\" --due 2026-08-10\n\n" +
			"A bare YYYY-MM-DD due is a whole-day deadline; pass an RFC3339 timestamp for a\n" +
			"particular moment.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := taskEditsFromFlags(cmd, raw)
			if err != nil {
				return usageError(err)
			}
			uid := compose.NewDeliveryID()
			doc, err := coreapi.NewTaskDocument(uid, args[1], e)
			if err != nil {
				return usageError(err)
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			target := href
			if target == "" {
				target = coreapi.TaskHref(uid)
			}
			res, err := client.PutPimObjectJSON(cmd.Context(), s, f.kind, args[0], target, doc,
				coreapi.PimPutOpts{IfNoneMatchStar: true})
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Created to-do %s (uid %s)", res.Href, uid)
				a.out.Msgf("complete it with: openemail calendars tasks done %s %s", args[0], res.Href)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&raw.due, "due", "", "deadline (YYYY-MM-DD for a whole day, RFC3339, or unix seconds)")
	cmd.Flags().IntVar(&raw.priority, "priority", 0, "0–9 (0 = undefined, 1 = highest)")
	cmd.Flags().StringVar(&raw.description, "description", "", "longer notes")
	cmd.Flags().StringVar(&href, "href", "", "resource name (default: <uid>.ics)")
	return cmd
}

func newPimTaskSetCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var raw taskFlags
	cmd := &cobra.Command{
		Use:     "set <calendar> <href>",
		Aliases: []string{"edit", "update"},
		Short:   "Change a to-do's title, deadline, priority or progress",
		Long: "Read the to-do as JSCalendar, change only what you named, and write it back\n" +
			"under If-Match. Members this CLI does not model — recurrence, alarms, and every\n" +
			"unmapped iCalendar property core preserved through its escape hatches — survive\n" +
			"untouched, which re-authoring the body by hand does not manage.\n\n" +
			"An empty value CLEARS a field: --due \"\" drops the deadline, --title \"\" the\n" +
			"summary. Omit a flag to leave it alone.\n\n" +
			"If this mailbox ORGANIZES the to-do, the write fans out iTIP to its attendees\n" +
			"exactly as an event edit does.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := taskEditsFromFlags(cmd, raw)
			if err != nil {
				return usageError(err)
			}
			if e.Empty() {
				return usageError(errors.New("nothing to change — pass at least one field flag"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			return a.mutateTask(cmd, client, s, f, args[0], args[1], e)
		},
	}
	cmd.Flags().StringVar(&raw.title, "title", "", "summary (empty clears)")
	cmd.Flags().StringVar(&raw.description, "description", "", "longer notes (empty clears)")
	cmd.Flags().StringVar(&raw.due, "due", "", "deadline (YYYY-MM-DD, RFC3339, or unix seconds; empty clears)")
	cmd.Flags().IntVar(&raw.priority, "priority", 0, "0–9 (0 = undefined, 1 = highest)")
	cmd.Flags().IntVar(&raw.percent, "percent", 0, "percent complete, 0–100")
	cmd.Flags().StringVar(&raw.progress, "progress", "", strings.Join(coreapi.TaskProgressValues, " | "))
	return cmd
}

func newPimTaskDoneCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var reopen bool
	cmd := &cobra.Command{
		Use:     "done <calendar> <href>",
		Aliases: []string{"complete", "check"},
		Short:   "Mark a to-do complete (or --reopen it)",
		Long: "Mark a to-do complete. That is three properties, not one: STATUS:COMPLETED,\n" +
			"PERCENT-COMPLETE:100 and the COMPLETED timestamp — which is why setting STATUS\n" +
			"by hand leaves other clients showing it as partly done.\n\n" +
			"--reopen is the exact inverse: back to needs-action, percentage reset, and the\n" +
			"completion timestamp removed.\n\n" +
			"Completing a to-do somebody ASSIGNED you updates your own copy only: core's\n" +
			"iTIP fan-out belongs to the organizer, so nothing is mailed. Tell them with\n" +
			"`openemail calendars respond`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := coreapi.TaskEdits{Progress: "completed", ProgressSet: true}
			if reopen {
				e.Progress = "needs-action"
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			return a.mutateTask(cmd, client, s, f, args[0], args[1], e)
		},
	}
	cmd.Flags().BoolVar(&reopen, "reopen", false, "the inverse: back to needs-action, clearing the completion")
	return cmd
}

// mutateTask is the read-modify-write shared by `set` and `done`.
//
// The If-Match comes from the ETag of the very read that produced the document,
// so a concurrent DAV or JMAP write loses with a 412 instead of being silently
// overwritten — the same guarantee core's own RSVP CAS gives.
func (a *app) mutateTask(cmd *cobra.Command, client *coreapi.Client, s coreapi.PimScope, f pimFamily, ref, href string, e coreapi.TaskEdits) error {
	obj, err := client.GetPimObjectJSON(cmd.Context(), s, f.kind, ref, href, "")
	if err != nil {
		return err
	}
	doc, err := coreapi.TaskDocumentOf(obj)
	if err != nil {
		return usageError(fmt.Errorf("%w (`openemail calendars objects get/put %s %s`)", err, ref, href))
	}
	edited, err := coreapi.ApplyTaskEdits(doc, e)
	if err != nil {
		return usageError(err)
	}
	res, err := client.PutPimObjectJSON(cmd.Context(), s, f.kind, ref, href, edited,
		coreapi.PimPutOpts{IfMatch: obj.Etag})
	if err != nil {
		if ae, ok := coreapi.AsAPIError(err); ok && ae.Status == 412 {
			a.out.Warnf("someone else changed this to-do while you were editing — re-run to pick up their version")
		}
		return err
	}
	a.out.Emit(res, func(w io.Writer) {
		a.out.Successf("Updated %s (etag %s)", res.Href, res.Etag)
		if e.ProgressSet {
			a.out.Msgf("progress: %s", edited["progress"])
		}
	})
	return nil
}
