package coreapi

import (
	"strings"
	"testing"
	"time"
)

func sp(s string) *string { return &s }

// A to-do is addressed exactly like an event, so `component` is the only thing
// on the wire that tells them apart. Everything task-shaped hangs off this
// predicate, so it must survive case and must not claim an unparsable body
// (which stores no component) is a task.
func TestIsTaskObject(t *testing.T) {
	cases := []struct {
		comp *string
		want bool
	}{
		{sp("VTODO"), true},
		{sp("vtodo"), true}, // core stamps upper case; a client must not depend on it
		{sp("VEVENT"), false},
		{sp("VJOURNAL"), false},
		{nil, false},
	}
	for _, tc := range cases {
		got := IsTaskObject(PimObject{PimObjectMeta: PimObjectMeta{Component: tc.comp}})
		if got != tc.want {
			t.Errorf("IsTaskObject(%v) = %v, want %v", tc.comp, got, tc.want)
		}
	}
}

// The CLOSED set is enumerated, never the open one: a STATUS nobody here has
// heard of must still show up in a list of things to do.
func TestTaskIsClosed(t *testing.T) {
	cases := map[*string]bool{
		sp("COMPLETED"):    true,
		sp("cancelled"):    true,
		sp("NEEDS-ACTION"): false,
		sp("IN-PROCESS"):   false,
		sp("X-SOMETHING"):  false,
		nil:                false, // no STATUS = not started
	}
	for status, want := range cases {
		got := TaskIsClosed(PimObject{PimObjectMeta: PimObjectMeta{EventStatus: status}})
		if got != want {
			t.Errorf("TaskIsClosed(%v) = %v, want %v", status, got, want)
		}
	}
}

// Core's `component` query is a zod enum over the RFC spellings, so a lowercase
// value is a 400 a user cannot read anything useful out of.
func TestNormalizeComponent(t *testing.T) {
	cases := map[string]string{
		"VTODO": "VTODO", "vtodo": "VTODO", "todo": "VTODO", "task": "VTODO",
		"vevent": "VEVENT", "event": "VEVENT",
		"vjournal": "VJOURNAL", "journal": "VJOURNAL",
	}
	for in, want := range cases {
		got, err := NormalizeComponent(in)
		if err != nil || got != want {
			t.Errorf("NormalizeComponent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"VCARD", "reminder", "vtodos", ""} {
		if _, err := NormalizeComponent(bad); err == nil {
			t.Errorf("NormalizeComponent(%q) unexpectedly ok", bad)
		}
	}
}

// The document a create builds is a JSCalendar Task, and every member core
// validates has a rule this must respect: `@type` decides the type (absent means
// Event, which files a to-do as a meeting), `due` is a LocalDateTime with the
// zone carried separately, and a whole-day due is `showWithoutTime`.
func TestNewTaskDocument(t *testing.T) {
	day := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).Unix()
	doc, err := NewTaskDocument("u-1", "Buy milk", TaskEdits{
		Due: &TaskDue{Unix: day, AllDay: true}, DueSet: true,
		Priority: 5, PrioritySet: true,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if doc["@type"] != "Task" {
		t.Fatalf("@type = %v — absent or wrong means core files it as an Event", doc["@type"])
	}
	if doc["uid"] != "u-1" || doc["title"] != "Buy milk" {
		t.Fatalf("doc = %v", doc)
	}
	if doc["due"] != "2026-08-10T00:00:00" {
		t.Fatalf("due = %v, want an RFC 8984 LocalDateTime (no zone suffix)", doc["due"])
	}
	if doc["timeZone"] != "Etc/UTC" {
		t.Fatalf("timeZone = %v", doc["timeZone"])
	}
	if doc["showWithoutTime"] != true {
		t.Fatalf("a whole-day due must set showWithoutTime, got %v", doc["showWithoutTime"])
	}
	if doc["priority"] != 5 {
		t.Fatalf("priority = %v", doc["priority"])
	}
	// A new to-do states that nobody has started it rather than leaving STATUS
	// absent for every client to guess at.
	if doc["progress"] != "needs-action" {
		t.Fatalf("progress = %v", doc["progress"])
	}

	timed, err := NewTaskDocument("u-2", "Ship", TaskEdits{
		Due: &TaskDue{Unix: time.Date(2026, 8, 10, 17, 30, 0, 0, time.UTC).Unix()}, DueSet: true,
	})
	if err != nil {
		t.Fatalf("timed build: %v", err)
	}
	if timed["due"] != "2026-08-10T17:30:00" {
		t.Fatalf("timed due = %v", timed["due"])
	}
	if _, ok := timed["showWithoutTime"]; ok {
		t.Fatalf("a timed due must not claim to be all-day: %v", timed)
	}

	// Core refuses these; refusing here names the field instead of a zod path.
	if _, err := NewTaskDocument("u-3", "x", TaskEdits{Priority: 12, PrioritySet: true}); err == nil {
		t.Error("priority 12 must be refused")
	}
	if _, err := NewTaskDocument("u-4", "x", TaskEdits{Percent: 140, PercentSet: true}); err == nil {
		t.Error("percentComplete 140 must be refused")
	}
	if _, err := NewTaskDocument("u-5", "x", TaskEdits{Progress: "abandoned", ProgressSet: true}); err == nil {
		t.Error("an unknown progress must be refused")
	}
	// `failed` is a real RFC 8984 value with no RFC 5545 STATUS behind it; core
	// refuses it, so offering it here would only produce a round trip to a 400.
	if _, err := NewTaskDocument("u-6", "x", TaskEdits{Progress: "failed", ProgressSet: true}); err == nil {
		t.Error("progress 'failed' has no iCalendar STATUS and must be refused")
	}
}

// `set` is a read-modify-write over the document core returned, so every member
// it does not touch — including the `open.email:*` escape hatches carrying the
// properties JSCalendar does not model — must survive verbatim. Re-authoring the
// body instead is how a VALARM or an X- property is lost on a title change.
func TestApplyTaskEditsPreservesUnknownMembers(t *testing.T) {
	stored := map[string]any{
		"@type": "Task", "uid": "u-1", "title": "Old",
		"due": "2026-08-10T00:00:00", "timeZone": "Europe/Zurich", "showWithoutTime": true,
		"open.email:taskProps":      []any{[]any{"x-asana-id", map[string]any{}, "text", "42"}},
		"open.email:taskComponents": []any{map[string]any{"name": "VALARM"}},
		"recurrenceRules":           []any{map[string]any{"frequency": "weekly"}},
	}
	got, err := ApplyTaskEdits(stored, TaskEdits{Title: "New", TitleSet: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got["title"] != "New" {
		t.Fatalf("title = %v", got["title"])
	}
	for _, key := range []string{
		"open.email:taskProps", "open.email:taskComponents",
		"recurrenceRules", "timeZone", "showWithoutTime",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s was dropped — a read-modify-write must preserve what it does not touch", key)
		}
	}
	// An untouched due keeps its own zone: rewriting it to Etc/UTC would move a
	// Zurich deadline without the user asking.
	if got["due"] != "2026-08-10T00:00:00" || got["timeZone"] != "Europe/Zurich" {
		t.Errorf("untouched due changed: %v / %v", got["due"], got["timeZone"])
	}
	// The source document must not be mutated in place — the caller still holds
	// it (the TUI re-renders the row it read).
	if stored["title"] != "Old" {
		t.Errorf("the stored document was mutated in place: %v", stored["title"])
	}
}

// Completion is three properties, not one: RFC 5545 pairs STATUS:COMPLETED with
// PERCENT-COMPLETE:100 and the COMPLETED timestamp. Setting STATUS alone leaves
// a task reading "partly done, at an unknown time" in every other client.
func TestApplyTaskEditsCompletion(t *testing.T) {
	got, err := ApplyTaskEdits(map[string]any{"@type": "Task", "uid": "u"},
		TaskEdits{Progress: "completed", ProgressSet: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got["progress"] != "completed" {
		t.Fatalf("progress = %v", got["progress"])
	}
	if got["percentComplete"] != 100 {
		t.Fatalf("percentComplete = %v — completion implies 100", got["percentComplete"])
	}
	stamp, _ := got["progressUpdated"].(string)
	if _, err := time.Parse("2006-01-02T15:04:05Z", stamp); err != nil {
		t.Fatalf("progressUpdated = %q — core requires a UTCDateTime (…Z)", stamp)
	}

	// Re-opening must clear the completion, or the task reads "100% done, needs
	// action".
	reopened, err := ApplyTaskEdits(got, TaskEdits{Progress: "needs-action", ProgressSet: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reopened["progressUpdated"]; ok {
		t.Errorf("re-opening must clear progressUpdated: %v", reopened)
	}
	if reopened["percentComplete"] != 0 {
		t.Errorf("re-opening must reset percentComplete, got %v", reopened["percentComplete"])
	}

	// An explicit percentage wins over the value completion implies.
	explicit, err := ApplyTaskEdits(map[string]any{"@type": "Task", "uid": "u"},
		TaskEdits{Progress: "in-process", ProgressSet: true, Percent: 40, PercentSet: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if explicit["percentComplete"] != 40 {
		t.Errorf("explicit percent must win, got %v", explicit["percentComplete"])
	}
}

// Clearing is a distinct intent from leaving alone: DueSet with a nil Due
// removes the deadline. A field that could only ever set would leave a mis-dated
// task un-fixable.
func TestApplyTaskEditsClearsDue(t *testing.T) {
	stored := map[string]any{
		"@type": "Task", "uid": "u", "due": "2026-08-10T00:00:00",
		"timeZone": "Etc/UTC", "showWithoutTime": true,
	}
	got, err := ApplyTaskEdits(stored, TaskEdits{DueSet: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := got["due"]; ok {
		t.Fatalf("a nil Due must remove the deadline: %v", got)
	}
	if _, ok := got["showWithoutTime"]; ok {
		t.Errorf("showWithoutTime outlived the only date it described: %v", got)
	}

	// With a start still present, showWithoutTime still describes something.
	withStart := map[string]any{
		"@type": "Task", "uid": "u", "start": "2026-08-10T00:00:00",
		"due": "2026-08-11T00:00:00", "timeZone": "Etc/UTC", "showWithoutTime": true,
	}
	kept, err := ApplyTaskEdits(withStart, TaskEdits{DueSet: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := kept["showWithoutTime"]; !ok {
		t.Errorf("showWithoutTime still describes the start: %v", kept)
	}
}

// RFC 8984 gives a Task ONE `timeZone` and ONE `showWithoutTime` covering BOTH
// start and due. So writing a due whose shape differs from the stored anchor
// rewrites the start with it — by the zone offset, or from a whole day to an
// instant — silently, and with no way for the user to notice. A task that has no
// start has no such coupling and takes any due.
func TestApplyTaskEditsRefusesAnchorShift(t *testing.T) {
	timed := &TaskDue{Unix: 1786000000}
	allDay := &TaskDue{Unix: 1786000000, AllDay: true}

	// A zoned start: a UTC due would move it by the offset.
	zoned := map[string]any{"@type": "Task", "uid": "u", "start": "2026-08-10T09:00:00", "timeZone": "Europe/Zurich"}
	if _, err := ApplyTaskEdits(zoned, TaskEdits{Due: timed, DueSet: true}); err == nil ||
		!strings.Contains(err.Error(), "Europe/Zurich") {
		t.Errorf("zoned start: err = %v — the zone must be named", err)
	}

	// A whole-day start: a timed due would turn it into an instant.
	wholeDay := map[string]any{"@type": "Task", "uid": "u", "start": "2026-08-10T00:00:00", "showWithoutTime": true}
	if _, err := ApplyTaskEdits(wholeDay, TaskEdits{Due: timed, DueSet: true}); err == nil {
		t.Error("whole-day start + timed due must be refused — it rewrites the start")
	}
	// …and the matching shape is fine: allDay short-circuits the zone entirely.
	if _, err := ApplyTaskEdits(wholeDay, TaskEdits{Due: allDay, DueSet: true}); err != nil {
		t.Errorf("whole-day start + whole-day due must be allowed: %v", err)
	}

	// A FLOATING start (no zone at all) would become UTC — also a move.
	floating := map[string]any{"@type": "Task", "uid": "u", "start": "2026-08-10T09:00:00"}
	if _, err := ApplyTaskEdits(floating, TaskEdits{Due: timed, DueSet: true}); err == nil {
		t.Error("a floating start must not be silently anchored to UTC")
	}

	// A start already in Etc/UTC is exactly what the writer would produce, so
	// there is nothing to move.
	utc := map[string]any{"@type": "Task", "uid": "u", "start": "2026-08-10T09:00:00", "timeZone": "Etc/UTC"}
	if _, err := ApplyTaskEdits(utc, TaskEdits{Due: timed, DueSet: true}); err != nil {
		t.Errorf("a UTC start must accept a timed due: %v", err)
	}

	// No start, no coupling — every shape is fine.
	dueOnly := map[string]any{"@type": "Task", "uid": "u", "timeZone": "Europe/Zurich"}
	for _, due := range []*TaskDue{timed, allDay} {
		if _, err := ApplyTaskEdits(dueOnly, TaskEdits{Due: due, DueSet: true}); err != nil {
			t.Errorf("a start-less task must accept any due: %v", err)
		}
	}
	// Clearing is always safe: it removes a member rather than re-anchoring one.
	if _, err := ApplyTaskEdits(zoned, TaskEdits{DueSet: true}); err != nil {
		t.Errorf("clearing a due must never be refused: %v", err)
	}
}

// RFC 5545 §3.6.2: a VTODO carries DUE or DURATION, never both. Core refuses the
// pair, so producing it here only costs a round trip.
func TestApplyTaskEditsRefusesDueWithDuration(t *testing.T) {
	doc := map[string]any{"@type": "Task", "uid": "u", "start": "2026-08-10T09:00:00", "estimatedDuration": "PT1H"}
	_, err := ApplyTaskEdits(doc, TaskEdits{Due: &TaskDue{Unix: 1786000000}, DueSet: true})
	if err == nil || !strings.Contains(err.Error(), "3.6.2") {
		t.Fatalf("err = %v — the RFC rule must be named", err)
	}
}

// The edit path refuses a document that is not a Task, rather than PUTting a
// mangled Event back. `data:null` is the VJOURNAL/unparsable case; `writable:
// false` is core saying up front that the write would be refused.
func TestTaskDocumentOf(t *testing.T) {
	if _, err := TaskDocumentOf(nil); err == nil {
		t.Error("a nil object must be refused")
	}
	if _, err := TaskDocumentOf(&PimObjectJSON{Data: nil}); err == nil {
		t.Error("a null data document must be refused")
	}
	event := &PimObjectJSON{Data: map[string]any{"@type": "Event", "uid": "u"}, Writable: true}
	if _, err := TaskDocumentOf(event); err == nil || !strings.Contains(err.Error(), "not a to-do") {
		t.Errorf("an Event must be refused by name, got %v", err)
	}
	unwritable := &PimObjectJSON{Data: map[string]any{"@type": "Task", "uid": "u"}, Writable: false}
	if _, err := TaskDocumentOf(unwritable); err == nil {
		t.Error("writable:false must be refused before the PUT, not after core's 400")
	}
	ok := &PimObjectJSON{Data: map[string]any{"@type": "Task", "uid": "u"}, Writable: true}
	if _, err := TaskDocumentOf(ok); err != nil {
		t.Errorf("a writable Task must pass: %v", err)
	}
}

// An href derived from the UID is what makes a re-run converge on the object it
// already wrote instead of minting a second copy of the same task.
func TestTaskHref(t *testing.T) {
	if got := TaskHref("01JABCDEF"); got != "01JABCDEF.ics" {
		t.Errorf("TaskHref = %q", got)
	}
}

func TestTaskEditsEmpty(t *testing.T) {
	if !(TaskEdits{}).Empty() {
		t.Error("a zero TaskEdits changes nothing")
	}
	if (TaskEdits{DueSet: true}).Empty() {
		t.Error("clearing the due is a change")
	}
}
