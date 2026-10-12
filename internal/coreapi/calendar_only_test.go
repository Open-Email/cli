package coreapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalendarOnlyMailError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/identities/pim" {
			fmt.Fprint(w, `{"id":"pim","facets":{}}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not_found"}`)
	}))
	defer srv.Close()
	_, err := testClient(t, srv.URL).GetRules(context.Background(), "pim")
	if err == nil || !strings.Contains(err.Error(), "no mail store") {
		t.Fatalf("missing no-mail explanation: %v", err)
	}
	if Status(err) != 404 || Code(err) != "not_found" {
		t.Fatalf("changed core's status/code: %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("a missing mail store must not trigger empty-resource fallbacks")
	}
}
