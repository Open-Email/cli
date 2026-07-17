//go:build live

// Live black-box e2e for LABEL operations, driven through the CLI. Mirrors core's
// test/live/labels.live.test.ts (IMAP CREATE/RENAME/DELETE/LIST + EXPUNGE):
// create + duplicate rejection, the five system labels vs a user label, rename &
// delete protection for system labels, per-label UID-ascending listing, and a
// \Deleted-flagged label expunge moving an orphan to trash.
package live

import "testing"

type labelEnv struct {
	address string
	mailbox string
}

var labels *labelEnv

func labelSetup(t *testing.T) *labelEnv {
	t.Helper()
	requireLive(t)
	if labels != nil {
		return labels
	}
	domain := createDomain(t, domainOpts{})
	address := "labels@" + domain
	mailbox := createMailbox(t, address, mailboxOpts{})
	createRoute(t, address, mailbox)
	labels = &labelEnv{address: address, mailbox: mailbox}
	return labels
}

var systemLabels = []string{"INBOX", "Sent", "Drafts", "Trash", "Archive"}

// TestLiveLabels_Lifecycle exercises create → list → rename → delete as one
// ordered flow (the cases share the mailbox and depend on each other's state).
func TestLiveLabels_Lifecycle(t *testing.T) {
	l := labelSetup(t)
	m := l.mailbox

	// create + duplicate rejection
	created := sysJSON(t, "labels", "create", "Work", "-m", m)
	if got := str(created, "name"); got != "Work" {
		t.Fatalf("created name = %q, want Work", got)
	}
	if _, ok := num(created, "uidValidity"); !ok {
		t.Fatalf("created label missing uidValidity: %v", created)
	}
	expectFail(t, systemKey, "label_exists", "labels", "create", "Work", "-m", m)

	// list surfaces the system labels + the user label, with correct role-ness
	list := sysJSON(t, "labels", "list", "-m", m)
	byName := map[string]map[string]any{}
	for _, it := range arr(list, "labels") {
		if mm, ok := it.(map[string]any); ok {
			byName[str(mm, "name")] = mm
		}
	}
	for _, name := range append(append([]string{}, systemLabels...), "Work") {
		if byName[name] == nil {
			t.Fatalf("label %q missing from listing", name)
		}
	}
	for _, name := range systemLabels {
		if _, isNull := byName[name]["role"].(string); !isNull {
			t.Fatalf("system label %q has null role", name)
		}
	}
	if byName["Work"]["role"] != nil {
		t.Fatalf("user label Work has a non-null role: %v", byName["Work"]["role"])
	}

	// rename a user label; system labels protected; missing; onto a taken name
	renamed := sysJSON(t, "labels", "rename", "Work", "Projects", "-m", m)
	if r, _ := renamed["renamed"].(bool); !r {
		t.Fatalf("rename did not report renamed: %v", renamed)
	}
	expectFail(t, systemKey, "403", "labels", "rename", "INBOX", "Nope", "-m", m)
	expectFail(t, systemKey, "404", "labels", "rename", "DoesNotExist", "x", "-m", m)
	expectFail(t, systemKey, "409", "labels", "rename", "Projects", "INBOX", "-m", m)

	// delete a user label; system labels protected; missing
	sysJSON(t, "labels", "create", "Temp", "-m", m)
	del := sysJSON(t, "labels", "delete", "Temp", "-m", m, "--yes")
	if d, _ := del["deleted"].(bool); !d {
		t.Fatalf("delete did not report deleted: %v", del)
	}
	expectFail(t, systemKey, "403", "labels", "delete", "INBOX", "-m", m, "--yes")
	expectFail(t, systemKey, "404", "labels", "delete", "DoesNotExist", "-m", m, "--yes")
}

func TestLiveLabels_PerLabelMessages(t *testing.T) {
	l := labelSetup(t)
	subject := "label-list " + runID + " " + uniq()
	deliverOK(t, l.address, mime(envFrom, l.address, subject, ""), deliveryID("label-list"))

	list := sysJSON(t, "labels", "messages", "INBOX", "-m", l.mailbox)
	if _, ok := num(list, "uidValidity"); !ok {
		t.Fatalf("label listing missing uidValidity: %v", list)
	}
	var mine map[string]any
	var uids []float64
	for _, it := range arr(list, "messages") {
		mm, _ := it.(map[string]any)
		if str(mm, "subject") == subject {
			mine = mm
		}
		if u, ok := num(mm, "uid"); ok {
			uids = append(uids, u)
		}
	}
	if mine == nil {
		t.Fatal("delivered message not found in the INBOX label listing")
	}
	if _, ok := num(mine, "uid"); !ok {
		t.Fatalf("message missing a uid: %v", mine)
	}
	for i := 1; i < len(uids); i++ {
		if uids[i] <= uids[i-1] {
			t.Fatalf("UIDs not strictly ascending: %v", uids)
		}
	}

	expectFail(t, systemKey, "404", "labels", "messages", "NoSuchLabel", "-m", l.mailbox)
}

func TestLiveLabels_Expunge(t *testing.T) {
	l := labelSetup(t)
	subject := "label-expunge " + runID + " " + uniq()
	res := deliverOK(t, l.address, mime(envFrom, l.address, subject, ""), deliveryID("label-expunge"))
	msgID := str(res, "messageId")

	// Mark \Deleted (STORE) — still live, still in INBOX.
	sysJSON(t, "messages", "flag", msgID, "--set", "deleted", "-m", l.mailbox)

	// EXPUNGE INBOX detaches \Deleted messages; INBOX was the message's only
	// label, so it is orphaned → moved to trash.
	exp := sysJSON(t, "labels", "expunge", "INBOX", "-m", l.mailbox)
	if c, _ := num(exp, "messagesExpunged"); c < 1 {
		t.Fatalf("messagesExpunged = %v, want >= 1", exp["messagesExpunged"])
	}
	found := false
	for _, it := range arr(exp, "expunged") {
		if mm, ok := it.(map[string]any); ok && str(mm, "messageId") == msgID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expunged set does not include %s: %v", msgID, exp["expunged"])
	}

	// Gone from the live listing…
	live := sysJSON(t, "messages", "list", "-m", l.mailbox)
	if findByID(live, "messages", msgID) != nil {
		t.Fatal("expunged message still in live listing")
	}
	// …but sitting in the trash.
	trash := sysJSON(t, "messages", "list", "-m", l.mailbox, "--trash")
	if findByID(trash, "messages", msgID) == nil {
		t.Fatal("expunged message not in trash")
	}
}
