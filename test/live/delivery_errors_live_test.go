//go:build live

// Live black-box e2e for delivery ERROR outcomes, driven through the CLI. Mirrors
// core's test/live/delivery-errors.live.test.ts — the HTTP→SMTP status mappings
// the MTA relies on. Core asserts exact HTTP status + error string; the CLI turns
// a 4xx/5xx into a non-zero exit and prints the machine error code, so each case
// asserts a failed exit whose stderr carries that code.
package live

import (
	"strings"
	"testing"
)

type deliverEnv struct {
	quotaAddr       string
	rxDisabledAddr  string
	disabledAddr    string
	unknownAddr     string
	sendDisabledDom string
}

var derr *deliverEnv

func deliverErrSetup(t *testing.T) *deliverEnv {
	t.Helper()
	requireLive(t)
	if derr != nil {
		return derr
	}
	d := &deliverEnv{}

	// 1) Over-quota: a normal domain with a tiny (2 KB) mailbox.
	quotaDom := createDomain(t, domainOpts{})
	d.quotaAddr = "tiny@" + quotaDom
	qmbx := createMailbox(t, d.quotaAddr, mailboxOpts{quotaBytes: 2000})
	createRoute(t, d.quotaAddr, qmbx)

	// 2) Send-only domain (sendOnly:true — the 0062 spelling of the old
	//    canReceive:false), address routed.
	rxDom := createDomain(t, domainOpts{sendOnly: boolp(true)})
	d.rxDisabledAddr = "rx@" + rxDom
	rxmbx := createMailbox(t, d.rxDisabledAddr, mailboxOpts{})
	createRoute(t, d.rxDisabledAddr, rxmbx)

	// 3) Disabled domain (enabled:false but receiving), address routed.
	disDom := createDomain(t, domainOpts{enabled: boolp(false), sendOnly: boolp(false)})
	d.disabledAddr = "d@" + disDom
	dmbx := createMailbox(t, d.disabledAddr, mailboxOpts{})
	createRoute(t, d.disabledAddr, dmbx)

	// 4) Unknown address: a normal domain with NO route to the target.
	unkDom := createDomain(t, domainOpts{})
	d.unknownAddr = "ghost-" + runID + "@" + unkDom

	// 5) Domain that cannot send (sendVerified:false), mailbox routed so the
	//    sender resolves locally and reaches the send gate. Core folds every
	//    reason a domain may not send into the one wire code sending_disabled.
	d.sendDisabledDom = createDomain(t, domainOpts{sendVerified: boolp(false), sendOnly: boolp(false)})
	sAddr := "s@" + d.sendDisabledDom
	smbx := createMailbox(t, sAddr, mailboxOpts{})
	createRoute(t, sAddr, smbx)

	derr = d
	return d
}

// deliverFail delivers and requires a non-zero exit whose stderr carries wantCode.
func deliverFail(t *testing.T, to, body, id, wantCode string) {
	t.Helper()
	_, errb, code := deliver(t, to, body, id)
	if code == 0 {
		t.Fatalf("delivery to %s unexpectedly succeeded (wanted %s)", to, wantCode)
	}
	if !strings.Contains(errb, wantCode) {
		t.Fatalf("delivery to %s error = %q, want %q", to, strings.TrimSpace(errb), wantCode)
	}
}

func TestLiveDeliveryErr_OverQuota(t *testing.T) {
	d := deliverErrSetup(t)
	// ~4 KB body vs the mailbox's 2000-byte quota — refused at beginDelivery.
	big := mime(envFrom, d.quotaAddr, "over-quota "+uniq(), strings.Repeat("x", 4000))
	deliverFail(t, d.quotaAddr, big, deliveryID("quota-big"), "over_quota")

	// Control: a small message stays under quota and delivers (over_quota refuses
	// at begin, before any counter bump).
	small := mime(envFrom, d.quotaAddr, "under-quota "+uniq(), "ok")
	res := deliverOK(t, d.quotaAddr, small, deliveryID("quota-small"))
	if got := str(res, "status"); got != "delivered" {
		t.Fatalf("under-quota status = %q, want delivered", got)
	}
}

func TestLiveDeliveryErr_ReceiveDisabled(t *testing.T) {
	d := deliverErrSetup(t)
	body := mime(envFrom, d.rxDisabledAddr, "rx-disabled "+uniq(), "")
	deliverFail(t, d.rxDisabledAddr, body, deliveryID("rx"), "receiving_disabled")
}

func TestLiveDeliveryErr_DomainDisabled(t *testing.T) {
	d := deliverErrSetup(t)
	body := mime(envFrom, d.disabledAddr, "domain-disabled "+uniq(), "")
	deliverFail(t, d.disabledAddr, body, deliveryID("disabled"), "receiving_disabled")
}

func TestLiveDeliveryErr_UnknownAddress(t *testing.T) {
	d := deliverErrSetup(t)
	body := mime(envFrom, d.unknownAddr, "unknown "+uniq(), "")
	deliverFail(t, d.unknownAddr, body, deliveryID("unknown"), "unknown_address")
}

func TestLiveDeliveryErr_SendDisabled(t *testing.T) {
	d := deliverErrSetup(t)
	// System key: the ownership fence is skipped, so the request reaches the
	// domain send-state gate (sendVerified:false) → sending_disabled. The gate precedes
	// recipient resolution, so the external .invalid recipient is never touched.
	from := "s@" + d.sendDisabledDom
	external := "ext@external-" + runID + ".invalid"
	body := mime(from, external, "send-disabled "+uniq(), "")
	_, errb, code := runAs(t, systemKey, body, "send",
		"--from", from, "--to", external,
		"--envelope-from", from, "--envelope-to", external,
		"--delivery-id", deliveryID("send-disabled"))
	if code == 0 {
		t.Fatal("send from a send-disabled domain unexpectedly succeeded")
	}
	if !strings.Contains(errb, "sending_disabled") {
		t.Fatalf("send error = %q, want sending_disabled", strings.TrimSpace(errb))
	}
}
