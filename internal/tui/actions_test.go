package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/openemail/openemail-cli/internal/compose"
	"github.com/openemail/openemail-cli/internal/coreapi"
)

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"1024", 1024, false},
		{"1K", 1024, false},
		{"1kb", 1024, false},
		{"500M", 500 << 20, false},
		{"1.5G", 3 << 29, false}, // 1.5 * 2^30
		{"2 TB", 2 << 40, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5M", 0, true},
	}
	for _, c := range cases {
		got, err := parseBytes(c.in)
		if c.err != (err != nil) {
			t.Fatalf("parseBytes(%q) err = %v; want err=%v", c.in, err, c.err)
		}
		if err == nil && got != c.want {
			t.Fatalf("parseBytes(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}

func TestResolveDestNonMailbox(t *testing.T) {
	ctx := t.Context()
	// Non-mailbox types never touch the client.
	f, v, err := resolveDest(ctx, nil, "webhook", " https://x.example/hook ")
	if err != nil || f != "webhookUrl" || v != "https://x.example/hook" {
		t.Fatalf("webhook = %q %q %v", f, v, err)
	}
	f, v, err = resolveDest(ctx, nil, "group", "")
	if err != nil || f != "" || v != "" {
		t.Fatalf("group should have no destination field, got %q %q %v", f, v, err)
	}
	if _, _, err = resolveDest(ctx, nil, "remote", ""); err == nil {
		t.Fatalf("remote with empty target must error")
	}
	if _, _, err = resolveDest(ctx, nil, "bogus", "x"); err == nil {
		t.Fatalf("unknown type must error")
	}
	// A mailbox target that is already an id passes through without a lookup.
	f, v, err = resolveDest(ctx, nil, "mailbox", "01ABCDEF")
	if err != nil || f != "mailboxId" || v != "01ABCDEF" {
		t.Fatalf("mailbox id passthrough = %q %q %v", f, v, err)
	}
}

func TestConfirmFlow(t *testing.T) {
	ran := false
	p := newConfirmPane(t.Context(), &Options{}, confirmSpec{
		title: "Delete thing",
		body:  "gone",
		verb:  "delete",
		submit: func(context.Context, *coreapi.Client) (string, error) {
			ran = true
			return "thing deleted", nil
		},
	})
	p.setSize(80, 20)

	// esc cancels without running the mutation.
	_, cmd := p.update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("esc should produce a pop command")
	}
	if _, ok := cmd().(popPaneMsg); !ok {
		t.Fatalf("esc should pop, got %T", cmd())
	}
	if ran {
		t.Fatalf("esc must not run the mutation")
	}

	// y submits; the async cmd yields confirmDoneMsg; success pops with refresh.
	_, cmd = p.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatalf("y should produce the submit command")
	}
	var done confirmDoneMsg
	for _, m := range collectMsgs(cmd) {
		if d, ok := m.(confirmDoneMsg); ok {
			done = d
		}
	}
	if !ran || done.flash != "thing deleted" || done.err != nil {
		t.Fatalf("submit outcome: ran=%v done=%+v", ran, done)
	}
	_, cmd = p.update(done)
	pr, ok := cmd().(popRefreshMsg)
	if !ok || pr.flash != "thing deleted" {
		t.Fatalf("success should pop-refresh with the flash, got %#v", cmd())
	}
}

func TestScreenActionKeys(t *testing.T) {
	opened := ""
	desc := resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}},
		actions: []action{
			{key: "n", label: "n new", run: func(context.Context, *Options, any) pane {
				opened = "new"
				return newNotePane("x", nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(_ context.Context, _ *Options, item any) pane {
				opened = "edit:" + item.(string)
				return newNotePane("x", nil)
			}},
		},
	}
	s := newScreenPane(t.Context(), &Options{}, desc)
	s.setSize(80, 20)

	// Row-bound action with no rows: no-op.
	_, cmd := s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if opened != "" || cmd != nil {
		t.Fatalf("e with no rows should be a no-op, opened=%q", opened)
	}

	// Rowless action works regardless.
	_, cmd = s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if opened != "new" {
		t.Fatalf("n should open the create pane, opened=%q", opened)
	}
	if _, ok := cmd().(pushPaneMsg); !ok {
		t.Fatalf("action should push a pane")
	}

	// Row-bound action receives the selected item.
	s.all = []rowData{{cells: []string{"alpha"}, item: "alpha"}}
	s.applyFilter()
	_, _ = s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if opened != "edit:alpha" {
		t.Fatalf("e should edit the selection, opened=%q", opened)
	}

	// The status hints advertise the actions.
	if h := s.hints(); !contains(h, "n new") || !contains(h, "e edit") {
		t.Fatalf("hints missing actions: %q", h)
	}
}

func TestScreenRefreshMsgSetsFlashAndRefetches(t *testing.T) {
	fetches := 0
	desc := resourceDesc{
		name:    "Things",
		columns: []column{{title: "NAME", flex: true}},
		fetch: func(context.Context, *coreapi.Client, string) ([]rowData, string, error) {
			fetches++
			return []rowData{{cells: []string{"fresh"}, item: "fresh"}}, "", nil
		},
	}
	s := newScreenPane(t.Context(), &Options{}, desc)
	s.setSize(80, 20)
	_, cmd := s.update(refreshMsg{flash: "thing created"})
	if s.flash != "thing created" || !s.loading {
		t.Fatalf("refreshMsg should set flash and start loading")
	}
	for _, m := range collectMsgs(cmd) {
		if pm, ok := m.(pageMsg); ok {
			s.update(pm)
		}
	}
	if fetches != 1 || len(s.all) != 1 {
		t.Fatalf("refresh should refetch (fetches=%d rows=%d)", fetches, len(s.all))
	}
}

func TestFormOutcomes(t *testing.T) {
	var name string
	spec := formSpec{
		title: "New thing",
		build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(huh.NewInput().Title("Name").Value(&name)))
		},
		submit: func(context.Context, *coreapi.Client) (string, pane, error) {
			return "created", nil, nil
		},
	}
	p := newFormPane(t.Context(), &Options{}, spec)
	p.setSize(80, 24)

	// esc cancels.
	_, cmd := p.update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(popPaneMsg); !ok {
		t.Fatalf("esc should pop the form")
	}

	// An errored submit re-opens the form with the error shown and the bound
	// values intact.
	name = "typed-by-user"
	pn, _ := p.update(formDoneMsg{paneID: p.id, err: errBoom})
	p = pn.(*formPane)
	if p.errMsg != "boom" || p.submitting {
		t.Fatalf("error should surface and re-open: errMsg=%q submitting=%v", p.errMsg, p.submitting)
	}
	if name != "typed-by-user" {
		t.Fatalf("bound values must survive the rebuild")
	}

	// A successful submit pops with the flash.
	_, cmd = p.update(formDoneMsg{paneID: p.id, flash: "created"})
	pr, ok := cmd().(popRefreshMsg)
	if !ok || pr.flash != "created" {
		t.Fatalf("success should pop-refresh, got %#v", cmd())
	}

	// A submit with an after-pane pops the form then pushes it (token reveal).
	// It must NOT pop-refresh here — the after-pane refreshes the revealed
	// listing on dismiss instead (a refresh now would misroute its result to the
	// after-pane and leave the listing stale).
	_, cmd = p.update(formDoneMsg{paneID: p.id, flash: "created", after: newNotePane("reveal", nil)})
	msgs := collectMsgs(cmd)
	var sawPop, sawPush bool
	for _, m := range msgs {
		switch m.(type) {
		case popPaneMsg:
			sawPop = true
		case popRefreshMsg:
			t.Fatalf("after-pane submit must not pop-refresh (misroutes the fetch), got %#v", msgs)
		case pushPaneMsg:
			sawPush = true
		}
	}
	if !sawPop || !sawPush {
		t.Fatalf("after-pane submit should pop AND push, got %#v", msgs)
	}
}

// A reveal note (one-time token) sits over the listing of the resource just
// created. Its refetch was misrouted to the note while the note was on top, so
// dismissing the note must refresh the revealed listing — otherwise the new row
// only appears after a manual refresh (the reported credentials-list bug).
func TestNotePaneDismissRefreshesRevealedList(t *testing.T) {
	p := newNotePane("Credential created", []string{"token: abc123"})
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}} {
		_, cmd := p.update(key)
		if cmd == nil {
			t.Fatalf("%v should produce a command", key)
		}
		if _, ok := cmd().(popRefreshMsg); !ok {
			t.Fatalf("note dismiss (%v) should pop-refresh the revealed listing, got %#v", key, cmd())
		}
	}
}

var errBoom = errors.New("boom")

// collectMsgs runs a tea.Cmd (possibly a batch/sequence) and gathers every
// message it yields. tea.Sequence wraps its commands in an unexported
// sequenceMsg ([]tea.Cmd), so slices-of-cmds are expanded via reflection.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	var walk func(m tea.Msg)
	walk = func(m tea.Msg) {
		if m == nil {
			return
		}
		if batch, ok := m.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					walk(c())
				}
			}
			return
		}
		if v := reflect.ValueOf(m); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := 0; i < v.Len(); i++ {
				if c, ok := v.Index(i).Interface().(tea.Cmd); ok && c != nil {
					walk(c())
				}
			}
			return
		}
		out = append(out, m)
	}
	walk(cmd())
	return out
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestDiffLabels(t *testing.T) {
	in := diffLabels([]string{"INBOX", "Work"}, []string{"Work", "Archive"})
	if len(in.LabelsAdd) != 1 || in.LabelsAdd[0] != "Archive" {
		t.Fatalf("adds = %v", in.LabelsAdd)
	}
	if len(in.LabelsRemove) != 1 || in.LabelsRemove[0] != "INBOX" {
		t.Fatalf("removes = %v", in.LabelsRemove)
	}
	in = diffLabels([]string{"INBOX"}, []string{"INBOX"})
	if len(in.LabelsAdd)+len(in.LabelsRemove) != 0 {
		t.Fatalf("identical sets should diff to nothing: %+v", in)
	}
	// A pure move: the add and remove ride one PATCH (additions apply first
	// server-side, so the message never passes through zero labels).
	in = diffLabels([]string{"INBOX"}, []string{"Archive"})
	if len(in.LabelsAdd) != 1 || len(in.LabelsRemove) != 1 {
		t.Fatalf("move should be one add + one remove: %+v", in)
	}
}

func TestScreenDirectAction(t *testing.T) {
	fetches := 0
	desc := resourceDesc{
		name:    "Trash",
		columns: []column{{title: "SUBJECT", flex: true}},
		fetch: func(context.Context, *coreapi.Client, string) ([]rowData, string, error) {
			fetches++
			return nil, "", nil
		},
		actions: []action{
			{key: "u", label: "u restore", needsRow: true, do: func(_ context.Context, _ *coreapi.Client, item any) (string, error) {
				return "restored " + item.(string), nil
			}},
		},
	}
	s := newScreenPane(t.Context(), &Options{}, desc)
	s.setSize(80, 20)
	s.all = []rowData{{cells: []string{"m1"}, item: "m1"}}
	s.applyFilter()

	_, cmd := s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if cmd == nil {
		t.Fatalf("direct action should produce an async command")
	}
	var done actDoneMsg
	for _, m := range collectMsgs(cmd) {
		if d, ok := m.(actDoneMsg); ok {
			done = d
		}
	}
	if done.flash != "restored m1" || done.err != nil {
		t.Fatalf("direct action outcome: %+v", done)
	}
	// Completion sets the flash and refetches the listing.
	pn, cmd := s.update(done)
	s = pn.(*screenPane)
	if s.flash != "restored m1" || !s.loading {
		t.Fatalf("actDoneMsg should flash and reload: flash=%q loading=%v", s.flash, s.loading)
	}
	for _, m := range collectMsgs(cmd) {
		if pm, ok := m.(pageMsg); ok {
			s.update(pm)
		}
	}
	if fetches != 1 {
		t.Fatalf("expected one refetch, got %d", fetches)
	}
}

func TestMembersDescGatedToGroups(t *testing.T) {
	routes := routesDesc()
	var membersAction *action
	for i := range routes.actions {
		if routes.actions[i].key == "M" {
			membersAction = &routes.actions[i]
		}
	}
	if membersAction == nil {
		t.Fatalf("routes should have an M members action")
	}
	mbxID := "01X"
	nonGroup := coreapi.Route{Address: "a@b.test", DestinationType: "mailbox", MailboxID: &mbxID}
	if p := membersAction.run(t.Context(), &Options{}, nonGroup); p != nil {
		t.Fatalf("M on a non-group route must be a no-op")
	}
	group := coreapi.Route{Address: "team@b.test", DestinationType: "group"}
	p := membersAction.run(t.Context(), &Options{}, group)
	if p == nil || !contains(p.title(), "team@b.test") {
		t.Fatalf("M on a group should open its members screen, got %v", p)
	}
}

func TestTrashDescRowsAndRestoreLabel(t *testing.T) {
	addr := "alice@example.test"
	d := trashDesc(coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr})
	if !contains(d.name, addr) {
		t.Fatalf("trash title should carry the mailbox label: %q", d.name)
	}
	var restore *action
	for i := range d.actions {
		if d.actions[i].key == "u" {
			restore = &d.actions[i]
		}
	}
	if restore == nil || restore.do == nil || !restore.needsRow {
		t.Fatalf("trash should have a row-bound direct restore action")
	}
	// The detail renderer accepts an expunged row (labels always empty there;
	// the memberships live in ExpungedLabels).
	sub := "bye"
	kvs := d.detail(coreapi.ExpungedMessageMeta{
		MessageMeta:    coreapi.MessageMeta{ID: "01MSG", Subject: &sub, Size: 42},
		ExpungedAt:     100,
		PurgeAfter:     200,
		ExpungedLabels: []string{"INBOX", "Work"},
	})
	joined := ""
	for _, e := range kvs {
		joined += e.k + "=" + e.v + ";"
	}
	if !contains(joined, "bye") || !contains(joined, "INBOX, Work") {
		t.Fatalf("trash detail missing fields: %s", joined)
	}
}

func TestDeletedMailboxesDesc(t *testing.T) {
	// The Mailboxes screen exposes the deleted view as a rowless action.
	var open *action
	mbx := mailboxesDesc()
	for i := range mbx.actions {
		if mbx.actions[i].key == "D" {
			open = &mbx.actions[i]
		}
	}
	if open == nil || open.needsRow {
		t.Fatalf("mailboxes should have a rowless D deleted action")
	}
	p := open.run(t.Context(), &Options{}, nil)
	if p == nil || !contains(p.title(), "Deleted mailboxes") {
		t.Fatalf("D should open the deleted-mailboxes screen, got %v", p)
	}

	// A non-restorable tombstone is refused locally — no client call, so a nil
	// client must not be touched.
	d := deletedMailboxesDesc()
	var restore *action
	for i := range d.actions {
		if d.actions[i].key == "u" {
			restore = &d.actions[i]
		}
	}
	if restore == nil || restore.do == nil || !restore.needsRow {
		t.Fatalf("deleted mailboxes should have a row-bound direct restore action")
	}
	addr := "gone@example.test"
	_, err := restore.do(t.Context(), nil, coreapi.DeletedMailbox{ID: "01D", PrimaryAddress: &addr, Restorable: false})
	if err == nil || !contains(err.Error(), "not restorable") {
		t.Fatalf("non-restorable tombstone should be refused locally, got %v", err)
	}

	// Purge is a row-bound confirm-gated action (irreversible → modal, not a
	// direct do). It is offered even on a non-restorable tombstone (expedite a
	// window-elapsed store), so the confirm opens regardless of Restorable.
	var purge *action
	for i := range d.actions {
		if d.actions[i].key == "p" {
			purge = &d.actions[i]
		}
	}
	if purge == nil || purge.run == nil || purge.do != nil || !purge.needsRow {
		t.Fatalf("deleted mailboxes should have a row-bound confirm-gated purge action")
	}
	cp := purge.run(t.Context(), &Options{}, coreapi.DeletedMailbox{ID: "01D", PrimaryAddress: &addr, Restorable: false})
	if cp == nil || !contains(cp.title(), addr) {
		t.Fatalf("purge should open a confirm pane titled for the mailbox, got %v", cp)
	}
}

func TestComposeMessageShape(t *testing.T) {
	msg := string(compose.TextMessage("a@x.test", "b@y.test", "héllo", "line1\nline2"))
	for _, want := range []string{"From: a@x.test\r\n", "To: b@y.test\r\n", "MIME-Version: 1.0\r\n", "line1\r\nline2\r\n"} {
		if !contains(msg, want) {
			t.Fatalf("compose missing %q in:\n%s", want, msg)
		}
	}
	if !contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("non-ASCII subject should be Q-encoded:\n%s", msg)
	}
	if contains(msg, "\n\n") && !contains(msg, "\r\n\r\n") {
		t.Fatalf("bare LF blank line in composed message")
	}
}

func TestComposePaneTitleCarriesMailbox(t *testing.T) {
	addr := "alice@example.test"
	p := composeFormPane(t.Context(), &Options{}, coreapi.Mailbox{ID: "01M", PrimaryAddress: &addr})
	if !contains(p.title(), addr) {
		t.Fatalf("compose title should carry the sending mailbox, got %q", p.title())
	}
	if !p.capturesInput() {
		t.Fatalf("compose form must capture input")
	}
}

func TestKeyCreatePaneByPrincipal(t *testing.T) {
	// Account principals go straight to the form (their key is self-scoped).
	p := keyCreatePane(t.Context(), &Options{Role: coreapi.PrincipalAccount})
	if _, ok := p.(*formPane); !ok {
		t.Fatalf("account principal should get the form directly, got %T", p)
	}
	// System principals get the loader that fetches the account picker options.
	p = keyCreatePane(t.Context(), &Options{Role: coreapi.PrincipalSystem})
	if _, ok := p.(*loaderPane); !ok {
		t.Fatalf("system principal should get the accounts loader, got %T", p)
	}
}

func TestLoaderSwapsToBuiltPane(t *testing.T) {
	target := newNotePane("built", nil)
	l := newLoaderPane(t.Context(), &Options{}, "Loading", func(context.Context, *coreapi.Client) (pane, error) {
		return target, nil
	})
	l.setSize(80, 20)
	var done loadedPaneMsg
	for _, m := range collectMsgs(l.init()) {
		if d, ok := m.(loadedPaneMsg); ok {
			done = d
		}
	}
	if done.p != pane(target) || done.err != nil {
		t.Fatalf("loader outcome: %+v", done)
	}
	_, cmd := l.update(done)
	msgs := collectMsgs(cmd)
	var sawPop, sawPush bool
	for _, m := range msgs {
		switch v := m.(type) {
		case popPaneMsg:
			sawPop = true
		case pushPaneMsg:
			sawPush = v.p == pane(target)
		}
	}
	if !sawPop || !sawPush {
		t.Fatalf("loader should pop itself then push the built pane, got %#v", msgs)
	}
}
