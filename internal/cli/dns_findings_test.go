package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// What the DNS table cannot say: a record that does not answer, with something
// else published at its name. Two DMARC records are the case it was written
// for (RFC 7489 §6.6.3: receivers then apply neither), where the table alone
// would have the customer publish a third.

func boolp(b bool) *bool { return &b }

func dmarcCheck(ok *bool, found ...string) coreapi.DNSRecordCheck {
	return coreapi.DNSRecordCheck{
		DNSRecord: coreapi.DNSRecord{Kind: "dmarc", Type: "TXT", Name: "_dmarc.acme.dev", Value: "v=DMARC1; p=none; rua=mailto:acme.dev@dmarc.open.email"},
		OK:        ok,
		Found:     found,
	}
}

func TestDNSFindingsSaysTwoDMARCRecordsAreTwo(t *testing.T) {
	lines := dnsFindings([]coreapi.DNSRecordCheck{dmarcCheck(boolp(false), "v=DMARC1; p=none", "v=DMARC1; p=reject")})
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", lines)
	}
	for _, want := range []string{
		"_dmarc.acme.dev publishes 2 DMARC records",
		"v=DMARC1; p=none | v=DMARC1; p=reject",
		"Receivers apply DMARC only when there is exactly one",
		"delete all but one, or merge them into one: v=DMARC1; p=none; rua=mailto:acme.dev@dmarc.open.email",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line %q lacks %q", lines[0], want)
		}
	}
}

func TestDNSFindingsSaysWhatAWrongRecordHolds(t *testing.T) {
	mx := coreapi.DNSRecordCheck{
		DNSRecord: coreapi.DNSRecord{Kind: "mx", Type: "MX", Name: "acme.dev", Value: "mx.open.email"},
		OK:        boolp(false),
		Found:     []string{"mx.other-provider.example"},
	}
	// A lone DMARC record that sets no policy is "wrong", not "two": the plain line.
	lone := dmarcCheck(boolp(false), "v=DMARC1")
	lines := dnsFindings([]coreapi.DNSRecordCheck{mx, lone})
	want := []string{
		"acme.dev holds mx.other-provider.example, not mx.open.email",
		"_dmarc.acme.dev holds v=DMARC1, not v=DMARC1; p=none; rua=mailto:acme.dev@dmarc.open.email",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestDNSFindingsSaysNothingForLiveMissingOrUnknown(t *testing.T) {
	lines := dnsFindings([]coreapi.DNSRecordCheck{
		dmarcCheck(boolp(true), "v=DMARC1; p=none"), // live
		dmarcCheck(boolp(false)),                    // missing: the table's "no" says it
		dmarcCheck(nil),                             // unknown: DNS could not be asked
		dmarcCheck(nil, "v=DMARC1; p=none", "v=DMARC1; p=reject"),
	})
	if len(lines) != 0 {
		t.Fatalf("want no lines, got %q", lines)
	}
	var buf bytes.Buffer
	printDNSFindings(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("nothing to say prints nothing, got %q", buf.String())
	}
}

func TestDNSFindingsSanitizesTheCustomersDNSText(t *testing.T) {
	evil := "v=DMARC1; p=none\x1b[2J\x1b]0;owned\x07\nfake line"
	lines := dnsFindings([]coreapi.DNSRecordCheck{dmarcCheck(boolp(false), evil, "v=DMARC1; p=reject")})
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", lines)
	}
	if strings.ContainsAny(lines[0], "\x1b\x07\n") {
		t.Fatalf("an escape or a newline reached the terminal: %q", lines[0])
	}
	if !strings.Contains(lines[0], "v=DMARC1; p=nonefake line") {
		t.Fatalf("the text itself should survive, stripped: %q", lines[0])
	}
}

// The two commands that print the records print the findings under them.
func TestDomainsDNSAndCreatePrintTheFindings(t *testing.T) {
	const twoDMARC = `{"type":"TXT","kind":"dmarc","name":"_dmarc.acme.dev","value":"v=DMARC1; p=none","purpose":"p","required":false,"ok":false,"found":["v=DMARC1; p=none","v=DMARC1; p=reject"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/dns/check") {
			_, _ = w.Write([]byte(`{"domain":"acme.dev","checkedAt":1,"cached":false,"status":{"dmarc":false},"records":[` + twoDMARC + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"domain":"acme.dev","enabled":true,"sendOnly":false,"sendVerified":false,"sendHold":null,"sending":false,"receiving":true,"records":[` + twoDMARC + `]}`))
	}))
	t.Cleanup(srv.Close)

	for _, args := range [][]string{{"dns", "acme.dev"}, {"create", "acme.dev"}} {
		var out bytes.Buffer
		a := &app{out: &Printer{out: &out, err: &out}, token: "oek_test", tokenSource: "flag", apiURL: srv.URL}
		cmd := newDomainsCmd(a)
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("domains %s: %v", args[0], err)
		}
		if !strings.Contains(out.String(), "! _dmarc.acme.dev publishes 2 DMARC records") {
			t.Fatalf("domains %s printed no finding:\n%s", args[0], out.String())
		}
	}
}
