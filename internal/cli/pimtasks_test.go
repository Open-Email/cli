package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// The listing command must reject a bad --component before it touches the
// network: core answers a lowercase or misspelt value with `validation_failed`
// and a zod path, which tells a user nothing about which spellings exist.
func TestObjListValidatesComponentBeforeAuth(t *testing.T) {
	cmd := newPimObjListCmd(&app{}, pimFamily{kind: coreapi.PimCalendars, noun: "calendar"}, nil)
	cmd.SetArgs([]string{"work", "--component", "reminder"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--component") {
		t.Fatalf("error = %v, want a --component usage error", err)
	}
	// A spelling core WOULD accept must get past validation and fail on auth
	// instead — otherwise the normalizer is rejecting valid input.
	cmd = newPimObjListCmd(&app{}, pimFamily{kind: coreapi.PimCalendars, noun: "calendar"}, nil)
	cmd.SetArgs([]string{"work", "--component", "vtodo"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("error = %v, want the auth error (vtodo must normalize, not fail)", err)
	}
}

// A to-do's scheduling metadata is not an event's: `dtend` carries DUE, and
// `eventStatus` carries the STATUS that says whether it is done. Printing them
// under an event's headers is how "END" came to mean "when it is due" and how
// completion became invisible.
func TestPrintPimTasks(t *testing.T) {
	var buf bytes.Buffer
	a := pimTestApp(&buf)
	tasks := []coreapi.PimObject{
		{PimObjectMeta: coreapi.PimObjectMeta{
			Href: "milk.ics", Component: strptr("VTODO"), UID: "u1",
			Dtend: i64ptr(1751000000), EventStatus: strptr("NEEDS-ACTION"),
			Organizer: strptr("mailto:boss@example.com"), UpdatedAt: 1751000000,
		}},
		{PimObjectMeta: coreapi.PimObjectMeta{
			Href: "ship.ics", Component: strptr("VTODO"), UID: "u2",
			EventStatus: strptr("COMPLETED"), UpdatedAt: 1751000000,
		}},
	}
	printPimTasks(a, &buf, tasks)
	out := buf.String()
	if !strings.Contains(out, "DUE") {
		t.Errorf("the task table must label the deadline column DUE, not END\n%s", out)
	}
	if !strings.Contains(out, "STATUS") {
		t.Errorf("the task table must show STATUS — it is the only field saying whether a task is done\n%s", out)
	}
	for _, want := range []string{"milk.ics", "NEEDS-ACTION", "ship.ics", "COMPLETED", "boss@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("task output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "mailto:") {
		t.Errorf("mailto: prefix not stripped\n%s", out)
	}
	// A to-do with no deadline is ordinary ("buy milk"); it must not render as a
	// 1970 epoch.
	if strings.Contains(out, "1970") {
		t.Errorf("a deadline-less task rendered epoch zero\n%s", out)
	}
}

// A calendar holds both kinds, so the mixed listing needs one shape whose
// headers are true of either. `dtend` is END for a VEVENT and DUE for a VTODO —
// the header must say so — and STATUS must be there, or a completed to-do is
// indistinguishable from an open one.
func TestPrintPimEventsCarriesStatusAndDue(t *testing.T) {
	var buf bytes.Buffer
	a := pimTestApp(&buf)
	objs := []coreapi.PimObject{
		{PimObjectMeta: coreapi.PimObjectMeta{
			Href: "standup.ics", Component: strptr("VEVENT"),
			Dtstart: i64ptr(1751000000), Dtend: i64ptr(1751003600),
			EventStatus: strptr("CONFIRMED"), UpdatedAt: 1751000000,
		}},
		{PimObjectMeta: coreapi.PimObjectMeta{
			Href: "milk.ics", Component: strptr("VTODO"),
			Dtend: i64ptr(1751003600), EventStatus: strptr("COMPLETED"), UpdatedAt: 1751000000,
		}},
	}
	printPimEvents(a, &buf, objs)
	out := buf.String()
	if !strings.Contains(out, "END/DUE") {
		t.Errorf("a mixed listing must not call a to-do's DUE an END\n%s", out)
	}
	if !strings.Contains(out, "COMPLETED") || !strings.Contains(out, "CONFIRMED") {
		t.Errorf("a mixed listing must carry STATUS for both kinds\n%s", out)
	}
}

// `tasks list --open` hides what is finished. It is a CLIENT-side filter (core
// has no status query), so it must be exact about which STATUS values count as
// closed — dropping an unknown one would silently hide a task.
func TestOpenTasksFilter(t *testing.T) {
	objs := []coreapi.PimObject{
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "a.ics", EventStatus: strptr("NEEDS-ACTION")}},
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "b.ics", EventStatus: strptr("COMPLETED")}},
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "c.ics", EventStatus: strptr("CANCELLED")}},
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "d.ics", EventStatus: strptr("IN-PROCESS")}},
		{PimObjectMeta: coreapi.PimObjectMeta{Href: "e.ics"}}, // no STATUS = not started
	}
	var hrefs []string
	for _, o := range openTasksOnly(objs) {
		hrefs = append(hrefs, o.Href)
	}
	want := "a.ics,d.ics,e.ics"
	if strings.Join(hrefs, ",") != want {
		t.Errorf("openTasksOnly = %v, want %v", hrefs, want)
	}
}

// A bare date is a whole-day deadline (a DATE-valued DUE, which is what a DAV
// client writes), not midnight on that day. Rendering "due Friday" as
// "due 00:00 Friday" makes every task look overdue the moment it is created.
func TestParseTaskDue(t *testing.T) {
	day, err := parseTaskDue("2026-08-10")
	if err != nil {
		t.Fatalf("date: %v", err)
	}
	if !day.AllDay || day.Unix != time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("bare date = %+v", day)
	}
	timed, err := parseTaskDue("2026-08-10T17:30:00Z")
	if err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	if timed.AllDay {
		t.Errorf("an RFC3339 due is a moment, not a whole day: %+v", timed)
	}
	if _, err := parseTaskDue("whenever"); err == nil {
		t.Error("an unparseable due must be refused")
	}
}

// taskEditsFromFlags is where "clear" and "leave alone" are told apart. Cobra's
// Changed() is the only thing that can: an empty string is a legitimate value
// for both readings.
func TestTaskEditsFromFlagsDistinguishesClearFromAbsent(t *testing.T) {
	build := func(args ...string) (coreapi.TaskEdits, error) {
		var raw taskFlags
		var got coreapi.TaskEdits
		var gotErr error
		cmd := &cobra.Command{Use: "set", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, _ []string) error {
			got, gotErr = taskEditsFromFlags(c, raw)
			return nil
		}}
		cmd.Flags().StringVar(&raw.title, "title", "", "")
		cmd.Flags().StringVar(&raw.description, "description", "", "")
		cmd.Flags().StringVar(&raw.due, "due", "", "")
		cmd.Flags().IntVar(&raw.priority, "priority", 0, "")
		cmd.Flags().IntVar(&raw.percent, "percent", 0, "")
		cmd.Flags().StringVar(&raw.progress, "progress", "", "")
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return got, gotErr
	}

	absent, err := build()
	if err != nil {
		t.Fatalf("absent: %v", err)
	}
	if !absent.Empty() {
		t.Errorf("no flags must change nothing: %+v", absent)
	}

	cleared, err := build("--due", "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !cleared.DueSet || cleared.Due != nil {
		t.Errorf(`--due "" must be a CLEAR (DueSet with a nil Due): %+v`, cleared)
	}

	set, err := build("--due", "2026-08-10", "--priority", "1", "--percent", "0")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if set.Due == nil || !set.Due.AllDay || !set.PrioritySet {
		t.Errorf("set = %+v", set)
	}
	// --percent 0 is a real value ("nothing done yet"), not an absent flag.
	if !set.PercentSet {
		t.Error("--percent 0 must register as set — it is a legitimate value")
	}

	if _, err := build("--due", "whenever"); err == nil {
		t.Error("an unparseable --due must be reported")
	}
}

// `set` with no field flags is a no-op the user did not mean; catching it before
// the read avoids a pointless GET + PUT that bumps the sync token for nothing.
func TestTaskSetRequiresAField(t *testing.T) {
	cmd := newPimTaskSetCmd(&app{}, pimFamily{kind: coreapi.PimCalendars, noun: "calendar"}, nil)
	cmd.SetArgs([]string{"tasks", "milk.ics"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("error = %v", err)
	}
}

// A bad flag value must be caught before the network, so a typo never costs a
// round trip and the message names the flag rather than a zod path.
func TestTaskAddValidatesBeforeAuth(t *testing.T) {
	cmd := newPimTaskAddCmd(&app{}, pimFamily{kind: coreapi.PimCalendars, noun: "calendar"}, nil)
	cmd.SetArgs([]string{"tasks", "Buy milk", "--priority", "12"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("error = %v, want a priority usage error", err)
	}
	cmd = newPimTaskAddCmd(&app{}, pimFamily{kind: coreapi.PimCalendars, noun: "calendar"}, nil)
	cmd.SetArgs([]string{"tasks", "Buy milk", "--due", "next tuesday"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--due") {
		t.Fatalf("error = %v, want a --due usage error", err)
	}
}

// The tasks group hangs off `calendars` only. An addressbook has no VTODOs, and
// a `tasks` verb there would be a command that can only ever fail.
func TestTasksGroupIsCalendarsOnly(t *testing.T) {
	cal := newCalendarsCmd(&app{})
	if findSub(cal, "tasks") == nil {
		t.Fatal("calendars must carry the tasks group")
	}
	if findSub(newAddressbooksCmd(&app{}), "tasks") != nil {
		t.Fatal("addressbooks must not carry a tasks group")
	}
	// The verbs a to-do actually needs, and no parallel tree of the collection
	// machinery `calendars` already provides.
	tasks := findSub(cal, "tasks")
	for _, verb := range []string{"list", "add", "set", "done"} {
		if findSub(tasks, verb) == nil {
			t.Errorf("tasks group missing %q", verb)
		}
	}
	for _, absent := range []string{"shares", "tokens", "export", "import", "changes"} {
		if findSub(tasks, absent) != nil {
			t.Errorf("tasks must not duplicate %q — it belongs to the collection, not the component", absent)
		}
	}
}

func findSub(parent *cobra.Command, name string) *cobra.Command {
	if parent == nil {
		return nil
	}
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
