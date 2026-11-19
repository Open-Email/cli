package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

/*
 * Since core #884 a spam complaint is filed against the account whose mail
 * drew it, and `admin suppressions` reads and lifts the GLOBAL rows only. Core
 * says a 404 from GET /suppressions/:address is "no global record, not a
 * verdict on account complaints or address lists", so neither `get` nor `lift`
 * may tell an operator an address is "clear to send": that was the promise of
 * a list that held every complaint, and it is now wrong exactly when it
 * matters, for an address that complained about one tenant's mail.
 */

func TestAdminSuppressionsGetSaysGlobalNotClear(t *testing.T) {
	var sawQuery string
	core := newMailCore(t, systemWhoami, func(r *http.Request) (int, string) {
		sawQuery = r.URL.RawQuery
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/suppressions/them@example.net" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		return 404, `{"error":"not_found"}`
	})

	_, errOut, code := runCLI(t, core.srv.URL, "admin", "suppressions", "get", "them@example.net")
	if code != 0 {
		t.Fatalf("a 404 is the answer, not a failure: exit %d, stderr %s", code, errOut)
	}
	if strings.Contains(errOut, "clear to send") {
		t.Errorf("a global 404 must not read as clear to send: %s", errOut)
	}
	if !strings.Contains(errOut, "not on the global suppression list") || !strings.Contains(errOut, "--query accountId=") {
		t.Errorf("the answer must name its scope and the way to the account's: %s", errOut)
	}
	if sawQuery != "" {
		t.Errorf("the global read sends no scope, got query %q", sawQuery)
	}

	// --json keeps its shape: the address and core's answer for the scope asked.
	out, errOut, code := runCLI(t, core.srv.URL, "admin", "suppressions", "get", "them@example.net", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["suppressed"] != false || got["address"] != "them@example.net" {
		t.Errorf("--json: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestAdminSuppressionsLiftSaysGlobalNotClear(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(r *http.Request) (int, string) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/suppressions/them@example.net" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		return 200, `{"deleted":true}`
	})
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "suppressions", "lift", "them@example.net", "--yes")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut)
	}
	if strings.Contains(errOut, "clear to send") || !strings.Contains(errOut, "Global suppression on them@example.net lifted") {
		t.Errorf("a global lift must say what it lifted, not that the address is clear: %s", errOut)
	}
}
