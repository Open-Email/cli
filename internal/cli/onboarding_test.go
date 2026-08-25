package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func ptrBool(v bool) *bool    { return &v }
func ptrInt32(v int32) *int32 { return &v }

func renderRecords(t *testing.T, records []coreapi.DNSRecordCheck, withLiveness bool) string {
	t.Helper()
	var buf bytes.Buffer
	p := newPrinter(false, true)
	printDNSRecords(&buf, p, records, withLiveness)
	return buf.String()
}

// A record row must be usable as-is: the customer pastes name, type and value
// into their DNS provider. MX/SRV keep their numeric fields outside `value` in
// the wire shape, so the renderer has to fold them back in.
func TestPrintDNSRecordsIsCopyPasteable(t *testing.T) {
	out := renderRecords(t, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{
			Kind: "mx", Type: "MX", Name: "a.test", Value: "mx.open.email",
			Priority: ptrInt32(10), Required: true,
		}},
		{DNSRecord: coreapi.DNSRecord{
			Kind: "jmap", Type: "SRV", Name: "_jmap._tcp.a.test", Value: "jmap.open.email",
			Priority: ptrInt32(0), Port: ptrInt32(443), Required: false,
		}},
	}, false)

	if !strings.Contains(out, "10 mx.open.email") {
		t.Fatalf("MX preference not rendered with the host:\n%s", out)
	}
	if !strings.Contains(out, "port 443") {
		t.Fatalf("SRV port not rendered:\n%s", out)
	}
	if !strings.Contains(out, "_jmap._tcp.a.test") {
		t.Fatalf("record name missing:\n%s", out)
	}
	if strings.Contains(out, "LIVE") {
		t.Fatalf("liveness column shown when not asked for:\n%s", out)
	}
}

// UNKNOWN liveness must never render as a plain "no". A resolver outage is our
// problem, and showing it as a failed record sends customers to re-check DNS
// that is already correct.
func TestPrintDNSRecordsDistinguishesUnknownFromAbsent(t *testing.T) {
	out := renderRecords(t, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{Kind: "spf", Type: "CNAME", Name: "oe-bounce.a.test", Value: "oe-bounce.open.email", Required: true}, OK: ptrBool(true)},
		{DNSRecord: coreapi.DNSRecord{Kind: "dkim", Type: "CNAME", Name: "oe1._domainkey.a.test", Value: "oe1.root", Required: true}, OK: ptrBool(false)},
		{DNSRecord: coreapi.DNSRecord{Kind: "dmarc", Type: "TXT", Name: "_dmarc.a.test", Value: "v=DMARC1", Required: false}}, // OK nil
	}, true)

	if !strings.Contains(out, "LIVE") {
		t.Fatalf("liveness column missing:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 {
		t.Fatalf("want header + 3 rows:\n%s", out)
	}
	if got := lastField(lines[3]); got != "?" {
		t.Fatalf("unknown liveness rendered as %q, want %q:\n%s", got, "?", out)
	}
	if got := lastField(lines[1]); got == "?" {
		t.Fatalf("known liveness rendered as unknown:\n%s", out)
	}
	if lastField(lines[1]) == lastField(lines[2]) {
		t.Fatalf("live and absent render identically:\n%s", out)
	}
}

// Core scores a record against value ∪ accept, so a domain with a vanity DAV
// hostname is LIVE while still publishing the platform target. Printing only
// the preferred target next to that verdict reads as a contradiction and sends
// the customer to "fix" a record that is already correct.
func TestPrintDNSRecordsShowsAcceptedAlternates(t *testing.T) {
	out := renderRecords(t, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{
			Kind: "dav", Type: "SRV", Name: "_caldavs._tcp.a.test", Value: "dav.a.test",
			Accept: []string{"dav.open.email"}, Priority: ptrInt32(0), Weight: ptrInt32(1),
			Port: ptrInt32(443), Required: false,
		}, OK: ptrBool(true)},
	}, true)

	if !strings.Contains(out, "dav.a.test") {
		t.Fatalf("preferred target missing:\n%s", out)
	}
	if !strings.Contains(out, "dav.open.email") {
		t.Fatalf("accepted alternate not rendered:\n%s", out)
	}
}

func TestBoolYNUnknown(t *testing.T) {
	if got := boolYNUnknown(nil); got != "?" {
		t.Fatalf("nil rendered %q", got)
	}
	if boolYNUnknown(ptrBool(true)) == boolYNUnknown(ptrBool(false)) {
		t.Fatal("true and false render identically")
	}
	if boolYNUnknown(ptrBool(true)) == "?" || boolYNUnknown(ptrBool(false)) == "?" {
		t.Fatal("a known value rendered as unknown")
	}
}

func lastField(line string) string {
	fields := strings.Fields(ansiSeq.ReplaceAllString(line, ""))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func renderNext(t *testing.T, d *coreapi.Domain, records []coreapi.DNSRecordCheck) string {
	t.Helper()
	var buf bytes.Buffer
	printOnboardingNext(&buf, newPrinter(false, true), d, records)
	return buf.String()
}

// Sending is earned by publishing DNS and re-running create, so a not-yet-sending
// domain must be told exactly which records are still missing.
func TestPrintOnboardingNextListsOnlyPendingRecords(t *testing.T) {
	out := renderNext(t, &coreapi.Domain{Domain: "a.test", Sending: false}, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{Kind: "mx", Type: "MX", Name: "a.test", Value: "mx.open.email"}, OK: ptrBool(true)},
		{DNSRecord: coreapi.DNSRecord{Kind: "spf", Type: "CNAME", Name: "oe-bounce.a.test", Value: "oe-bounce.x.test"}, OK: ptrBool(false)},
		{DNSRecord: coreapi.DNSRecord{Kind: "dkim", Type: "CNAME", Name: "oe1._domainkey.a.test", Value: "oe1.root"}}, // unknown
	})

	if strings.Contains(out, "mx.open.email") {
		t.Fatalf("a live record was listed as pending:\n%s", out)
	}
	if !strings.Contains(out, "oe-bounce.x.test") {
		t.Fatalf("missing record not listed:\n%s", out)
	}
	// Unknown is not "done": omitting it would tell a customer they are finished
	// during a resolver outage.
	if !strings.Contains(out, "oe1._domainkey.a.test") {
		t.Fatalf("unknown-liveness record not listed:\n%s", out)
	}
	if !strings.Contains(out, "re-run") {
		t.Fatalf("next step not stated:\n%s", out)
	}
}

func TestPrintOnboardingNextSilentWhenNothingLeft(t *testing.T) {
	sending := &coreapi.Domain{Domain: "a.test", Sending: true}
	if out := renderNext(t, sending, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{Kind: "spf"}, OK: ptrBool(false)},
	}); out != "" {
		t.Fatalf("already-sending domain nagged:\n%s", out)
	}

	allLive := &coreapi.Domain{Domain: "a.test", Sending: false}
	if out := renderNext(t, allLive, []coreapi.DNSRecordCheck{
		{DNSRecord: coreapi.DNSRecord{Kind: "spf"}, OK: ptrBool(true)},
	}); out != "" {
		t.Fatalf("nothing pending but rendered:\n%s", out)
	}
	if out := renderNext(t, allLive, nil); out != "" {
		t.Fatalf("no records but rendered:\n%s", out)
	}
}

// fmtDNS gained the JMAP verdict, which is present only for domains that opted
// in — it must not appear (as "jmap=?") for everyone else.
func TestFmtDNSShowsJMAPOnlyWhenPresent(t *testing.T) {
	base := &coreapi.DNSStatus{MX: ptrBool(true), SPF: ptrBool(true), DKIM: ptrBool(false), DMARC: ptrBool(false)}
	if got := fmtDNS(base); strings.Contains(got, "jmap") {
		t.Fatalf("jmap shown for a domain that did not opt in: %q", got)
	}
	withJMAP := *base
	withJMAP.JMAP = ptrBool(true)
	if got := fmtDNS(&withJMAP); !strings.Contains(got, "jmap") {
		t.Fatalf("jmap verdict dropped: %q", got)
	}
}

// The new refusals must each carry an actionable next step, since none of them
// is fixable by retrying the same command unchanged.
func TestOnboardingErrorHints(t *testing.T) {
	cases := []struct{ code, want string }{
		{"sending_not_writable", "domains create"},
		{"domain_claimed_elsewhere", "support"},
		{"verification_unavailable", "try again"},
	}
	for _, tc := range cases {
		got := errorHint(&coreapi.APIError{Code: tc.code})
		if got == "" {
			t.Fatalf("%s has no hint", tc.code)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s hint %q lacks %q", tc.code, got, tc.want)
		}
	}
}
