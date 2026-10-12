package tui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestCalendarOnlyRulesPane(t *testing.T) {
	c, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/identities/pim" {
			fmt.Fprint(w, `{"id":"pim","facets":{}}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not_found"}`)
	})
	defer done()
	desc := rulesDesc(coreapi.Mailbox{ID: "pim"})
	_, _, err := desc.fetch(context.Background(), c, "")
	if err == nil || !strings.Contains(err.Error(), "no mail store") {
		t.Fatalf("want no-mail explanation in the pane, got %v", err)
	}
}
