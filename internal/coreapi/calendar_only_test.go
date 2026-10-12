package coreapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestCalendarOnlyMailErrorKeepsUnconfirmedFailures(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		status       int
	}{
		{"missing", `{"error":"not_found"}`, 404},
		{"forbidden", `{"error":"forbidden"}`, 403},
		{"unavailable", `{"error":"unavailable"}`, 500},
		{"old response", `{"id":"pim"}`, 200},
		{"null facets", `{"facets":null}`, 200},
		{"array facets", `{"facets":[]}`, 200},
		{"null mail", `{"facets":{"mail":null}}`, 200},
		{"bound mail", `{"facets":{"mail":{"provisioned":false}}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/identities/pim" {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.detail)
					return
				}
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":"not_found","message":"original"}`)
			}))
			defer srv.Close()
			_, err := testClient(t, srv.URL).GetRules(context.Background(), "pim")
			ae, ok := AsAPIError(err)
			if !ok || ae.NoMailStore || !IsNotFound(err) || ae.Message != "original" {
				t.Fatalf("unconfirmed failure was reclassified: %v", err)
			}
			if string(ae.Body) != `{"error":"not_found","message":"original"}` || ae.Path != "/mailboxes/pim/rules" || ae.Method != "GET" {
				t.Fatalf("original response/request changed: %+v", ae)
			}
		})
	}
}

func TestCalendarOnlyMailErrorAllMailPanes(t *testing.T) {
	for _, resource := range []string{"rules", "vacation", "sieve/scripts", "pickups", "search", "messages/expunged", "messages"} {
		t.Run(resource, func(t *testing.T) {
			var reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer oek_test" {
					t.Error("diagnostic changed the bearer")
				}
				if r.URL.Path == "/api/v1/identities/pim" {
					reads.Add(1)
					fmt.Fprint(w, `{"facets":{}}`)
					return
				}
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":"not_found"}`)
			}))
			defer srv.Close()
			err := testClient(t, srv.URL).doJSON(context.Background(), request{method: "GET", path: "/mailboxes/pim/" + resource}, nil)
			ae, ok := AsAPIError(err)
			if !ok || !ae.NoMailStore || reads.Load() != 1 {
				t.Fatalf("no diagnosis for %s: %v (%d reads)", resource, err, reads.Load())
			}
		})
	}
}

func TestCalendarOnlyDiagnosisDoesNotRetryWritesOrProbeOtherRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path  string
		status, calls int
	}{
		{"GET", "/mailboxes/pim/rules", 200, 1},
		{"GET", "/mailboxes/pim/rules", 403, 1},
		{"GET", "/pim/pim/calendars", 404, 1},
		{"GET", "/identities/pim", 404, 1},
		{"PUT", "/mailboxes/pim/rules", 404, 2},
	} {
		t.Run(fmt.Sprintf("%s %s %d", tc.method, tc.path, tc.status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.method == "PUT" && r.Method == "GET" && r.URL.Path == "/api/v1/identities/pim" {
					fmt.Fprint(w, `{"facets":{}}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"not_found"}`)
			}))
			defer srv.Close()
			_ = testClient(t, srv.URL).doJSON(context.Background(), request{method: tc.method, path: tc.path}, nil)
			if calls.Load() != int32(tc.calls) {
				t.Fatalf("got %d calls, want %d", calls.Load(), tc.calls)
			}
		})
	}
}
