//go:build live

// Live black-box e2e for auth & tenant scoping, driven through the CLI. Mirrors
// core's test/live/authz.live.test.ts:
//   - a mailbox principal (app password) is fenced from every directory group
//     (insufficient_scope) yet still reaches its OWN mailbox;
//   - a cross-tenant read fails as not_found (404), never a scope error — with a
//     same-key control and a system-key control proving the object exists;
//   - an account key is confined to its own domains (foreign inbound collapses to
//     unknown_address; a route on a foreign domain is unknown_domain).
package live

import (
	"strings"
	"testing"
)

type authzEnv struct {
	domainA    string
	mailboxA   string
	appPass    string // oemp_ app password bound to mailboxA (a mailbox principal)
	accountKey string // oek_ account key for tenant A (an account principal)

	accountB string
	domainB  string
	addressB string
	mailboxB string
}

var authz *authzEnv

func authzSetup(t *testing.T) *authzEnv {
	t.Helper()
	requireLive(t)
	if authz != nil {
		return authz
	}
	a := &authzEnv{}

	// Tenant A: mailbox → route → credential → account key.
	a.domainA = createDomain(t, domainOpts{})
	addressA := "alice@" + a.domainA
	a.mailboxA = createMailbox(t, addressA, mailboxOpts{})
	createRoute(t, addressA, a.mailboxA)
	_, a.appPass = createAppPassword(t, a.mailboxA)
	a.accountKey = createAccountKey(t, ensureAccount(t))

	// Tenant B: a distinct account, domain, mailbox, and route.
	a.accountB = createAccount(t, "tenant-b")
	a.domainB = createDomain(t, domainOpts{accountID: a.accountB})
	a.addressB = "bob@" + a.domainB
	a.mailboxB = createMailbox(t, a.addressB, mailboxOpts{accountID: a.accountB})
	createRoute(t, a.addressB, a.mailboxB)

	authz = a
	return a
}

func TestLiveAuthz_MailboxPrincipalFence(t *testing.T) {
	a := authzSetup(t)

	// Every directory group rejects a mailbox principal at the fence.
	expectFail(t, a.appPass, "insufficient_scope", "domains", "list")
	expectFail(t, a.appPass, "insufficient_scope", "routes", "list")
	expectFail(t, a.appPass, "insufficient_scope", "mailboxes", "list")
	// A directory write is fenced identically — before the endpoint's own check.
	expectFail(t, a.appPass, "insufficient_scope", "accounts", "create", "x")

	// …but the mailbox principal DOES reach its own mailbox.
	own := jsonAs(t, a.appPass, "", "mailboxes", "get", a.mailboxA)
	if got := str(own, "id"); got != a.mailboxA {
		t.Fatalf("own mailbox id = %q, want %q", got, a.mailboxA)
	}
}

func TestLiveAuthz_CrossTenantNoLeak(t *testing.T) {
	a := authzSetup(t)

	// Tenant A's account key reaching for tenant B: 404, never a scope error.
	expectFail(t, a.accountKey, "not_found", "mailboxes", "get", a.mailboxB)
	expectFail(t, a.accountKey, "not_found", "domains", "get", a.domainB)
	expectFail(t, a.accountKey, "not_found", "routes", "get", a.addressB)
	expectFail(t, a.accountKey, "not_found", "accounts", "get", a.accountB)

	// Control 1: the same account key DOES see its own mailbox — the 404s above
	// are scoping, not a broken credential.
	own := jsonAs(t, a.accountKey, "", "mailboxes", "get", a.mailboxA)
	if got := str(own, "id"); got != a.mailboxA {
		t.Fatalf("own mailbox id = %q, want %q", got, a.mailboxA)
	}
	// Control 2: the SYSTEM key DOES see tenant B's mailbox — it exists, so the
	// account key's 404 is scoping, not absence.
	sys := sysJSON(t, "mailboxes", "get", a.mailboxB)
	if got := str(sys, "id"); got != a.mailboxB {
		t.Fatalf("system view of tenant B id = %q, want %q", got, a.mailboxB)
	}
}

func TestLiveAuthz_AccountKeyDomainConfinement(t *testing.T) {
	a := authzSetup(t)

	// Inbound to a foreign tenant's domain collapses to unknown_address (404) —
	// no cross-tenant address enumeration.
	subject := "authz-inbound " + runID + " " + uniq()
	body := mime(envFrom, a.addressB, subject, "")
	_, errb, code := runAs(t, a.accountKey, body, "deliver", "inbound",
		"--to", a.addressB, "--from", envFrom, "--delivery-id", deliveryID("authz-inbound"))
	if code == 0 {
		t.Fatal("foreign-domain inbound unexpectedly succeeded")
	}
	if !strings.Contains(errb, "unknown_address") {
		t.Fatalf("foreign inbound error = %q, want unknown_address", strings.TrimSpace(errb))
	}

	// Creating a route on a foreign domain is refused with unknown_domain (400),
	// even though the target mailbox belongs to the caller.
	expectFail(t, a.accountKey, "unknown_domain",
		"routes", "create", "x@"+a.domainB, "--type", "mailbox", "--mailbox", a.mailboxA)
}
