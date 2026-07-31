package tui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func pimMbx() coreapi.Mailbox {
	addr := "alice@x.test"
	return coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr}
}

// The mailboxes screen drills into calendars with c and addressbooks with b.
func TestMailboxPimActionsOpenScreens(t *testing.T) {
	acts := map[string]string{"c": "Calendars", "b": "Addressbooks"}
	for key, want := range acts {
		var found *action
		desc := mailboxesDesc()
		for i := range desc.actions {
			if desc.actions[i].key == key {
				found = &desc.actions[i]
			}
		}
		if found == nil || !found.needsRow || found.run == nil {
			t.Fatalf("mailboxes should have a row-bound %s action", key)
		}
		p := found.run(context.Background(), &Options{}, pimMbx())
		if p == nil || !contains(p.title(), want) || !contains(p.title(), "alice@x.test") {
			t.Fatalf("%s should open the %s screen for the mailbox, got %v", key, want, p)
		}
	}
}

// Collection rows render both families from the family-keyed listing; the
// screen carries the full action set.
func TestPimCollectionsDescRowsAndActions(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/calendars") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"calendars":[
			{"id":"01C","kind":"calendar","name":"work","displayName":"Work","color":null,"description":null,"visibility":"shared","role":null,"syncToken":"oe-sync:1:5","createdAt":100,"objectCount":3},
			{"id":"01D","kind":"calendar","name":"default","displayName":"Calendar","color":null,"description":null,"visibility":"private","role":"default","syncToken":"oe-sync:1:1","createdAt":90,"objectCount":0}
		]}`))
	})
	defer done()

	d := pimCollectionsDesc(pimMbx(), coreapi.PimCalendars)
	rows, next, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "" || len(rows) != 2 {
		t.Fatalf("want 2 unpaginated rows, got %d (next %q)", len(rows), next)
	}
	if rows[0].cells[0] != "work" || rows[0].cells[2] != "shared" || rows[0].cells[4] != "3" {
		t.Fatalf("row0 cells wrong: %v", rows[0].cells)
	}
	if rows[1].cells[3] != "default" {
		t.Fatalf("role cell wrong: %v", rows[1].cells)
	}

	keys := map[string]bool{}
	for _, a := range d.actions {
		keys[a.key] = true
	}
	for _, k := range []string{"n", "e", "d", "S", "T", "i"} {
		if !keys[k] {
			t.Fatalf("collections screen missing %q action (have %v)", k, keys)
		}
	}
	if d.open == nil {
		t.Fatal("enter should open the objects listing")
	}
	p := d.open(context.Background(), &Options{}, rows[0].item)
	if p == nil || !contains(p.title(), "work") {
		t.Fatalf("open should title for the collection, got %v", p)
	}
}

// Objects rows differ per family: scheduling columns for calendars, vCard
// extracts for addressbooks; pagination relays the cursor.
func TestPimObjectsDescRows(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fields"); got != "meta" {
			t.Fatalf("listing should ask fields=meta, got %q", got)
		}
		if strings.Contains(r.URL.Path, "/calendars/") {
			w.Write([]byte(`{"objects":[
				{"id":"01O","href":"standup.ics","uid":"u1","etag":"e1","size":812,"contentType":"text/calendar; charset=utf-8","component":"VEVENT","dtstart":1751000000,"dtend":1751003600,"rrule":"FREQ=WEEKLY","organizer":"mailto:boss@x.test","attendees":null,"sequence":0,"eventStatus":null,"transp":null,"vcardFn":null,"vcardEmail":null,"vcardN":null,"createdAt":100,"updatedAt":100}
			],"nextCursor":"01O"}`))
			return
		}
		w.Write([]byte(`{"objects":[
			{"id":"01P","href":"alice.vcf","uid":"u2","etag":"e2","size":240,"contentType":"text/vcard; charset=utf-8","component":"VCARD","dtstart":null,"dtend":null,"rrule":null,"organizer":null,"attendees":null,"sequence":0,"eventStatus":null,"transp":null,"vcardFn":"Alice A.","vcardEmail":"alice@x.test","vcardN":null,"createdAt":100,"updatedAt":100}
		]}`))
	})
	defer done()

	col := coreapi.PimCollection{ID: "01C", Name: "work"}
	cal := pimObjectsDesc(pimMbx(), coreapi.PimCalendars, col)
	rows, next, err := cal.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("calendar fetch: %v", err)
	}
	if next != "01O" {
		t.Fatalf("cursor should relay, got %q", next)
	}
	if rows[0].cells[0] != "standup.ics" || rows[0].cells[1] != "VEVENT" || rows[0].cells[4] != "yes" {
		t.Fatalf("calendar cells wrong: %v", rows[0].cells)
	}

	ab := pimObjectsDesc(pimMbx(), coreapi.PimAddressbooks, coreapi.PimCollection{ID: "01B", Name: "default"})
	rows, _, err = ab.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("addressbook fetch: %v", err)
	}
	if rows[0].cells[1] != "Alice A." || rows[0].cells[2] != "alice@x.test" {
		t.Fatalf("addressbook cells wrong: %v", rows[0].cells)
	}
}

// The RSVP action exists on calendars only; delete opens a confirm whose copy
// warns about the organizer CANCEL fan-out.
func TestPimObjectsActionsPerFamily(t *testing.T) {
	col := coreapi.PimCollection{ID: "01C", Name: "work"}
	obj := coreapi.PimObject{PimObjectMeta: coreapi.PimObjectMeta{Href: "evt.ics", UID: "u1"}}

	cal := pimObjectsDesc(pimMbx(), coreapi.PimCalendars, col)
	var rsvp, del *action
	for i := range cal.actions {
		switch cal.actions[i].key {
		case "p":
			rsvp = &cal.actions[i]
		case "d":
			del = &cal.actions[i]
		}
	}
	if rsvp == nil || !rsvp.needsRow {
		t.Fatal("calendars need a row-bound p rsvp action")
	}
	if p := rsvp.run(context.Background(), &Options{}, obj); p == nil || !contains(p.title(), "evt.ics") {
		t.Fatalf("rsvp should open a form titled for the object, got %v", p)
	}
	if del == nil {
		t.Fatal("objects need a d delete action")
	}

	ab := pimObjectsDesc(pimMbx(), coreapi.PimAddressbooks, col)
	for _, a := range ab.actions {
		if a.key == "p" {
			t.Fatal("addressbooks must not offer rsvp")
		}
	}
}

// The shares screen warns when visibility is private (grants open nothing).
func TestPimSharesDescCaption(t *testing.T) {
	private := pimSharesDesc(pimMbx(), coreapi.PimCalendars, coreapi.PimCollection{ID: "01C", Name: "work", Visibility: "private"})
	if private.caption == "" || !contains(private.caption, "private") {
		t.Fatalf("private collection should carry the visibility caveat, got %q", private.caption)
	}
	shared := pimSharesDesc(pimMbx(), coreapi.PimCalendars, coreapi.PimCollection{ID: "01C", Name: "work", Visibility: "shared"})
	if shared.caption != "" {
		t.Fatalf("shared collection needs no caveat, got %q", shared.caption)
	}
}

// Token rows render the one-time-URL lifecycle columns.
func TestPimTokensDescRows(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tokens":[{"id":"01T","collectionId":"01C","kind":"calendar","label":"team","expiresAt":null,"accessCount":7,"lastAccessedAt":200,"createdAt":100}]}`))
	})
	defer done()
	d := pimTokensDesc(pimMbx(), coreapi.PimCalendars, coreapi.PimCollection{ID: "01C", Name: "work"})
	rows, _, err := d.fetch(context.Background(), c, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if rows[0].cells[1] != "team" || rows[0].cells[2] != "7" {
		t.Fatalf("token cells wrong: %v", rows[0].cells)
	}
}

// pimContentKVs bounds pathological bodies instead of flooding the pane.
func TestPimContentKVsBounds(t *testing.T) {
	kvs := pimContentKVs(strings.NewReader("BEGIN:VCALENDAR\nEND:VCALENDAR\n"))
	// spacer + "Content" header + 2 lines
	if len(kvs) != 4 || kvs[2].v != "BEGIN:VCALENDAR" {
		t.Fatalf("content kvs wrong: %+v", kvs)
	}
	long := strings.Repeat("X:1\n", 500)
	kvs = pimContentKVs(strings.NewReader(long))
	if len(kvs) > 130 || kvs[len(kvs)-1].v != "… truncated" {
		t.Fatalf("long content should truncate, got %d lines (last %q)", len(kvs), kvs[len(kvs)-1].v)
	}
}
