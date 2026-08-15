//go:build live

// Live black-box e2e for the CLI against a DEPLOYED core — the PRE-LAUNCH §7
// smoke flows, driven entirely through the openemail binary. Mirrors core's
// test/live/e2e.live.test.ts, asserting on the CLI's `--json` payloads, exit
// codes, and rendered error codes rather than raw HTTP.
package live

import (
	"strings"
	"testing"
	"time"
)

// e2eCtx is the one shared mailbox/domain/credential provisioned once for the
// whole e2e file (Go compiles the package into a single test binary, so the
// setup is done lazily on first use and reused across cases).
type e2eEnv struct {
	domain   string
	address  string
	mailbox  string
	credUser string
	credPass string
}

var e2e *e2eEnv

func e2eSetup(t *testing.T) *e2eEnv {
	t.Helper()
	requireLive(t)
	if e2e != nil {
		return e2e
	}
	domain := createDomain(t, domainOpts{})
	address := "alice@" + domain
	mailbox := createMailbox(t, address, mailboxOpts{})
	createRoute(t, address, mailbox)
	user, pass := createAppPassword(t, mailbox)
	e2e = &e2eEnv{domain: domain, address: address, mailbox: mailbox, credUser: user, credPass: pass}
	return e2e
}

func TestLiveE2E_Health(t *testing.T) {
	requireLive(t)
	// `status` probes health + resolves the bearer — the user-facing health check.
	m := sysJSON(t, "status")
	if healthy, _ := m["healthy"].(bool); !healthy {
		t.Fatalf("core not healthy: %v", m)
	}
	if got := str(m, "principal"); got != "system" {
		t.Fatalf("principal = %q, want system", got)
	}
}

func TestLiveE2E_InboundAndRaw(t *testing.T) {
	c := e2eSetup(t)
	subject := "inbound " + runID + " " + uniq()
	res := deliverOK(t, c.address, mime(envFrom, c.address, subject, ""), deliveryID("inbound"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("status = %q, want delivered", got)
	}
	msgID := str(res, "messageId")
	if msgID == "" {
		t.Fatal("no messageId")
	}

	// The raw MIME streams back and still carries the subject.
	out, errb, code := runAs(t, systemKey, "", "messages", "raw", msgID, "-m", c.mailbox)
	if code != 0 {
		t.Fatalf("messages raw exit %d: %s", code, errb)
	}
	if !strings.Contains(out, subject) {
		t.Fatalf("raw body missing subject %q", subject)
	}
}

func TestLiveE2E_VerifyLogin(t *testing.T) {
	c := e2eSetup(t)
	// The app password verifies and reports sendability (admin, system-only).
	m := sysJSON(t, "admin", "verify-login", c.credUser, "--password", c.credPass)
	if got := str(m, "mailboxId"); got != c.mailbox {
		t.Fatalf("mailboxId = %q, want %q", got, c.mailbox)
	}
	if send, _ := m["canSend"].(bool); !send {
		t.Fatalf("canSend = false, want true: %v", m)
	}
	permitted := make([]string, 0)
	for _, it := range arr(m, "permittedFrom") {
		if s, ok := it.(string); ok {
			permitted = append(permitted, s)
		}
	}
	if !contains(permitted, c.credUser) {
		t.Fatalf("permittedFrom %v does not contain %q", permitted, c.credUser)
	}

	// A wrong password is rejected (401 invalid_credentials → non-zero exit).
	_, _, code := runAs(t, systemKey, "", "admin", "verify-login", c.credUser, "--password", "not-the-password")
	if code == 0 {
		t.Fatal("verify-login accepted a wrong password")
	}
}

func TestLiveE2E_DuplicateDelivery(t *testing.T) {
	c := e2eSetup(t)
	id := deliveryID("dup")
	body := mime(envFrom, c.address, "dup "+id, "")

	first := deliverOK(t, c.address, body, id)
	if dup, _ := first["duplicate"].(bool); dup {
		t.Fatal("first delivery reported duplicate:true")
	}
	firstID := str(first, "messageId")

	second := deliverOK(t, c.address, body, id)
	if dup, _ := second["duplicate"].(bool); !dup {
		t.Fatalf("second delivery reported duplicate:false: %v", second)
	}
	if got := str(second, "messageId"); got != firstID {
		t.Fatalf("duplicate messageId = %q, want %q", got, firstID)
	}
}

func TestLiveE2E_OutboundSentDedup(t *testing.T) {
	c := e2eSetup(t)
	subject := "outbound " + runID + " " + uniq()
	external := "rcpt@external-" + runID + ".invalid"
	// Identical bytes across both submissions → identical blob hash → the Sent
	// copy (keyed sent:<hash>) collapses to one entry. Build the body once.
	body := mime(c.address, external, subject, "")

	submit := func() map[string]any {
		out, errb, code := runAs(t, systemKey, body, "--json", "send",
			"--from", c.address, "--to", external,
			"--envelope-from", c.address, "--envelope-to", external,
			"--save", "--delivery-id", deliveryID("out"))
		if code != 0 {
			t.Fatalf("send exit %d: %s", code, errb)
		}
		return decodeMap(t, []string{"send"}, out)
	}

	if got := str(submit(), "status"); got != "queued" {
		t.Fatalf("first submit status = %q, want queued", got)
	}
	if got := str(submit(), "status"); got != "queued" {
		t.Fatalf("second submit status = %q, want queued", got)
	}

	sent := sysJSON(t, "messages", "list", "-m", c.mailbox, "--label", "Sent")
	n := 0
	for _, s := range subjectsOf(sent, "messages") {
		if s == subject {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Sent copies with subject %q = %d, want 1", subject, n)
	}
	t.Logf("[NOTE] relay actually receiving %s is external and not observable — the queued status only guarantees durable staging + enqueue", external)
}

func TestLiveE2E_GroupExpansion(t *testing.T) {
	c := e2eSetup(t)
	addrA := "member-a@" + c.domain
	addrB := "member-b@" + c.domain
	group := "team@" + c.domain
	mbA := createMailbox(t, addrA, mailboxOpts{})
	createRoute(t, addrA, mbA)
	mbB := createMailbox(t, addrB, mailboxOpts{})
	createRoute(t, addrB, mbB)
	createGroupRoute(t, group, []string{addrA, addrB})

	subject := "group " + runID + " " + uniq()
	res := deliverOK(t, group, mime(envFrom, group, subject, ""), deliveryID("group"))
	if got := str(res, "status"); got != "queued" {
		t.Fatalf("group delivery status = %q, want queued", got)
	}

	for _, box := range []struct{ id, addr string }{{mbA, addrA}, {mbB, addrB}} {
		poll(t, "group message in "+box.addr, 45*time.Second, func() bool {
			list := sysJSON(t, "messages", "list", "-m", box.id)
			for _, it := range arr(list, "messages") {
				mm, _ := it.(map[string]any)
				if str(mm, "subject") == subject {
					if metaField(mm, "listAddress") != group {
						t.Fatalf("listAddress = %q, want %q", metaField(mm, "listAddress"), group)
					}
					return true
				}
			}
			return false
		})
	}
}

func TestLiveE2E_SearchAndReindex(t *testing.T) {
	c := e2eSetup(t)
	token := "zqx" + runID + "needle"
	subject := "search " + token
	deliverOK(t, c.address, mime(envFrom, c.address, subject, "locate the "+token+" within"), deliveryID("search"))

	poll(t, "search indexing", 60*time.Second, func() bool {
		res := sysJSON(t, "search", token, "-m", c.mailbox)
		return contains(subjectsOf(res, "results"), subject)
	})

	re := sysJSON(t, "admin", "reindex", c.mailbox)
	if _, ok := num(re, "enqueued"); !ok {
		t.Fatalf("reindex enqueued not a number: %v", re)
	}
}

func TestLiveE2E_TrashLifecycle(t *testing.T) {
	c := e2eSetup(t)
	subject := "trash " + runID + " " + uniq()
	res := deliverOK(t, c.address, mime(envFrom, c.address, subject, ""), deliveryID("trash"))
	msgID := str(res, "messageId")

	rm := sysJSON(t, "messages", "delete", msgID, "-m", c.mailbox)
	if exp, _ := rm["expunged"].(bool); !exp {
		t.Fatalf("delete did not expunge: %v", rm)
	}

	// Gone from the live listing…
	live := sysJSON(t, "messages", "list", "-m", c.mailbox)
	if findByID(live, "messages", msgID) != nil {
		t.Fatal("expunged message still in live listing")
	}
	// …restorable in the trash, stamped with a purge horizon.
	trash := sysJSON(t, "messages", "list", "-m", c.mailbox, "--trash")
	tm := findByID(trash, "messages", msgID)
	if tm == nil {
		t.Fatal("message not in trash")
	}
	if _, ok := num(tm, "purgeAfter"); !ok {
		t.Fatalf("trash row missing purgeAfter: %v", tm)
	}

	purge := sysJSON(t, "messages", "trash", "empty", "-m", c.mailbox, "--yes")
	if p, _ := num(purge, "purgedCount"); p < 1 {
		t.Fatalf("purgedCount = %v, want >= 1", purge["purgedCount"])
	}
	after := sysJSON(t, "messages", "list", "-m", c.mailbox, "--trash")
	if findByID(after, "messages", msgID) != nil {
		t.Fatal("message still in trash after empty")
	}
}

func TestLiveE2E_RestoreMessage(t *testing.T) {
	c := e2eSetup(t)
	token := "rzx" + runID + "revive"
	subject := "restore " + token
	res := deliverOK(t, c.address, mime(envFrom, c.address, subject, "bring back the "+token), deliveryID("restore"))
	msgID := str(res, "messageId")

	before := sysJSON(t, "messages", "get", msgID, "-m", c.mailbox)
	uidBefore, ok := labelUID(before, "INBOX")
	if !ok {
		t.Fatalf("message not in INBOX before delete: %v", before)
	}

	sysJSON(t, "messages", "delete", msgID, "-m", c.mailbox)

	restored := sysJSON(t, "messages", "restore", msgID, "-m", c.mailbox)
	if r, _ := restored["restored"].(bool); !r {
		t.Fatalf("restore did not report restored: %v", restored)
	}
	msg, _ := restored["message"].(map[string]any)
	uidAfter, ok := labelUID(msg, "INBOX")
	if !ok {
		t.Fatalf("restored message not in INBOX: %v", msg)
	}
	if uidAfter <= uidBefore {
		t.Fatalf("restore UID %v not greater than %v", uidAfter, uidBefore)
	}

	// Restore re-enqueues indexing — searchable again within seconds.
	poll(t, "restore reindex", 60*time.Second, func() bool {
		s := sysJSON(t, "search", token, "-m", c.mailbox)
		return findByID(s, "results", msgID) != nil
	})
}

func TestLiveE2E_MailboxSoftDeleteRestore(t *testing.T) {
	c := e2eSetup(t)
	addr := "scratch-" + uniq() + "@" + c.domain
	scratch := createMailbox(t, addr, mailboxOpts{})
	createRoute(t, addr, scratch)

	subject := "scratch " + runID + " " + uniq()
	body := mime(envFrom, addr, subject, "")
	res := deliverOK(t, addr, body, deliveryID("scratch"))
	msgID := str(res, "messageId")

	del := sysJSON(t, "mailboxes", "delete", scratch, "--yes")
	if d, _ := del["deleted"].(bool); !d {
		t.Fatalf("delete did not report deleted: %v", del)
	}
	if _, ok := num(del, "restorableUntil"); !ok {
		t.Fatalf("delete missing restorableUntil: %v", del)
	}

	// Gone from the directory…
	if _, _, code := runAs(t, systemKey, "", "mailboxes", "get", scratch); code == 0 {
		t.Fatal("deleted mailbox still readable")
	}
	// …so redelivery has no route and bounces permanently (unknown_address).
	_, errb, code := deliver(t, addr, body, deliveryID("scratch-redeliver"))
	if code == 0 {
		t.Fatal("redelivery to deleted mailbox succeeded")
	}
	if !strings.Contains(errb, "unknown_address") {
		t.Fatalf("redelivery error = %q, want unknown_address", strings.TrimSpace(errb))
	}
	// …and it appears in the deleted-tombstone listing.
	tomb := sysJSON(t, "mailboxes", "list", "--deleted")
	if findByID(tomb, "mailboxes", scratch) == nil {
		t.Fatal("deleted mailbox not in tombstone listing")
	}

	// Undo within the window brings the mailbox — and its mail — back.
	restore := sysJSON(t, "mailboxes", "restore", scratch)
	if r, _ := restore["restored"].(bool); !r {
		t.Fatalf("restore did not report restored: %v", restore)
	}
	if _, _, code := runAs(t, systemKey, "", "mailboxes", "get", scratch); code != 0 {
		t.Fatal("restored mailbox not readable")
	}
	list := sysJSON(t, "messages", "list", "-m", scratch)
	if findByID(list, "messages", msgID) == nil {
		t.Fatal("restored mailbox lost its mail")
	}
	t.Log("[NOTE] the 7-day deferred wipe (MAILBOX_WIPE_DELAY_MS) can't be tested synchronously — verify by backdating the DO meta stamps (PRE-LAUNCH §7/§8)")
}

func TestLiveE2E_DomainTraffic(t *testing.T) {
	c := e2eSetup(t)
	out, errb, code := runAs(t, systemKey, "", "--json", "domains", "traffic", c.domain, "--range", "1h")
	if code != 0 {
		if strings.Contains(errb, "analytics_unavailable") || strings.Contains(errb, "503") {
			t.Skipf("domain traffic unavailable (ANALYTICS_API_TOKEN not set): %s", strings.TrimSpace(errb))
		}
		t.Fatalf("domains traffic exit %d: %s", code, errb)
	}
	m := decodeMap(t, []string{"domains", "traffic"}, out)
	if got := str(m, "domain"); got != c.domain {
		t.Fatalf("domain = %q, want %q", got, c.domain)
	}
	if est, _ := m["estimated"].(bool); !est {
		t.Fatalf("estimated = false, want true: %v", m)
	}
	if rd, _ := num(m, "retentionDays"); rd != 90 {
		t.Fatalf("retentionDays = %v, want 90", m["retentionDays"])
	}
	if _, ok := m["totals"].(map[string]any); !ok {
		t.Fatalf("totals missing: %v", m)
	}
	t.Log("[NOTE] the authoritative Iceberg traffic log flushes after minutes — confirm with `wrangler r2 sql query`; AE aggregates may lag a few seconds")
}
