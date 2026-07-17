//go:build live

// Live black-box e2e for the recipient-resolution ladder, driven through the
// CLI. Mirrors core's test/live/routing.live.test.ts: it proves WHICH mailbox
// received by delivering a unique-subject message and finding that subject via
// `messages list`. Exact/pattern/tag/alias tiers deliver synchronously (the
// message is listable immediately after the delivered status); the bare-'*'
// catch-all is paced (queued → polled), like group fan-out.
package live

import (
	"testing"
	"time"
)

type routeEnv struct {
	patternDomain    string
	patternMailbox   string // pattern destination (no route/address of its own)
	vipMailbox       string // exact route vip@patternDomain → here
	tagDomain        string
	tagMailbox       string // exact route alice@tagDomain → here
	catchallDomain   string
	catchallMailbox  string // '*' destination
	canonicalDomain  string
	canonicalMailbox string // exact route bob@canonicalDomain → here
	aliasDomain      string // alias_of canonicalDomain
}

var routes *routeEnv

func routingSetup(t *testing.T) *routeEnv {
	t.Helper()
	requireLive(t)
	if routes != nil {
		return routes
	}
	r := &routeEnv{}

	// tier 2 + (tier 1 > tier 2): one domain, a pattern-destination mailbox, and
	// an exact-route mailbox for vip@.
	r.patternDomain = createDomain(t, domainOpts{})
	r.patternMailbox = createMailbox(t, "", mailboxOpts{})
	r.vipMailbox = createMailbox(t, "vip@"+r.patternDomain, mailboxOpts{})
	createRoute(t, "vip@"+r.patternDomain, r.vipMailbox)

	// tier 3: exact base route; delivery targets the +tag form.
	r.tagDomain = createDomain(t, domainOpts{})
	r.tagMailbox = createMailbox(t, "alice@"+r.tagDomain, mailboxOpts{})
	createRoute(t, "alice@"+r.tagDomain, r.tagMailbox)

	// tier 4: a domain whose ONLY route is the '*' catch-all.
	r.catchallDomain = createDomain(t, domainOpts{})
	r.catchallMailbox = createMailbox(t, "", mailboxOpts{})

	// tier 0: canonical BEFORE the alias (the alias write needs the canonical to
	// exist); the route lives on the canonical.
	r.canonicalDomain = createDomain(t, domainOpts{})
	r.canonicalMailbox = createMailbox(t, "bob@"+r.canonicalDomain, mailboxOpts{})
	createRoute(t, "bob@"+r.canonicalDomain, r.canonicalMailbox)
	r.aliasDomain = createDomain(t, domainOpts{aliasOf: r.canonicalDomain})

	routes = r
	return r
}

func TestLiveRouting_PatternTier(t *testing.T) {
	r := routingSetup(t)
	createPattern(t, r.patternDomain, "sales*", r.patternMailbox)

	subject := "pattern " + runID + " " + uniq()
	res := deliverOK(t, "sales-eu@"+r.patternDomain, mime(envFrom, "sales-eu@"+r.patternDomain, subject, ""), deliveryID("pattern"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("status = %q, want delivered", got)
	}
	if !contains(subjectsIn(t, r.patternMailbox), subject) {
		t.Fatalf("pattern message did not land in the pattern mailbox")
	}
}

func TestLiveRouting_ExactBeatsPattern(t *testing.T) {
	r := routingSetup(t)
	// vip* → patternMailbox, but the exact route vip@ → vipMailbox must outrank it.
	createPattern(t, r.patternDomain, "vip*", r.patternMailbox)

	exactSubject := "exact-wins " + runID + " " + uniq()
	res := deliverOK(t, "vip@"+r.patternDomain, mime(envFrom, "vip@"+r.patternDomain, exactSubject, ""), deliveryID("exact"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("exact status = %q, want delivered", got)
	}
	if !contains(subjectsIn(t, r.vipMailbox), exactSubject) {
		t.Fatal("exact-route mail did not land in the vip mailbox")
	}
	if contains(subjectsIn(t, r.patternMailbox), exactSubject) {
		t.Fatal("exact-route mail wrongly landed in the pattern mailbox")
	}

	// vip2@ has no exact route → the vip* pattern catches it.
	patternSubject := "pattern-fallback " + runID + " " + uniq()
	res = deliverOK(t, "vip2@"+r.patternDomain, mime(envFrom, "vip2@"+r.patternDomain, patternSubject, ""), deliveryID("vip2"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("fallback status = %q, want delivered", got)
	}
	if !contains(subjectsIn(t, r.patternMailbox), patternSubject) {
		t.Fatal("vip2 mail did not fall through to the pattern mailbox")
	}
	if contains(subjectsIn(t, r.vipMailbox), patternSubject) {
		t.Fatal("vip2 mail wrongly landed in the vip mailbox")
	}
}

func TestLiveRouting_TagStripped(t *testing.T) {
	r := routingSetup(t)
	subject := "tag-stripped " + runID + " " + uniq()
	to := "alice+promo@" + r.tagDomain
	res := deliverOK(t, to, mime(envFrom, to, subject, ""), deliveryID("tag"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("status = %q, want delivered", got)
	}
	if !contains(subjectsIn(t, r.tagMailbox), subject) {
		t.Fatal("plus-addressed mail did not fall back to the base route")
	}
}

func TestLiveRouting_CatchAll(t *testing.T) {
	r := routingSetup(t)
	createPattern(t, r.catchallDomain, "*", r.catchallMailbox)

	subject := "catch-all " + runID + " " + uniq()
	to := "whoever-" + runID + "@" + r.catchallDomain
	// A bare '*' is a funnel into one DO, so it is PACED: queued (202) then drained
	// asynchronously. Poll for it to land.
	res := deliverOK(t, to, mime(envFrom, to, subject, ""), deliveryID("catchall"))
	if got := str(res, "status"); got != "queued" {
		t.Fatalf("catch-all status = %q, want queued", got)
	}
	poll(t, "catch-all message in "+r.catchallMailbox, 60*time.Second, func() bool {
		return contains(subjectsIn(t, r.catchallMailbox), subject)
	})
}

func TestLiveRouting_AliasRewrite(t *testing.T) {
	r := routingSetup(t)
	// bob@aliasDomain — the alias holds no routes; tier 0 rewrites to
	// bob@canonicalDomain, whose exact route lands it in canonicalMailbox.
	subject := "alias-rewrite " + runID + " " + uniq()
	to := "bob@" + r.aliasDomain
	res := deliverOK(t, to, mime(envFrom, to, subject, ""), deliveryID("alias"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("status = %q, want delivered", got)
	}
	if !contains(subjectsIn(t, r.canonicalMailbox), subject) {
		t.Fatal("alias-domain mail did not resolve on the canonical domain")
	}
}
