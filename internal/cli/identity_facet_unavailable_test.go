package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A store core could not read in time is still bound: `identities get` names it
// and says its usage is unavailable, never "0 (0 B)" and never "not bound".
func TestIdentityGetFacetUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"01I","primaryAddress":"a@b.test","accountId":"acc1","createdAt":5,"facets":{"mail":{"storeId":"01I","unavailable":true},"pim":{"storeId":"01I","unavailable":true}}}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	a := &app{apiURL: srv.URL, token: "oek_test", out: &Printer{out: &out, err: &out}}
	cmd := newIdentityGetCmd(a)
	cmd.SetArgs([]string{"01I"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "usage unavailable") != 2 {
		t.Fatalf("expected both stores reported unavailable:\n%s", got)
	}
	for _, wrong := range []string{"0 (0 B)", "not bound", "not yet provisioned"} {
		if strings.Contains(got, wrong) {
			t.Fatalf("unavailable usage rendered as %q:\n%s", wrong, got)
		}
	}
}
