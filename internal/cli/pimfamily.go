package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// pimFamily parameterizes one command tree over core's two PIM path families —
// the same two-spellings-one-implementation shape core itself has.
type pimFamily struct {
	kind         coreapi.PimKind
	use          string
	aliases      []string
	noun         string // "calendar" | "addressbook"
	objectsAlias string // "events" | "contacts"
	ext          string // ".ics" | ".vcf" (help text only)
	jsonName     string // "JSCalendar" | "JSContact" — the JSON representation
	short        string
}

func newCalendarsCmd(a *app) *cobra.Command {
	return newPimFamilyCmd(a, pimFamily{
		kind:         coreapi.PimCalendars,
		use:          "calendars",
		aliases:      []string{"calendar", "cal"},
		noun:         "calendar",
		objectsAlias: "events",
		ext:          ".ics",
		jsonName:     "JSCalendar",
		short:        "Manage a mailbox's calendars (iCalendar objects, sharing, feeds)",
	})
}

func newAddressbooksCmd(a *app) *cobra.Command {
	return newPimFamilyCmd(a, pimFamily{
		kind:         coreapi.PimAddressbooks,
		use:          "addressbooks",
		aliases:      []string{"addressbook", "abook"},
		noun:         "addressbook",
		objectsAlias: "contacts",
		ext:          ".vcf",
		jsonName:     "JSContact",
		short:        "Manage a mailbox's addressbooks (vCard objects, sharing, feeds)",
	})
}

func newPimFamilyCmd(a *app, f pimFamily) *cobra.Command {
	var owner string
	cmd := &cobra.Command{
		Use:     f.use,
		Aliases: f.aliases,
		Short:   f.short,
	}
	addMailboxFlag(cmd, a)
	cmd.PersistentFlags().StringVar(&owner, "owner", "",
		"owner mailbox (id or address) of a "+f.noun+" shared with you; collections of another owner must be addressed by ULID")
	scope := func(cmd *cobra.Command, client *coreapi.Client) (coreapi.PimScope, error) {
		return a.pimScope(cmd.Context(), client, owner)
	}
	cmd.AddCommand(
		newPimColListCmd(a, f, scope),
		newPimColCreateCmd(a, f, scope),
		newPimColGetCmd(a, f, scope),
		newPimColUpdateCmd(a, f, scope),
		newPimColDeleteCmd(a, f, scope),
		newPimChangesCmd(a, f, scope),
		newPimExportCmd(a, f, scope),
		newPimImportCmd(a, f, scope),
		newPimObjectsCmd(a, f, scope),
		newPimSharesCmd(a, f, scope),
		newPimTokensCmd(a, f, scope),
	)
	if f.kind == coreapi.PimCalendars {
		cmd.AddCommand(newPimRespondCmd(a, scope), newInvitationsCmd(a), newPimTasksCmd(a, f, scope))
	}
	return cmd
}

// pimScopeFn resolves the acting/owner mailbox pair for one invocation.
type pimScopeFn func(cmd *cobra.Command, client *coreapi.Client) (coreapi.PimScope, error)

// pimScope resolves the acting mailbox (-m / profile default) and, when
// --owner names a different mailbox, addresses that owner's store while acting
// as the resolved mailbox (X-Acting-Mailbox) — the shared-collection path.
func (a *app) pimScope(ctx context.Context, client *coreapi.Client, owner string) (coreapi.PimScope, error) {
	acting, err := a.resolveMailbox(ctx, client, "")
	if err != nil {
		return coreapi.PimScope{}, err
	}
	if owner == "" {
		return coreapi.PimScope{MailboxID: acting}, nil
	}
	ownerID, err := a.resolveMailbox(ctx, client, owner)
	if err != nil {
		return coreapi.PimScope{}, err
	}
	return coreapi.PimScope{MailboxID: ownerID, Acting: acting}, nil
}

func newPimColListCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + f.use + " with visibility and object counts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			cols, err := client.ListPimCollections(cmd.Context(), s, f.kind)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{f.use: cols}, func(w io.Writer) {
				rows := make([][]string, 0, len(cols))
				for _, c := range cols {
					rows = append(rows, []string{
						c.Name, c.ID, strOr(c.DisplayName, "—"), c.Visibility,
						strOr(c.Role, "—"), fmt.Sprintf("%d", c.ObjectCount), fmtEpoch(c.CreatedAt),
					})
				}
				printTable(w, a.out, []string{"NAME", "ID", "DISPLAY", "VISIBILITY", "ROLE", "OBJECTS", "CREATED"}, rows)
			})
			return nil
		},
	}
}

func newPimColCreateCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var displayName, color, description, role, visibility string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a " + f.noun + " (name = URL slug: letters, digits, . _ ~ -)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			in := coreapi.PimCollectionCreateInput{
				Name: args[0], DisplayName: displayName, Color: color,
				Description: description, Visibility: visibility,
			}
			if cmd.Flags().Changed("role") {
				in.Role = &role
			}
			col, err := client.CreatePimCollection(cmd.Context(), s, f.kind, in)
			if err != nil {
				return err
			}
			a.out.Emit(col, func(w io.Writer) {
				a.out.Successf("Created %s %q (%s)", f.noun, col.Name, col.ID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&displayName, "display-name", "", "human display name")
	cmd.Flags().StringVar(&color, "color", "", "client hint, e.g. #0A84FF")
	cmd.Flags().StringVar(&description, "description", "", "free-text description")
	cmd.Flags().StringVar(&role, "role", "", "collection role (e.g. default, tasks)")
	cmd.Flags().StringVar(&visibility, "visibility", "", "private (default) | shared | public")
	return cmd
}

func newPimColGetCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	return &cobra.Command{
		Use:   "get <" + f.noun + ">",
		Short: "Show one " + f.noun + " (by ULID or name slug)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			col, err := client.GetPimCollection(cmd.Context(), s, f.kind, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(col, func(w io.Writer) {
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"ID", col.ID},
					{"Name", col.Name},
					{"Display name", strOr(col.DisplayName, "—")},
					{"Kind", col.Kind},
					{"Visibility", col.Visibility},
					{"Role", strOr(col.Role, "—")},
					{"Color", strOr(col.Color, "—")},
					{"Description", strOr(col.Description, "—")},
					{"Objects", fmt.Sprintf("%d", col.ObjectCount)},
					{"Sync token", col.SyncToken},
					{"Created", fmtEpoch(col.CreatedAt)},
				})
			})
			return nil
		},
	}
}

func newPimColUpdateCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var name, displayName, color, description, role, visibility, expectedVisibility string
	cmd := &cobra.Command{
		Use:   "update <" + f.noun + ">",
		Short: "Update a " + f.noun + " (rename, display properties, visibility)",
		Long: "Update a " + f.noun + ". Only flags you pass are sent; passing an empty value\n" +
			"clears a nullable field (display-name, color, description, role).\n" +
			"--expected-visibility guards a visibility change against a concurrent\n" +
			"change (409 visibility_conflict on a lost race).",
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
			patch := map[string]any{}
			setStr := func(flag, key, val string) {
				if !cmd.Flags().Changed(flag) {
					return
				}
				if val == "" {
					patch[key] = nil
				} else {
					patch[key] = val
				}
			}
			if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			setStr("display-name", "displayName", displayName)
			setStr("color", "color", color)
			setStr("description", "description", description)
			setStr("role", "role", role)
			if cmd.Flags().Changed("visibility") {
				patch["visibility"] = visibility
			}
			if cmd.Flags().Changed("expected-visibility") {
				patch["expectedVisibility"] = expectedVisibility
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass at least one field flag"))
			}
			col, err := client.UpdatePimCollection(cmd.Context(), s, f.kind, args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(col, func(w io.Writer) {
				a.out.Successf("Updated %s %q (%s)", f.noun, col.Name, col.ID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new URL slug (renames the DAV path; the ULID is stable)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "human display name (empty clears)")
	cmd.Flags().StringVar(&color, "color", "", "client color hint (empty clears)")
	cmd.Flags().StringVar(&description, "description", "", "description (empty clears)")
	cmd.Flags().StringVar(&role, "role", "", "collection role (empty clears)")
	cmd.Flags().StringVar(&visibility, "visibility", "", "private | shared | public")
	cmd.Flags().StringVar(&expectedVisibility, "expected-visibility", "", "current visibility this change assumes (CAS guard)")
	return cmd
}

func newPimColDeleteCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <" + f.noun + ">",
		Aliases: []string{"rm"},
		Short:   "Delete a " + f.noun + " and every object in it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete %s %q and ALL its objects?", f.noun, args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			res, err := client.DeletePimCollection(cmd.Context(), s, f.kind, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Deleted %s %q (%d object(s) destroyed)", f.noun, args[0], res.ObjectsDestroyed)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newPimChangesCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var since string
	var limit int
	cmd := &cobra.Command{
		Use:   "changes <" + f.noun + ">",
		Short: "Sync diff since an opaque token (omit --since for the initial listing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			ch, err := client.PimCollectionChanges(cmd.Context(), s, f.kind, args[0], since, limit)
			if err != nil {
				if ae, ok := coreapi.AsAPIError(err); ok && ae.Status == 410 {
					a.out.Warnf("sync token expired or invalid — resync from scratch (run without --since)")
				}
				return err
			}
			a.out.Emit(ch, func(w io.Writer) {
				rows := make([][]string, 0, len(ch.Changed)+len(ch.Deleted))
				for _, c := range ch.Changed {
					rows = append(rows, []string{"changed", c.Href, c.UID, c.Etag})
				}
				for _, href := range ch.Deleted {
					rows = append(rows, []string{"deleted", href, "—", "—"})
				}
				printTable(w, a.out, []string{"CHANGE", "HREF", "UID", "ETAG"}, rows)
				a.out.Msgf("sync token: %s", ch.SyncToken)
				if ch.Truncated {
					a.out.Msgf("more changes remain — re-run with --since %s", ch.SyncToken)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "opaque sync token from a previous run")
	cmd.Flags().IntVar(&limit, "limit", 0, "max changes to return (1–500)")
	return cmd
}

func newPimExportCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "export <" + f.noun + ">",
		Short: "Export the whole " + f.noun + " as one merged " + f.ext + " document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			body, _, err := client.ExportPimCollection(cmd.Context(), s, f.kind, args[0])
			if err != nil {
				return err
			}
			defer body.Close()
			return writeStream(a, body, outPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write to a file instead of stdout")
	return cmd
}

func newPimImportCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "import <" + f.noun + ">",
		Short: "Bulk-import a merged " + f.ext + " document (idempotent; hrefs derive from UIDs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			body, err := readRawInput(file)
			if err != nil {
				return err
			}
			res, err := client.ImportPimCollection(cmd.Context(), s, f.kind, args[0], body)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Imported: %d created, %d replaced, %d failed", res.CreatedCount, res.ReplacedCount, res.FailedCount)
				for _, it := range res.Items {
					if it.Status == "failed" {
						a.out.Warnf("  %s: %s", it.UID, it.Error)
					}
				}
			})
			if res.FailedCount > 0 {
				return silentExit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the document from a file (default: stdin)")
	return cmd
}

func newPimObjectsCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "objects",
		Aliases: []string{f.objectsAlias, "obj"},
		Short:   "Read and write the " + f.noun + "'s raw " + f.ext + " objects",
	}
	cmd.AddCommand(
		newPimObjListCmd(a, f, scope),
		newPimObjGetCmd(a, f, scope),
		newPimObjPutCmd(a, f, scope),
		newPimObjDeleteCmd(a, f, scope),
		newPimObjMoveCmd(a, f, scope),
	)
	return cmd
}

func newPimObjListCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var limit int
	var cursor, fields, uid, start, end, component string
	var expand, all bool
	short := "List objects"
	if f.kind == coreapi.PimCalendars {
		short = "List objects (optionally a time range, expanded into occurrences)"
	}
	cmd := &cobra.Command{
		Use:     "list <" + f.noun + ">",
		Aliases: []string{"ls"},
		Short:   short,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Every flag is parsed BEFORE the client is built, so a typo costs
			// nothing and never reaches core. --component in particular: core's
			// query is a zod enum over the upper-case spellings, so
			// `--component vtodo` comes back as a `validation_failed` naming a zod
			// path, which tells a user nothing about what to type instead.
			opts := coreapi.PimObjectListOpts{
				Limit: limit, Cursor: cursor, Fields: fields, UID: uid,
				Expand: expand,
			}
			if cmd.Flags().Changed("component") {
				normalized, cerr := coreapi.NormalizeComponent(component)
				if cerr != nil {
					return usageError(fmt.Errorf("--component: %w", cerr))
				}
				opts.Component = normalized
			}
			if cmd.Flags().Changed("start") {
				sec, perr := parseTimeArg(start)
				if perr != nil {
					return usageError(fmt.Errorf("--start: %w", perr))
				}
				opts.Start = &sec
			}
			if cmd.Flags().Changed("end") {
				sec, perr := parseTimeArg(end)
				if perr != nil {
					return usageError(fmt.Errorf("--end: %w", perr))
				}
				opts.End = &sec
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			var objs []coreapi.PimObject
			var window *coreapi.PimWindow
			next := ""
			for {
				page, lerr := client.ListPimObjects(cmd.Context(), s, f.kind, args[0], opts)
				if lerr != nil {
					return lerr
				}
				objs = append(objs, page.Objects...)
				if window == nil {
					window = page.Window
				}
				// Stall guard, same as coreapi.Depaginate's — a cursor that fails
				// to advance would loop forever and accumulate unboundedly. This
				// loop is hand-rolled (it must not drain without --all, and it
				// needs the trailing cursor and the window the helper discards),
				// so the guard has to be repeated here rather than inherited.
				if all && page.NextCursor != "" && page.NextCursor == opts.Cursor {
					return fmt.Errorf("pagination cursor did not advance (%q) — aborting", page.NextCursor)
				}
				next = page.NextCursor
				if !all || next == "" {
					break
				}
				opts.Cursor = next
			}
			payload := map[string]any{"objects": objs, "nextCursor": next}
			if window != nil {
				payload["window"] = window
			}
			a.out.Emit(payload, func(w io.Writer) {
				switch {
				case expand:
					printPimInstances(a, w, objs)
				case f.kind == coreapi.PimAddressbooks:
					printPimContacts(a, w, objs)
				case opts.Component == coreapi.TaskComponent:
					// The user asked for to-dos, so the table is a to-do's: DUE
					// rather than END, and the STATUS that says whether it is done.
					// Driven by the FLAG, not by the rows, so the shape of a
					// listing never depends on what happens to be in it.
					printPimTasks(a, w, objs)
				default:
					printPimEvents(a, w, objs)
				}
				if window != nil && window.Clamped {
					a.out.Msgf("range clamped to %s … %s", fmtEpoch(window.Start), fmtEpoch(window.End))
				}
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max objects per page (1–5000)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "resume from a previous nextCursor")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().StringVar(&fields, "fields", "", "meta (omit raw content) | full (default)")
	cmd.Flags().StringVar(&uid, "uid", "", "point lookup by iCalendar/vCard UID")
	if f.kind == coreapi.PimCalendars {
		cmd.Flags().StringVar(&start, "start", "", "range start (RFC3339, YYYY-MM-DD, or unix seconds)")
		cmd.Flags().StringVar(&end, "end", "", "range end, exclusive (RFC3339, YYYY-MM-DD, or unix seconds)")
		cmd.Flags().StringVar(&component, "component", "", "restrict to one kind: event | task | journal (VEVENT | VTODO | VJOURNAL)")
		cmd.Flags().BoolVar(&expand, "expand", false, "expand recurring objects into occurrences (requires --start and --end)")
	}
	return cmd
}

// printPimEvents renders a calendar listing that may hold either kind.
//
// One column serves two meanings because core's extract does: `dtend` is a
// VEVENT's end and a VTODO's DUE, so the header says END/DUE rather than
// picking one and lying about the other half of the calendar. STATUS is here
// for the same reason — without it a completed to-do and an open one render
// identically, and the COMP column is the only hint anything differs.
func printPimEvents(a *app, w io.Writer, objs []coreapi.PimObject) {
	rows := make([][]string, 0, len(objs))
	for _, o := range objs {
		rows = append(rows, []string{
			o.Href, strOr(o.Component, "—"), fmtEpochPtr(o.Dtstart), fmtEpochPtr(o.Dtend),
			strOr(o.EventStatus, "—"),
			truncate(strings.TrimPrefix(derefOr(o.Organizer, "—"), "mailto:"), 28),
			boolYN(o.Rrule != nil), fmtEpoch(o.UpdatedAt),
		})
	}
	printTable(w, a.out, []string{"HREF", "COMP", "START", "END/DUE", "STATUS", "ORGANIZER", "RECURS", "UPDATED"}, rows)
}

func printPimContacts(a *app, w io.Writer, objs []coreapi.PimObject) {
	rows := make([][]string, 0, len(objs))
	for _, o := range objs {
		rows = append(rows, []string{
			o.Href, truncate(derefOr(o.VcardFn, "—"), 30), derefOr(o.VcardEmail, "—"),
			fmt.Sprintf("%d", o.Size), fmtEpoch(o.UpdatedAt),
		})
	}
	printTable(w, a.out, []string{"HREF", "NAME", "EMAIL", "SIZE", "UPDATED"}, rows)
}

func printPimInstances(a *app, w io.Writer, objs []coreapi.PimObject) {
	var rows [][]string
	for _, o := range objs {
		if len(o.Instances) == 0 {
			// A non-recurring object inside the window still lists once.
			rows = append(rows, []string{fmtEpochPtr(o.Dtstart), fmtEpochPtr(o.Dtend), derefOr(o.EventStatus, "—"), o.Href, o.UID})
			continue
		}
		for _, in := range o.Instances {
			rows = append(rows, []string{fmtEpoch(in.Start), fmtEpoch(in.End), derefOr(in.Status, "—"), o.Href, o.UID})
		}
	}
	printTable(w, a.out, []string{"START", "END", "STATUS", "HREF", "UID"}, rows)
}

func newPimObjGetCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var uid, outPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get <" + f.noun + "> [href]",
		Short: "Fetch one object's raw " + f.ext + " text (by href, or --uid)",
		Long: "Fetch one object. By default this is the raw " + f.ext + " wire text.\n" +
			"--json asks for the same resource as " + f.jsonName + " instead, which is the\n" +
			"shape to edit programmatically — it reports `writable:false` when the object\n" +
			"can be read as JSON but never written back.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			href, err := pimHrefArg(args, uid)
			if err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			if asJSON {
				obj, jerr := client.GetPimObjectJSON(cmd.Context(), s, f.kind, args[0], href, uid)
				if jerr != nil {
					return jerr
				}
				a.out.Emit(obj, func(w io.Writer) {
					printPimObjectJSON(a, obj, f)
				})
				return nil
			}
			body, hdr, err := client.GetPimObject(cmd.Context(), s, f.kind, args[0], href, uid)
			if err != nil {
				return err
			}
			defer body.Close()
			if err := writeStream(a, body, outPath); err != nil {
				return err
			}
			if outPath != "" {
				a.out.Msgf("etag %s", strings.Trim(hdr.Get("ETag"), `"`))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&uid, "uid", "", "look up by UID instead of href")
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write to a file instead of stdout")
	cmd.Flags().BoolVar(&asJSON, "json", false, "fetch as "+f.jsonName+" instead of raw "+f.ext)
	return cmd
}

// printPimObjectJSON renders the JSON representation for a human: the data
// document itself, with the writability caveat stated rather than implied.
func printPimObjectJSON(a *app, obj *coreapi.PimObjectJSON, f pimFamily) {
	if obj.Data == nil {
		// Narrow, and worth naming: core maps a VEVENT to an Event, a VTODO to a
		// Task and a VCARD to a Card, so a null here means a VJOURNAL (which no
		// JSCalendar type models) or a body core could not parse — never "this
		// kind of object is unsupported".
		a.out.Warnf("no %s mapping for this object (a journal entry, or a body that did not parse) — the wire text is below", f.jsonName)
		fmt.Fprintln(os.Stdout, obj.Content)
		return
	}
	blob, err := json.MarshalIndent(obj.Data, "", "  ")
	if err != nil {
		a.out.Warnf("could not render the data document: %v", err)
		return
	}
	fmt.Fprintln(os.Stdout, string(blob))
	if !obj.Writable {
		a.out.Warnf("read-only: this object converts to %s but cannot be converted back, so an edit would be refused", f.jsonName)
	}
}

func newPimObjPutCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var file, ifMatch string
	var createOnly, asJSON bool
	cmd := &cobra.Command{
		Use:   "put <" + f.noun + "> <href>",
		Short: "Create or replace one object from raw " + f.ext + " text (or --json)",
		Long: "Create or replace one object from raw " + f.ext + " text (from --file or stdin).\n" +
			"--json takes a " + f.jsonName + " document instead; core converts it to wire text\n" +
			"before the ordinary write path runs, so ETags, If-Match and scheduling behave\n" +
			"identically either way.\n\n" +
			"--if-match guards a replace with the stored ETag (412 on mismatch);\n" +
			"--create-only (If-None-Match: *) refuses to overwrite an existing href.\n" +
			"On a calendar, an organizer's write fans out iTIP invitations to attendees.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			body, err := readRawInput(file)
			if err != nil {
				return err
			}
			opts := coreapi.PimPutOpts{IfMatch: ifMatch, IfNoneMatchStar: createOnly}
			var res *coreapi.PimPutResult
			if asJSON {
				var data map[string]any
				if jerr := json.Unmarshal(body, &data); jerr != nil {
					return usageError(fmt.Errorf("invalid %s JSON: %w", f.jsonName, jerr))
				}
				res, err = client.PutPimObjectJSON(cmd.Context(), s, f.kind, args[0], args[1], data, opts)
			} else {
				res, err = client.PutPimObject(cmd.Context(), s, f.kind, args[0], args[1], body, opts)
			}
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				verb := "Replaced"
				if res.Created {
					verb = "Created"
				}
				a.out.Successf("%s %s (etag %s)", verb, res.Href, res.Etag)
				if res.Unparsed {
					a.out.Warnf("stored verbatim but unparsable — range queries and scheduling cannot see inside it")
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the object from a file (default: stdin)")
	cmd.Flags().StringVar(&ifMatch, "if-match", "", "replace only if the stored ETag matches")
	cmd.Flags().BoolVar(&createOnly, "create-only", false, "fail (412) if the href already exists")
	cmd.Flags().BoolVar(&asJSON, "json", false, "the input is a "+f.jsonName+" document, not raw "+f.ext)
	return cmd
}

func newPimObjDeleteCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var uid, ifMatch string
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <" + f.noun + "> [href]",
		Aliases: []string{"rm"},
		Short:   "Delete one object (by href, or --uid); a calendar organizer's delete sends CANCEL",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			href, err := pimHrefArg(args, uid)
			if err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			what := href
			if uid != "" {
				what = "uid " + uid
			}
			if !yes && !confirm(fmt.Sprintf("Delete %s from %s %q?", what, f.noun, args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			res, err := client.DeletePimObject(cmd.Context(), s, f.kind, args[0], href, uid, ifMatch)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Deleted %s (uid %s)", res.Href, res.UID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&uid, "uid", "", "delete by UID instead of href")
	cmd.Flags().StringVar(&ifMatch, "if-match", "", "delete only if the stored ETag matches")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newPimObjMoveCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "move <" + f.noun + "> <href> <newHref>",
		Short: "Rename an object's href within its " + f.noun + " (DAV MOVE)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			res, err := client.MovePimObject(cmd.Context(), s, f.kind, args[0], args[1], args[2])
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Moved %s → %s", args[1], args[2])
			})
			return nil
		},
	}
	return cmd
}

// newPimRespondCmd takes no pimFamily because RSVP is calendars-only: it is
// registered under that guard in newPimFamilyCmd, and RespondPimObject
// addresses the calendars path family directly.
func newPimRespondCmd(a *app, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "respond <calendar> <href> <accepted|declined|tentative|needs-action>",
		Aliases: []string{"rsvp"},
		Short:   "RSVP to an invitation: patch your copy and tell the organizer",
		Long: "Record your participation status on an event in your calendar. Your copy is\n" +
			"patched, then the organizer is told by exactly one route: a local organizer's\n" +
			"copy is updated in place, a remote organizer is mailed a METHOD:REPLY.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			partstat, err := normalizePartstat(args[2])
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
			res, err := client.RespondPimObject(cmd.Context(), s, args[0], args[1], partstat)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("RSVP %s recorded on %s", res.Partstat, args[1])
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
	return cmd
}

func newPimSharesCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "Manage a " + f.noun + "'s sharing grants (owner only)",
	}
	cmd.AddCommand(
		newPimShareListCmd(a, f, scope),
		newPimShareSetCmd(a, f, scope),
		newPimShareRemoveCmd(a, f, scope),
	)
	return cmd
}

func newPimShareListCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	return &cobra.Command{
		Use:     "list <" + f.noun + ">",
		Aliases: []string{"ls"},
		Short:   "List sharing grants",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			shares, err := client.ListPimShares(cmd.Context(), s, f.kind, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"shares": shares}, func(w io.Writer) {
				rows := make([][]string, 0, len(shares))
				for _, sh := range shares {
					// The address when core knows one, the ULID otherwise — an
					// address-less identity is a real state here, not an error.
					rows = append(rows, []string{
						granteeLabel(sh.ShareeAddress, sh.ShareeMailboxID),
						sh.Permission,
						fmtEpoch(sh.CreatedAt),
					})
				}
				printTable(w, a.out, []string{"SHAREE", "PERMISSION", "GRANTED"}, rows)
			})
			return nil
		},
	}
}

func newPimShareSetCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var permission string
	cmd := &cobra.Command{
		Use:     "set <" + f.noun + "> <shareeMailbox>",
		Aliases: []string{"grant"},
		Short:   "Grant (or change) a mailbox's access",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if permission != "read" && permission != "read-write" {
				return usageError(fmt.Errorf("invalid --permission %q: expected read or read-write", permission))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			sharee, err := a.resolveMailbox(cmd.Context(), client, args[1])
			if err != nil {
				return err
			}
			sh, err := client.PutPimShare(cmd.Context(), s, f.kind, args[0], sharee, permission)
			if err != nil {
				return err
			}
			a.out.Emit(sh, func(w io.Writer) {
				a.out.Successf("Granted %s to %s", sh.Permission, granteeLabel(sh.ShareeAddress, sh.ShareeMailboxID))
				a.out.Msgf("the grant opens nothing by itself — the %s's visibility must also be shared or public", f.noun)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&permission, "permission", "read", "read | read-write")
	return cmd
}

func newPimShareRemoveCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <" + f.noun + "> <shareeMailbox>",
		Aliases: []string{"rm", "revoke"},
		Short:   "Revoke a mailbox's grant",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Revoke %s's access to %s %q?", args[1], f.noun, args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			sharee, err := a.resolveMailbox(cmd.Context(), client, args[1])
			if err != nil {
				return err
			}
			if err := client.DeletePimShare(cmd.Context(), s, f.kind, args[0], sharee); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "shareeMailboxId": sharee}, func(w io.Writer) {
				a.out.Successf("Revoked %s's access", sharee)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newPimTokensCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage a " + f.noun + "'s read-only feed tokens (owner only)",
	}
	cmd.AddCommand(
		newPimTokenListCmd(a, f, scope),
		newPimTokenCreateCmd(a, f, scope),
		newPimTokenRevokeCmd(a, f, scope),
	)
	return cmd
}

func newPimTokenListCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	return &cobra.Command{
		Use:     "list <" + f.noun + ">",
		Aliases: []string{"ls"},
		Short:   "List feed tokens (never the plaintext)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			tokens, err := client.ListPimTokens(cmd.Context(), s, f.kind, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"tokens": tokens}, func(w io.Writer) {
				rows := make([][]string, 0, len(tokens))
				for _, tk := range tokens {
					rows = append(rows, []string{
						tk.ID, strOr(tk.Label, "—"), fmt.Sprintf("%d", tk.AccessCount),
						fmtEpochPtr(tk.LastAccessedAt), fmtEpochPtr(tk.ExpiresAt), fmtEpoch(tk.CreatedAt),
					})
				}
				printTable(w, a.out, []string{"ID", "LABEL", "USES", "LAST USED", "EXPIRES", "CREATED"}, rows)
			})
			return nil
		},
	}
}

func newPimTokenCreateCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var label, expiresIn string
	cmd := &cobra.Command{
		Use:   "create <" + f.noun + ">",
		Short: "Mint a feed token — the unauthenticated subscribe URL is shown once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var expiresAt int64
			if expiresIn != "" {
				d, perr := parseLifetime(expiresIn)
				if perr != nil {
					return usageError(fmt.Errorf("--expires-in: %w", perr))
				}
				expiresAt = time.Now().Add(d).Unix()
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			tk, err := client.CreatePimToken(cmd.Context(), s, f.kind, args[0], label, expiresAt)
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(tk, nil)
				return nil
			}
			a.out.Successf("Created feed token %s", tk.ID)
			a.out.Warnf("This URL embeds the token and is shown only once — store it now.")
			fmt.Fprintln(os.Stdout, tk.URL)
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "human label for the token")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "lifetime, e.g. 720h or 90d (default: never expires)")
	return cmd
}

func newPimTokenRevokeCmd(a *app, f pimFamily, scope pimScopeFn) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <" + f.noun + "> <tokenId>",
		Aliases: []string{"rm", "delete"},
		Short:   "Revoke a feed token (its URL stops working immediately)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Revoke feed token %s?", args[1])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			s, err := scope(cmd, client)
			if err != nil {
				return err
			}
			if err := client.RevokePimToken(cmd.Context(), s, f.kind, args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "id": args[1]}, func(w io.Writer) {
				a.out.Successf("Revoked feed token %s", args[1])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// ── shared helpers ───────────────────────────────────────────────────────────

// pimHrefArg resolves the optional href argument: by-uid lookups may omit it
// (core still requires a placeholder segment in the route).
func pimHrefArg(args []string, uid string) (string, error) {
	if len(args) >= 2 {
		return args[1], nil
	}
	if uid == "" {
		return "", usageError(errors.New("pass an href, or --uid to address the object by UID"))
	}
	return "by-uid", nil
}

// readRawInput reads a small raw body wholesale from a file or stdin (PIM
// objects are text; core caps single objects at 1 MiB and imports at 16 MiB).
func readRawInput(path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	return io.ReadAll(os.Stdin)
}

// writeStream copies a response stream to a file or stdout.
func writeStream(a *app, body io.Reader, outPath string) error {
	var dst io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		dst = f
	}
	if _, err := io.Copy(dst, body); err != nil {
		return err
	}
	if outPath != "" {
		a.out.Successf("Wrote %s", outPath)
	}
	return nil
}

// parseTimeArg accepts unix seconds, RFC3339, or a bare date (UTC midnight).
func parseTimeArg(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty time")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Unix(), nil
	}
	return 0, fmt.Errorf("unparseable time %q (want RFC3339, YYYY-MM-DD, or unix seconds)", s)
}

// parseLifetime is time.ParseDuration plus a day suffix ("90d").
func parseLifetime(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("unparseable lifetime %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// normalizePartstat maps a friendly spelling to the RFC 5545 PARTSTAT value.
func normalizePartstat(s string) (string, error) {
	v := strings.ToUpper(strings.ReplaceAll(s, "_", "-"))
	switch v {
	case "ACCEPTED", "DECLINED", "TENTATIVE", "NEEDS-ACTION":
		return v, nil
	}
	return "", fmt.Errorf("invalid partstat %q: expected accepted, declined, tentative, or needs-action", s)
}
