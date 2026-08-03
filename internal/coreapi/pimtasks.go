package coreapi

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// A to-do is not a second resource kind — it is a VTODO stored in a calendar,
// addressed by the same href, guarded by the same ETag, written through the same
// PUT and scheduled by the same iTIP fan-out as a VEVENT. So there are no
// task-specific endpoints to wrap here.
//
// What IS task-specific is the DOCUMENT. Core maps a VTODO to a JSCalendar Task
// (RFC 8984) rather than an Event, and the two are not interchangeable: a Task
// has `due` where an Event has `duration`, `progress`/`percentComplete` where an
// Event has `status`, and an OPTIONAL `start` (RFC 5545 §3.6.2) where an Event
// requires one. This file is the one place that knows those rules, so the
// command tree and the console cannot disagree about what marking a task done
// means.

// TaskComponent is the iCalendar component a to-do is stored as.
const TaskComponent = "VTODO"

// TaskProgressValues are RFC 8984 §4.7.3's `progress` values that have an
// RFC 5545 STATUS behind them. `failed` is a real JSCalendar value core refuses
// on write — there is no iCalendar equivalent, and folding it into CANCELLED
// would tell every other client the task was abandoned rather than attempted —
// so it is not offered here either.
var TaskProgressValues = []string{"needs-action", "in-process", "completed", "cancelled"}

// closedTaskStatuses are the STATUS values meaning a to-do needs no more
// attention. The CLOSED set is enumerated rather than the open one on purpose: a
// value we have not heard of must show up in a list of things to do, never
// vanish from it.
var closedTaskStatuses = map[string]bool{"COMPLETED": true, "CANCELLED": true}

// IsTaskObject reports whether a stored object is a to-do. An object with no
// component is a body core could not parse; it is not a task.
func IsTaskObject(o PimObject) bool {
	return o.Component != nil && strings.EqualFold(*o.Component, TaskComponent)
}

// TaskIsClosed reports whether a to-do is finished. A to-do with no STATUS at
// all is NOT started (RFC 5545 leaves it implicit), so it counts as open.
func TaskIsClosed(o PimObject) bool {
	return o.EventStatus != nil && closedTaskStatuses[strings.ToUpper(*o.EventStatus)]
}

// NormalizeComponent maps a friendly spelling to the RFC 5545 component name.
//
// Core's `component` query is a zod enum over the three upper-case spellings, so
// `component=vtodo` answers `validation_failed` naming a zod path — which tells
// a user nothing about what to type instead. Normalizing the obvious forms means
// the common case works and a real typo never leaves the machine.
func NormalizeComponent(s string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "VTODO", "TODO", "TASK":
		return "VTODO", nil
	case "VEVENT", "EVENT":
		return "VEVENT", nil
	case "VJOURNAL", "JOURNAL":
		return "VJOURNAL", nil
	}
	return "", fmt.Errorf("invalid component %q: expected event (VEVENT), task (VTODO), or journal (VJOURNAL)", s)
}

// TaskHref derives a to-do's resource name from its UID — the convention core's
// own importer uses, so a re-run addresses the object it already wrote instead
// of minting a second copy of the same task.
func TaskHref(uid string) string { return uid + ".ics" }

// TaskDue is a deadline already resolved to an instant. AllDay marks a
// DATE-valued DUE ("due that day") rather than a moment on it.
type TaskDue struct {
	Unix   int64
	AllDay bool
}

// TaskEdits is one edit's intent. Each field pairs with a *Set flag because
// CLEARING is a distinct intent from leaving alone — an empty title removes the
// summary, an unset one keeps it — and a single zero value cannot mean both.
// Due is additionally nilable: DueSet with a nil Due removes the deadline.
type TaskEdits struct {
	Title       string
	TitleSet    bool
	Description string
	DescSet     bool
	Due         *TaskDue
	DueSet      bool
	Priority    int
	PrioritySet bool
	Percent     int
	PercentSet  bool
	Progress    string
	ProgressSet bool
}

// Empty reports whether an edit would change nothing.
func (e TaskEdits) Empty() bool {
	return !e.TitleSet && !e.DescSet && !e.DueSet && !e.PrioritySet && !e.PercentSet && !e.ProgressSet
}

// NewTaskDocument mints a fresh JSCalendar Task.
//
// `@type` is not decoration: core's REST write reads it to choose the converter,
// and ABSENT means Event — so a document without it files a to-do as a meeting.
func NewTaskDocument(uid, title string, e TaskEdits) (map[string]any, error) {
	doc := map[string]any{"@type": "Task", "uid": uid}
	if strings.TrimSpace(title) != "" {
		doc["title"] = title
	}
	if !e.ProgressSet {
		// A new to-do is one nobody has started. Stating it beats leaving STATUS
		// absent, which every client renders its own way.
		e.Progress, e.ProgressSet = "needs-action", true
	}
	e.TitleSet = false // the positional title above is the authority
	return ApplyTaskEdits(doc, e)
}

// ApplyTaskEdits is the modify half of a read-modify-write over the document
// core returned.
//
// It mutates NAMED members and leaves everything else alone, which is the whole
// point. A JSCalendar Task carries members no client here models — recurrence
// rules, alarms via `open.email:taskComponents`, and every unmapped iCalendar
// property via `open.email:taskProps` — and rebuilding the body from a form
// would drop them on a one-word title change. Those escape hatches exist in core
// precisely to stop that loss; a client that re-authored the document would
// reintroduce it on this side.
func ApplyTaskEdits(doc map[string]any, e TaskEdits) (map[string]any, error) {
	out := make(map[string]any, len(doc)+4)
	for k, v := range doc {
		out[k] = v
	}

	setOrClear := func(key, val string) {
		if val == "" {
			delete(out, key)
		} else {
			out[key] = val
		}
	}
	if e.TitleSet {
		setOrClear("title", e.Title)
	}
	if e.DescSet {
		setOrClear("description", e.Description)
	}

	if e.DueSet {
		if err := setTaskDue(out, e.Due); err != nil {
			return nil, err
		}
	}

	if e.PrioritySet {
		// RFC 5545 §3.8.1.9 bounds PRIORITY at 0–9, and core's jsTaskToICal
		// enforces it; catching it here names the field instead of a zod path.
		if e.Priority < 0 || e.Priority > 9 {
			return nil, fmt.Errorf("priority must be 0–9 (0 = undefined, 1 = highest), got %d", e.Priority)
		}
		if e.Priority == 0 {
			delete(out, "priority")
		} else {
			out["priority"] = e.Priority
		}
	}

	if e.ProgressSet {
		progress := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(e.Progress), "_", "-"))
		if !containsString(TaskProgressValues, progress) {
			return nil, fmt.Errorf("progress must be one of %s, got %q",
				strings.Join(TaskProgressValues, ", "), e.Progress)
		}
		out["progress"] = progress
		if progress == "completed" {
			// RFC 5545 pairs STATUS:COMPLETED with PERCENT-COMPLETE:100 and the
			// COMPLETED timestamp. Writing STATUS alone leaves every other client
			// showing a finished task as partly done, at an unknown time.
			out["percentComplete"] = 100
			out["progressUpdated"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		} else {
			// Re-opening: the completion timestamp describes a completion that no
			// longer holds, and a lingering 100% reads as "done, needs action".
			delete(out, "progressUpdated")
			out["percentComplete"] = 0
		}
	}

	if e.PercentSet {
		if e.Percent < 0 || e.Percent > 100 {
			return nil, fmt.Errorf("percent complete must be 0–100, got %d", e.Percent)
		}
		// After the progress block, so an explicitly given percentage wins over
		// the value completion implies.
		out["percentComplete"] = e.Percent
	}

	return out, nil
}

// setTaskDue writes (or clears) the deadline.
//
// The hazard this guards is the ANCHOR. RFC 8984 gives a Task one `timeZone`
// and one `showWithoutTime` covering BOTH `start` and `due` — so writing a due
// whose shape differs from the stored anchor rewrites the start along with it:
// by the zone offset, or from a whole day into an instant. Doing the zone
// arithmetic instead is not an option here; it cannot be verified against the
// VTIMEZONE core stored, and getting it wrong moves a deadline silently. So a
// document that would be affected is refused, naming what would have changed.
func setTaskDue(doc map[string]any, due *TaskDue) error {
	if due == nil {
		delete(doc, "due")
		// showWithoutTime describes the date members; with none left it describes
		// nothing, and a later timed due would inherit an all-day claim.
		if _, hasStart := doc["start"]; !hasStart {
			delete(doc, "showWithoutTime")
		}
		// Clearing removes a member rather than re-anchoring one, so none of the
		// guards below apply.
		return nil
	}
	if _, hasDuration := doc["estimatedDuration"]; hasDuration {
		// RFC 5545 §3.6.2: a VTODO carries DUE or DURATION, never both. Core
		// refuses the pair; saying so here names what to remove first.
		return errors.New("this to-do has an estimated duration, and RFC 5545 §3.6.2 allows DUE or DURATION but not both — clear the duration first")
	}
	if _, hasStart := doc["start"]; hasStart {
		zone, _ := doc["timeZone"].(string)
		storedAllDay, _ := doc["showWithoutTime"].(bool)
		switch {
		case due.AllDay && storedAllDay:
			// Both whole-day: the zone is inert on either member (a DATE value
			// carries none), so the anchor cannot move.
		case !due.AllDay && !storedAllDay && zone == "Etc/UTC":
			// Already exactly what this writer produces.
		case storedAllDay != due.AllDay:
			return errors.New("this to-do has a start, and RFC 8984 gives a task ONE date shape for both start and due — changing the due between a whole day and a moment would rewrite the start too")
		case zone == "":
			return errors.New("this to-do has a floating start (no timezone), and RFC 8984 gives a task ONE timeZone for both start and due — setting the due here would anchor the start to UTC")
		default:
			return fmt.Errorf("this to-do starts in %s, and RFC 8984 gives a task ONE timeZone covering both start and due — setting the due here would move the start", zone)
		}
	}

	// RFC 8984 LocalDateTime: no offset, no Z; the zone rides `timeZone`.
	doc["due"] = time.Unix(due.Unix, 0).UTC().Format("2006-01-02T15:04:05")
	doc["timeZone"] = "Etc/UTC"
	if due.AllDay {
		doc["showWithoutTime"] = true
	} else {
		delete(doc, "showWithoutTime")
	}
	return nil
}

// TaskDocumentOf is the guard between a read and the write that follows it.
//
// Each refusal is one core would otherwise make after a round trip — except the
// Event case, which core would ACCEPT, storing an Event-shaped body over what
// the caller believes is their to-do.
func TaskDocumentOf(obj *PimObjectJSON) (map[string]any, error) {
	if obj == nil || obj.Data == nil {
		return nil, errors.New("this object has no JSCalendar mapping (a VJOURNAL, or a body core could not parse) — edit it as raw iCalendar instead")
	}
	if t, _ := obj.Data["@type"].(string); t != "Task" {
		named := t
		if named == "" {
			named = "absent"
		}
		return nil, fmt.Errorf("this object is not a to-do (@type %q)", named)
	}
	if !obj.Writable {
		// Core computed this: its reader is more permissive than its writer, so
		// the document it just handed us cannot be converted back.
		return nil, errors.New("core reports this object as read-only JSON (it converts to JSCalendar but not back) — edit it as raw iCalendar instead")
	}
	return obj.Data, nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
