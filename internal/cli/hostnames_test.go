package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func mxSlot(status *coreapi.HostnameCheckStatus) *coreapi.DomainHostnameList {
	host, verified := "mx.acme.dev", int64(1)
	return &coreapi.DomainHostnameList{
		Domain:   "acme.dev",
		CanClaim: true,
		Hostnames: []coreapi.DomainHostname{{
			Service: "mx", Domain: "acme.dev", Hostname: &host,
			NsTargets:  []string{"ns1.example", "ns2.example"},
			VerifiedAt: &verified, Status: status,
		}},
	}
}

// A delegation core could only read from its OWN responder proves the delegation
// REACHES us — not that ours are the only nameservers listed. The STATE column
// cannot hold that qualification, so the listing has to say it: an operator who
// reads "live" over a responder-sourced verdict believes a check that never ran.
func TestHostnamesSayWhenTheDelegationCameFromOurResponder(t *testing.T) {
	var buf bytes.Buffer
	p := newPrinter(false, true)
	printHostnames(&buf, p, mxSlot(&coreapi.HostnameCheckStatus{
		NS: "ok", DelegationSource: "responder", Resolution: "ok",
	}))
	if !strings.Contains(buf.String(), "not from your parent zone") {
		t.Fatalf("responder-sourced delegation went unqualified:\n%s", buf.String())
	}
}

// The parent zone IS the stronger reading — set equality against what the
// customer published — so qualifying it there would be noise that teaches an
// operator to skip the line in the case that matters.
func TestHostnamesStaySilentWhenTheParentZoneWasRead(t *testing.T) {
	var buf bytes.Buffer
	p := newPrinter(false, true)
	printHostnames(&buf, p, mxSlot(&coreapi.HostnameCheckStatus{
		NS: "ok", DelegationSource: "parent", Resolution: "ok",
	}))
	if strings.Contains(buf.String(), "parent zone") {
		t.Fatalf("parent-sourced delegation was qualified anyway:\n%s", buf.String())
	}
}
