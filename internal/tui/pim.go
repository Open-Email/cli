package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// pimNoun renders a kind's singular noun for titles and confirm copy.
func pimNoun(kind coreapi.PimKind) string {
	if kind == coreapi.PimAddressbooks {
		return "addressbook"
	}
	return "calendar"
}

func pimTitle(kind coreapi.PimKind) string {
	if kind == coreapi.PimAddressbooks {
		return "Addressbooks"
	}
	return "Calendars"
}

// pimScopeFor is the console's owner-mode scope: the console always operates
// on the drilled-into mailbox's own store (shared-collection access stays
// CLI-only, via --owner).
func pimScopeFor(mbx coreapi.Mailbox) coreapi.PimScope {
	return coreapi.PimScope{MailboxID: mbx.ID}
}

// pimCollectionsDesc lists one mailbox's calendars or addressbooks — the
// drill-down the Mailboxes screen opens with c/b. Enter opens the objects
// listing; i shows the collection detail (sync token etc.).
func pimCollectionsDesc(mbx coreapi.Mailbox, kind coreapi.PimKind) resourceDesc {
	scope := pimScopeFor(mbx)
	desc := resourceDesc{
		key:  string(kind) + ":" + mbx.ID,
		name: pimTitle(kind) + " — " + mailboxLabel(&mbx),
		columns: []column{
			{title: "NAME", flex: true},
			{title: "DISPLAY", flex: true},
			{title: "VISIBILITY", width: 10},
			{title: "ROLE", width: 8},
			{title: "OBJECTS", width: 7},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			cols, err := c.ListPimCollections(ctx, scope, kind)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(cols))
			for i, col := range cols {
				rows[i] = rowData{
					cells: []string{
						col.Name, strOr(col.DisplayName, "—"), col.Visibility,
						strOr(col.Role, "—"), fmt.Sprintf("%d", col.ObjectCount), fmtEpoch(col.CreatedAt),
					},
					item: col,
				}
			}
			return rows, "", nil // collections are unpaginated (max 10)
		},
		detail: func(item any) []kv {
			col := item.(coreapi.PimCollection)
			return []kv{
				{k: "id", v: col.ID},
				{k: "name", v: col.Name},
				{k: "display name", v: strOr(col.DisplayName, "—")},
				{k: "kind", v: col.Kind},
				{k: "visibility", v: col.Visibility},
				{k: "role", v: strOr(col.Role, "—")},
				{k: "color", v: strOr(col.Color, "—")},
				{k: "description", v: strOr(col.Description, "—")},
				{k: "objects", v: fmt.Sprintf("%d", col.ObjectCount)},
				{k: "sync token", v: col.SyncToken},
				{k: "created", v: fmtEpoch(col.CreatedAt)},
			}
		},
		open: func(ctx context.Context, ui *Options, item any) pane {
			return newScreenPane(ctx, ui, pimObjectsDesc(mbx, kind, item.(coreapi.PimCollection)))
		},
	}
	desc.actions = []action{
		{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
			return pimCollectionFormPane(ctx, ui, mbx, kind, nil)
		}},
		{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			col := item.(coreapi.PimCollection)
			return pimCollectionFormPane(ctx, ui, mbx, kind, &col)
		}},
		{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			return pimCollectionDeleteConfirm(ctx, ui, mbx, kind, item.(coreapi.PimCollection))
		}},
		{key: "S", label: "S shares", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			return newScreenPane(ctx, ui, pimSharesDesc(mbx, kind, item.(coreapi.PimCollection)))
		}},
		{key: "T", label: "T tokens", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			return newScreenPane(ctx, ui, pimTokensDesc(mbx, kind, item.(coreapi.PimCollection)))
		}},
		{key: "i", label: "i info", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			col := item.(coreapi.PimCollection)
			return newDetailPane(ctx, ui, desc, rowData{cells: []string{col.Name}, item: col})
		}},
	}
	return desc
}

// pimObjectsDesc lists one collection's objects (metadata only — the raw body
// is fetched by the detail pane's enrichment). Calendars get an RSVP action;
// enter shows the object detail with its wire text.
func pimObjectsDesc(mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) resourceDesc {
	scope := pimScopeFor(mbx)
	// A calendar holds to-dos as well as events, and core overloads one extract
	// for both: `dtend` is an event's end and a to-do's DUE. One table serves
	// both, so the header says both rather than picking one and lying about half
	// the calendar. STATUS earns its width for the same reason — it is the only
	// field that says whether a to-do is done, and without it a finished one and
	// an open one render identically.
	cols := []column{
		{title: "HREF", flex: true},
		{title: "COMP", width: 6},
		{title: "START", width: 16},
		{title: "END/DUE", width: 16},
		{title: "STATUS", width: 12},
		{title: "RECURS", width: 6},
		{title: "UPDATED", width: 16},
	}
	if kind == coreapi.PimAddressbooks {
		cols = []column{
			{title: "HREF", flex: true},
			{title: "NAME", flex: true},
			{title: "EMAIL", flex: true},
			{title: "SIZE", width: 9},
			{title: "UPDATED", width: 16},
		}
	}
	desc := resourceDesc{
		key:     string(kind) + ":objects:" + col.ID,
		name:    pimTitle(kind) + " › " + col.Name,
		columns: cols,
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			page, err := c.ListPimObjects(ctx, scope, kind, col.ID, coreapi.PimObjectListOpts{
				Limit: pageLimit, Cursor: cursor, Fields: "meta",
			})
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(page.Objects))
			for i, o := range page.Objects {
				var cells []string
				if kind == coreapi.PimAddressbooks {
					cells = []string{
						o.Href, strOr(o.VcardFn, "—"), strOr(o.VcardEmail, "—"),
						fmtBytes(o.Size), fmtEpoch(o.UpdatedAt),
					}
				} else {
					cells = []string{
						o.Href, strOr(o.Component, "—"), fmtEpochPtr(o.Dtstart), fmtEpochPtr(o.Dtend),
						strOr(o.EventStatus, "—"), yn(o.Rrule != nil), fmtEpoch(o.UpdatedAt),
					}
				}
				rows[i] = rowData{cells: cells, item: o}
			}
			return rows, page.NextCursor, nil
		},
		detail: func(item any) []kv {
			o := item.(coreapi.PimObject)
			kvs := []kv{
				{k: "href", v: o.Href},
				{k: "uid", v: o.UID},
				{k: "id", v: o.ID},
				{k: "etag", v: o.Etag},
				{k: "size", v: fmtBytes(o.Size)},
				{k: "content type", v: o.ContentType},
				{k: "component", v: strOr(o.Component, "—")},
			}
			if kind == coreapi.PimAddressbooks {
				kvs = append(kvs,
					kv{k: "name", v: strOr(o.VcardFn, "—")},
					kv{k: "email", v: strOr(o.VcardEmail, "—")},
				)
			} else {
				if coreapi.IsTaskObject(o) {
					// A to-do's one timestamp is a DEADLINE, and RFC 5545 §3.6.2
					// makes DTSTART optional — the commonest task has none — so
					// an event's start/end labels describe the wrong object. The
					// start row appears only when there is one, and STATUS moves
					// up because on a to-do it is the headline fact.
					if o.Dtstart != nil {
						kvs = append(kvs, kv{k: "start", v: fmtEpochPtr(o.Dtstart)})
					}
					kvs = append(kvs,
						kv{k: "due", v: fmtEpochPtr(o.Dtend)},
						kv{k: "status", v: strOr(o.EventStatus, "—")},
					)
				} else {
					kvs = append(kvs,
						kv{k: "start", v: fmtEpochPtr(o.Dtstart)},
						kv{k: "end", v: fmtEpochPtr(o.Dtend)},
						kv{k: "status", v: strOr(o.EventStatus, "—")},
					)
				}
				kvs = append(kvs,
					kv{k: "rrule", v: strOr(o.Rrule, "—")},
					kv{k: "organizer", v: strings.TrimPrefix(strOr(o.Organizer, "—"), "mailto:")},
					kv{k: "sequence", v: fmt.Sprintf("%d", o.Sequence)},
				)
				// Attendees belong to both: core schedules to-dos (RFC 5546 §3.4),
				// so an ASSIGNED task carries the same ORGANIZER/ATTENDEE pair an
				// invitation does.
				for i, at := range o.Attendees {
					label := at.Email
					if at.Partstat != "" {
						label += " (" + at.Partstat + ")"
					}
					kvs = append(kvs, kv{k: fmt.Sprintf("attendee %d", i+1), v: label})
				}
			}
			kvs = append(kvs,
				kv{k: "created", v: fmtEpoch(o.CreatedAt)},
				kv{k: "updated", v: fmtEpoch(o.UpdatedAt)},
			)
			return kvs
		},
		extra: func(ctx context.Context, c *coreapi.Client, item any) ([]kv, error) {
			o := item.(coreapi.PimObject)
			body, _, err := c.GetPimObject(ctx, scope, kind, col.ID, o.Href, "")
			if err != nil {
				return nil, err
			}
			defer body.Close()
			return pimContentKVs(body), nil
		},
	}
	desc.actions = []action{
		{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
			return pimObjectDeleteConfirm(ctx, ui, mbx, kind, col, item.(coreapi.PimObject))
		}},
	}
	if kind == coreapi.PimCalendars {
		desc.actions = append(desc.actions,
			action{
				key: "p", label: "p rsvp", needsRow: true,
				run: func(ctx context.Context, ui *Options, item any) pane {
					return pimRsvpFormPane(ctx, ui, mbx, col, item.(coreapi.PimObject))
				},
			},
			// Directly run rather than confirmed: completing a to-do is exactly
			// reversible (the same key reopens it), and a modal on the one
			// interaction a task list exists for would be noise. The flash says
			// which way it went, since the row scrolls away on refresh.
			action{
				key: "t", label: "t done/reopen", needsRow: true,
				do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
					return toggleTaskDone(ctx, c, scope, col, item.(coreapi.PimObject))
				},
			},
		)
	}
	return desc
}

// toggleTaskDone flips a to-do between complete and needs-action.
//
// It is a read-modify-write of the JSCalendar Task rather than a synthesized
// body: the stored document carries members nothing here models (recurrence,
// alarms, and every unmapped iCalendar property core kept through its escape
// hatches), and re-authoring it would drop them. The If-Match comes from the
// ETag of the very read that produced the document, so a concurrent DAV or JMAP
// write loses with a 412 instead of being silently overwritten.
func toggleTaskDone(ctx context.Context, c *coreapi.Client, scope coreapi.PimScope, col coreapi.PimCollection, o coreapi.PimObject) (string, error) {
	if !coreapi.IsTaskObject(o) {
		return "", fmt.Errorf("%s is not a to-do — only a VTODO can be completed", o.Href)
	}
	reopen := coreapi.TaskIsClosed(o)

	obj, err := c.GetPimObjectJSON(ctx, scope, coreapi.PimCalendars, col.ID, o.Href, "")
	if err != nil {
		return "", err
	}
	doc, err := coreapi.TaskDocumentOf(obj)
	if err != nil {
		return "", err
	}
	edits := coreapi.TaskEdits{Progress: "completed", ProgressSet: true}
	if reopen {
		edits.Progress = "needs-action"
	}
	edited, err := coreapi.ApplyTaskEdits(doc, edits)
	if err != nil {
		return "", err
	}
	if _, err := c.PutPimObjectJSON(ctx, scope, coreapi.PimCalendars, col.ID, o.Href, edited,
		coreapi.PimPutOpts{IfMatch: obj.Etag}); err != nil {
		return "", err
	}
	if reopen {
		return o.Href + " reopened (needs-action)", nil
	}
	return o.Href + " marked done", nil
}

// pimContentKVs renders a raw iCalendar/vCard stream as detail lines, bounded
// so a pathological object cannot flood the pane.
func pimContentKVs(body io.Reader) []kv {
	const maxLines = 120
	kvs := []kv{{}, {v: "Content"}}
	sc := bufio.NewScanner(io.LimitReader(body, 64<<10))
	n := 0
	truncated := false
	for sc.Scan() {
		if n >= maxLines {
			truncated = true
			break
		}
		kvs = append(kvs, kv{k: " ", v: sc.Text()})
		n++
	}
	// A read error or an over-long line ends Scan early; say so rather than
	// presenting a silently short object as the whole content.
	if !truncated && sc.Err() != nil {
		truncated = true
	}
	if truncated {
		kvs = append(kvs, kv{k: " ", v: "… truncated"})
	}
	return kvs
}

// pimSharesDesc lists one collection's sharing grants.
func pimSharesDesc(mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) resourceDesc {
	scope := pimScopeFor(mbx)
	caption := ""
	if col.Visibility == "private" {
		caption = "visibility is private — grants open nothing until it is shared or public (e on the " + pimNoun(kind) + ")"
	}
	return resourceDesc{
		key:     string(kind) + ":shares:" + col.ID,
		name:    "Shares — " + col.Name,
		caption: caption,
		columns: []column{
			{title: "SHAREE MAILBOX", flex: true},
			{title: "PERMISSION", width: 10},
			{title: "GRANTED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			shares, err := c.ListPimShares(ctx, scope, kind, col.ID)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(shares))
			for i, sh := range shares {
				rows[i] = rowData{
					cells: []string{sh.ShareeIdentityID, sh.Permission, fmtEpoch(sh.CreatedAt)},
					item:  sh,
				}
			}
			return rows, "", nil
		},
		actions: []action{
			{key: "a", label: "a add", run: func(ctx context.Context, ui *Options, _ any) pane {
				return pimShareFormPane(ctx, ui, mbx, kind, col)
			}},
			{key: "d", label: "d remove", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return pimShareRemoveConfirm(ctx, ui, mbx, kind, col, item.(coreapi.PimShare))
			}},
		},
	}
}

// pimTokensDesc lists one collection's read-only feed tokens.
func pimTokensDesc(mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) resourceDesc {
	scope := pimScopeFor(mbx)
	return resourceDesc{
		key:  string(kind) + ":tokens:" + col.ID,
		name: "Feed tokens — " + col.Name,
		columns: []column{
			{title: "ID", width: 26},
			{title: "LABEL", flex: true},
			{title: "USES", width: 6},
			{title: "LAST USED", width: 16},
			{title: "EXPIRES", width: 16},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			tokens, err := c.ListPimTokens(ctx, scope, kind, col.ID)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(tokens))
			for i, tk := range tokens {
				rows[i] = rowData{
					cells: []string{
						tk.ID, strOr(tk.Label, "—"), fmt.Sprintf("%d", tk.AccessCount),
						fmtEpochPtr(tk.LastAccessedAt), fmtEpochPtr(tk.ExpiresAt), fmtEpoch(tk.CreatedAt),
					},
					item: tk,
				}
			}
			return rows, "", nil
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return pimTokenFormPane(ctx, ui, mbx, kind, col)
			}},
			{key: "d", label: "d revoke", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return pimTokenRevokeConfirm(ctx, ui, mbx, kind, col, item.(coreapi.PimToken))
			}},
		},
	}
}

// --- forms & confirms ---

// pimCollectionFormPane creates or edits a calendar/addressbook. Edit sends an
// expectedVisibility CAS from the row it edited, and clears a nullable field
// when its input is emptied.
func pimCollectionFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col *coreapi.PimCollection) pane {
	scope := pimScopeFor(mbx)
	var name, displayName, color, description string
	visibility := "private"
	title := "New " + pimNoun(kind)
	if col != nil {
		name = col.Name
		displayName = strOr(col.DisplayName, "")
		color = strOr(col.Color, "")
		description = strOr(col.Description, "")
		visibility = col.Visibility
		title = "Edit " + pimNoun(kind) + " " + col.Name
	}
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Name").
				Description("URL slug — letters, digits, . _ ~ - (the DAV path segment)").
				Placeholder("work").Value(&name),
			huh.NewInput().Title("Display name").Placeholder("optional").Value(&displayName),
			huh.NewInput().Title("Color").Placeholder("optional — e.g. #0A84FF").Value(&color),
			huh.NewInput().Title("Description").Placeholder("optional").Value(&description),
			huh.NewSelect[string]().Title("Visibility").
				Description("shared/public is what makes grants and the public directory work").
				Options(
					huh.NewOption("private", "private"),
					huh.NewOption("shared", "shared"),
					huh.NewOption("public", "public"),
				).
				Value(&visibility),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return "", nil, errors.New("name is required")
		}
		if col == nil {
			created, err := c.CreatePimCollection(sctx, scope, kind, coreapi.PimCollectionCreateInput{
				Name:        trimmed,
				DisplayName: strings.TrimSpace(displayName),
				Color:       strings.TrimSpace(color),
				Description: strings.TrimSpace(description),
				Visibility:  visibility,
			})
			if err != nil {
				return "", nil, err
			}
			return pimNoun(kind) + " " + created.Name + " created (" + created.ID + ")", nil, nil
		}
		patch := map[string]any{
			"visibility":         visibility,
			"expectedVisibility": col.Visibility,
		}
		if trimmed != col.Name {
			patch["name"] = trimmed
		}
		setNullable := func(key, val string) {
			if v := strings.TrimSpace(val); v != "" {
				patch[key] = v
			} else {
				patch[key] = nil
			}
		}
		setNullable("displayName", displayName)
		setNullable("color", color)
		setNullable("description", description)
		updated, err := c.UpdatePimCollection(sctx, scope, kind, col.ID, patch)
		if err != nil {
			return "", nil, err
		}
		return pimNoun(kind) + " " + updated.Name + " updated", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: title, build: build, submit: submit})
}

func pimCollectionDeleteConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) pane {
	scope := pimScopeFor(mbx)
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete " + pimNoun(kind) + " " + col.Name,
		body: fmt.Sprintf("Delete this %s and ALL %d object(s) in it. DAV/JMAP clients will see "+
			"every member as destroyed. Not undoable.", pimNoun(kind), col.ObjectCount),
		verb: "delete " + pimNoun(kind),
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			res, err := c.DeletePimCollection(sctx, scope, kind, col.ID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s %s deleted (%d object(s) destroyed)", pimNoun(kind), col.Name, res.ObjectsDestroyed), nil
		},
	})
}

func pimObjectDeleteConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection, o coreapi.PimObject) pane {
	scope := pimScopeFor(mbx)
	body := "Delete this object. Not undoable."
	if kind == coreapi.PimCalendars {
		body = "Delete this event. If this mailbox is the ORGANIZER, a CANCEL is sent to every attendee. Not undoable."
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete " + o.Href,
		body:  body,
		verb:  "delete object",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			res, err := c.DeletePimObject(sctx, scope, kind, col.ID, o.Href, "", "")
			if err != nil {
				return "", err
			}
			return res.Href + " deleted", nil
		},
	})
}

// pimRsvpFormPane records the mailbox's participation status on an event and
// notifies the organizer (a local organizer's copy is patched, a remote one is
// mailed a METHOD:REPLY).
func pimRsvpFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, col coreapi.PimCollection, o coreapi.PimObject) pane {
	scope := pimScopeFor(mbx)
	partstat := "ACCEPTED"
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("RSVP — "+o.Href).
				Description("your reply is recorded on your copy and the organizer is told by exactly one route").
				Options(
					huh.NewOption("Accept", "ACCEPTED"),
					huh.NewOption("Decline", "DECLINED"),
					huh.NewOption("Tentative", "TENTATIVE"),
					huh.NewOption("Reset to needs-action", "NEEDS-ACTION"),
				).
				Value(&partstat),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		res, err := c.RespondPimObject(sctx, scope, col.ID, o.Href, partstat)
		if err != nil {
			return "", nil, err
		}
		flash := "RSVP " + res.Partstat
		switch {
		case res.OrganizerUpdated:
			flash += " — organizer's copy updated"
		case res.ReplySent:
			flash += " — reply mailed to the organizer"
		default:
			flash += " — recorded (organizer not notified)"
		}
		return flash, nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "RSVP — " + o.Href, build: build, submit: submit})
}

// pimShareFormPane grants a mailbox access to a collection. The sharee may be
// a mailbox id or an address (resolved through its route).
func pimShareFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) pane {
	scope := pimScopeFor(mbx)
	var sharee string
	permission := "read"
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Sharee").
				Description("mailbox id or address").
				Placeholder("bob@example.com").Value(&sharee),
			huh.NewSelect[string]().Title("Permission").
				Options(
					huh.NewOption("read", "read"),
					huh.NewOption("read-write", "read-write"),
				).
				Value(&permission),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		target := strings.TrimSpace(sharee)
		if target == "" {
			return "", nil, errors.New("sharee is required")
		}
		if strings.Contains(target, "@") {
			r, err := c.GetRoute(sctx, target)
			if err != nil {
				return "", nil, err
			}
			if r.DestinationType != "mailbox" || r.MailboxID == nil {
				return "", nil, fmt.Errorf("%s does not route to a mailbox (type %s)", target, r.DestinationType)
			}
			target = *r.MailboxID
		}
		sh, err := c.PutPimShare(sctx, scope, kind, col.ID, target, permission)
		if err != nil {
			return "", nil, err
		}
		flash := sh.Permission + " granted to " + sh.ShareeIdentityID
		if col.Visibility == "private" {
			flash += " — NOTE: visibility is private, the grant opens nothing yet"
		}
		return flash, nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "Share " + col.Name, build: build, submit: submit})
}

func pimShareRemoveConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection, sh coreapi.PimShare) pane {
	scope := pimScopeFor(mbx)
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Revoke access for " + sh.ShareeIdentityID,
		body:  "The sharee loses access to " + col.Name + " immediately (their subscription, if any, stops resolving).",
		verb:  "revoke share",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.DeletePimShare(sctx, scope, kind, col.ID, sh.ShareeIdentityID); err != nil {
				return "", err
			}
			return "share revoked", nil
		},
	})
}

// pimTokenExpiries is the feed-token lifetime vocabulary offered by the mint
// form; 0 = never expires.
var pimTokenExpiries = []struct {
	label string
	d     time.Duration
}{
	{"never expires", 0},
	{"7 days", 7 * 24 * time.Hour},
	{"30 days", 30 * 24 * time.Hour},
	{"90 days", 90 * 24 * time.Hour},
	{"1 year", 365 * 24 * time.Hour},
}

// pimTokenFormPane mints a feed token; the unauthenticated URL is revealed
// exactly once via a note pane (the API-key pattern).
func pimTokenFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection) pane {
	scope := pimScopeFor(mbx)
	var label string
	expiry := time.Duration(0)
	opts := make([]huh.Option[time.Duration], len(pimTokenExpiries))
	for i, e := range pimTokenExpiries {
		opts[i] = huh.NewOption(e.label, e.d)
	}
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Label").Placeholder("optional — e.g. team feed").Value(&label),
			huh.NewSelect[time.Duration]().Title("Expires").
				Description("anyone with the URL can read the whole "+pimNoun(kind)).
				Options(opts...).
				Value(&expiry),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		var expiresAt int64
		if expiry > 0 {
			expiresAt = time.Now().Add(expiry).Unix()
		}
		tk, err := c.CreatePimToken(sctx, scope, kind, col.ID, strings.TrimSpace(label), expiresAt)
		if err != nil {
			return "", nil, err
		}
		reveal := newNotePane("Feed token created", []string{
			pimNoun(kind) + " " + col.Name + " · " + strOr(tk.Label, "(no label)"),
			"",
			"Unauthenticated feed URL — shown ONCE, copy it now:",
			"",
			tk.URL,
			"",
			"expires: " + fmtEpochPtr(tk.ExpiresAt),
		})
		return "feed token created", reveal, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "New feed token — " + col.Name, build: build, submit: submit})
}

func pimTokenRevokeConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, kind coreapi.PimKind, col coreapi.PimCollection, tk coreapi.PimToken) pane {
	scope := pimScopeFor(mbx)
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Revoke feed token " + strOr(tk.Label, tk.ID),
		body:  "Its URL stops working immediately. Subscribed clients will start failing to refresh.",
		verb:  "revoke token",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.RevokePimToken(sctx, scope, kind, col.ID, tk.ID); err != nil {
				return "", err
			}
			return "feed token revoked", nil
		},
	})
}
