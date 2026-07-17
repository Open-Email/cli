//go:build live

// Live black-box e2e for the message CRUD surface, driven through the CLI —
// APPEND, the STORE/COPY/MOVE + last-label expunge PATCHes, and the DELETE
// variants (label-detach / expunge-to-trash / purge). Mirrors core's
// test/live/messages-crud.live.test.ts.
//
// Two guards the CLI applies client-side diverge from core's server-side answer,
// and are asserted as the CLI actually behaves (a usage exit, not an HTTP error):
// an invalid --flags value and combining --purge with --label.
package live

import (
	"strings"
	"testing"
)

type crudEnv struct {
	address string
	mailbox string
}

var crud *crudEnv

func crudSetup(t *testing.T) *crudEnv {
	t.Helper()
	requireLive(t)
	if crud != nil {
		return crud
	}
	domain := createDomain(t, domainOpts{})
	address := "crud@" + domain
	mailbox := createMailbox(t, address, mailboxOpts{})
	createRoute(t, address, mailbox)
	crud = &crudEnv{address: address, mailbox: mailbox}
	return crud
}

// crudAppend appends a raw MIME message (IMAP APPEND, bypasses routing) and
// requires success, returning the parsed AppendResult.
func crudAppend(t *testing.T, c *crudEnv, subject string, flags, label string) map[string]any {
	t.Helper()
	args := []string{"--json", "messages", "append", "-m", c.mailbox, "--delivery-id", deliveryID("append")}
	if label != "" {
		args = append(args, "--label", label)
	}
	if flags != "" {
		args = append(args, "--flags", flags)
	}
	out, errb, code := runAs(t, systemKey, mime(envFrom, c.address, subject, ""), args...)
	if code != 0 {
		t.Fatalf("append exit %d: %s", code, errb)
	}
	return decodeMap(t, []string{"append"}, out)
}

func TestLiveCRUD_Append(t *testing.T) {
	c := crudSetup(t)
	subject := "crud-append " + runID + " " + uniq()
	res := crudAppend(t, c, subject, "seen", "")
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("status = %q, want delivered", got)
	}
	if str(res, "messageId") == "" {
		t.Fatal("no messageId")
	}
	if _, ok := num(res, "uid"); !ok {
		t.Fatalf("missing uid: %v", res)
	}
	if _, ok := num(res, "uidValidity"); !ok {
		t.Fatalf("missing uidValidity: %v", res)
	}
	if dup, _ := res["duplicate"].(bool); dup {
		t.Fatal("fresh append reported duplicate:true")
	}

	got := sysJSON(t, "messages", "get", str(res, "messageId"), "-m", c.mailbox)
	if !containsFlag(got, "seen") {
		t.Fatalf("flags missing seen: %v", got["flags"])
	}
	if !contains(labelNames(got), "INBOX") {
		t.Fatalf("labels missing INBOX: %v", labelNames(got))
	}
}

func TestLiveCRUD_InvalidFlag(t *testing.T) {
	c := crudSetup(t)
	// The CLI validates flags client-side (a usage exit), so an invalid flag never
	// reaches core — this diverges from core's server-side 400 invalid_flag.
	_, errb, code := runAs(t, systemKey, mime(envFrom, c.address, "crud-badflag "+uniq(), ""),
		"messages", "append", "-m", c.mailbox, "--flags", "bogus")
	if code == 0 {
		t.Fatal("append with an invalid flag unexpectedly succeeded")
	}
	if !strings.Contains(errb, "invalid flag") {
		t.Fatalf("error = %q, want it to mention an invalid flag", strings.TrimSpace(errb))
	}
}

func TestLiveCRUD_PatchAndMove(t *testing.T) {
	c := crudSetup(t)
	subject := "crud-patch " + runID + " " + uniq()
	id := str(crudAppend(t, c, subject, "", ""), "messageId")

	ensureLabel(t, c.mailbox, "Work")

	// STORE + COPY: add a label, then set a flag (the CLI splits these into two
	// PATCHes — `messages label` and `messages flag`).
	labeled := sysJSON(t, "messages", "label", id, "--add", "Work", "-m", c.mailbox)
	if names := labelNames(labeled); !contains(names, "INBOX") || !contains(names, "Work") {
		t.Fatalf("after label add, names = %v, want INBOX+Work", names)
	}
	flagged := sysJSON(t, "messages", "flag", id, "--set", "flagged", "-m", c.mailbox)
	if !containsFlag(flagged, "flagged") {
		t.Fatalf("after flag set, flags = %v, want flagged", flagged["flags"])
	}

	// MOVE (add then remove) relabels in one call; additions apply before
	// removals, so this never passes through a zero-label state (no expunge).
	moved := sysJSON(t, "messages", "move", id, "--from", "INBOX", "--to", "Archive", "-m", c.mailbox)
	names := labelNames(moved)
	if !contains(names, "Archive") || !contains(names, "Work") || contains(names, "INBOX") {
		t.Fatalf("after move, names = %v, want Archive+Work and not INBOX", names)
	}
}

func TestLiveCRUD_LastLabelExpunge(t *testing.T) {
	c := crudSetup(t)
	id := str(crudAppend(t, c, "crud-expunge "+runID+" "+uniq(), "", ""), "messageId")

	// Removing the message's only label expunges it (the CLI renders the expunge
	// notice, not normal metadata).
	res := sysJSON(t, "messages", "label", id, "--remove", "INBOX", "-m", c.mailbox)
	if exp, _ := res["expunged"].(bool); !exp {
		t.Fatalf("removing last label did not expunge: %v", res)
	}
	if _, ok := num(res, "purgeAfter"); !ok {
		t.Fatalf("expunge notice missing purgeAfter: %v", res)
	}

	live := sysJSON(t, "messages", "list", "-m", c.mailbox)
	if findByID(live, "messages", id) != nil {
		t.Fatal("expunged message still live")
	}
	trash := sysJSON(t, "messages", "list", "-m", c.mailbox, "--trash")
	if tm := findByID(trash, "messages", id); tm == nil {
		t.Fatal("expunged message not in trash")
	} else if _, ok := num(tm, "purgeAfter"); !ok {
		t.Fatalf("trash row missing purgeAfter: %v", tm)
	}
}

func TestLiveCRUD_DetachLabel(t *testing.T) {
	c := crudSetup(t)
	id := str(crudAppend(t, c, "crud-detach "+runID+" "+uniq(), "", ""), "messageId")
	ensureLabel(t, c.mailbox, "Work")
	sysJSON(t, "messages", "label", id, "--add", "Work", "-m", c.mailbox)

	// Detaching Work leaves INBOX, so the message is NOT expunged.
	detach := sysJSON(t, "messages", "delete", id, "--label", "Work", "-m", c.mailbox)
	if got := str(detach, "removedFromLabel"); got != "Work" {
		t.Fatalf("removedFromLabel = %q, want Work", got)
	}
	if exp, _ := detach["expunged"].(bool); exp {
		t.Fatal("detach wrongly expunged the message")
	}
	live := sysJSON(t, "messages", "list", "-m", c.mailbox)
	if findByID(live, "messages", id) == nil {
		t.Fatal("message gone after a single-label detach")
	}

	// A non-existent label → unknown_label (404).
	expectFail(t, systemKey, "unknown_label", "messages", "delete", id, "--label", "NoSuch", "-m", c.mailbox)
}

func TestLiveCRUD_PurgeLive(t *testing.T) {
	c := crudSetup(t)
	id := str(crudAppend(t, c, "crud-purge "+runID+" "+uniq(), "", ""), "messageId")

	purge := sysJSON(t, "messages", "delete", id, "--purge", "--yes", "-m", c.mailbox)
	if d, _ := purge["deleted"].(bool); !d {
		t.Fatalf("purge did not report deleted: %v", purge)
	}
	if p, _ := purge["purged"].(bool); !p {
		t.Fatalf("purge did not report purged: %v", purge)
	}

	// Gone from BOTH live and trash (a purge bypasses the trash).
	live := sysJSON(t, "messages", "list", "-m", c.mailbox)
	if findByID(live, "messages", id) != nil {
		t.Fatal("purged message still live")
	}
	trash := sysJSON(t, "messages", "list", "-m", c.mailbox, "--trash")
	if findByID(trash, "messages", id) != nil {
		t.Fatal("purged message in trash")
	}

	// --purge + --label is rejected by the CLI client-side (a usage exit), before
	// any request — this diverges from core's server-side 400 label_purge_exclusive.
	_, errb, code := runAs(t, systemKey, "", "messages", "delete", id, "--purge", "--label", "Work", "-m", c.mailbox)
	if code == 0 {
		t.Fatal("--purge with --label unexpectedly succeeded")
	}
	if !strings.Contains(errb, "combined") {
		t.Fatalf("error = %q, want it to reject combining --purge and --label", strings.TrimSpace(errb))
	}
}

// ensureLabel creates a user label, tolerating a pre-existing one (the crud
// mailbox is shared across cases, so "Work" may already exist).
func ensureLabel(t *testing.T, mailbox, name string) {
	t.Helper()
	_, errb, code := runAs(t, systemKey, "", "labels", "create", name, "-m", mailbox)
	if code != 0 && !strings.Contains(errb, "label_exists") {
		t.Fatalf("create label %q exit %d: %s", name, code, errb)
	}
}

// containsFlag reports whether a message-metadata object carries the given flag.
func containsFlag(m map[string]any, want string) bool {
	for _, it := range arr(m, "flags") {
		if s, ok := it.(string); ok && s == want {
			return true
		}
	}
	return false
}
