package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The read is one call and carries both halves — the destinations and where
// mail is actually going.
func TestGetForwarding(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Write([]byte(`{
			"destinations":[
				{"id":"01D","address":"me@elsewhere.test","state":"verified",
				 "codeExpiresAt":null,"verifiedAt":1700000000,"createdAt":1699999000}
			],
			"forwardAll":{"destinationId":"01D","address":"me@elsewhere.test","paused":false}
		}`))
	}))
	defer srv.Close()

	fwd, err := m3Client(t, srv.URL).GetForwarding(context.Background(), "01M")
	if err != nil {
		t.Fatalf("GetForwarding: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/mailboxes/01M/forwarding" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if len(fwd.Destinations) != 1 || fwd.Destinations[0].State != "verified" {
		t.Fatalf("destinations = %+v", fwd.Destinations)
	}
	// A null codeExpiresAt must survive as nil rather than decoding to 0 — the
	// pair (codeExpiresAt, verifiedAt) is how a client reads the ceremony's
	// state, and a zero would read as "expired long ago".
	if fwd.Destinations[0].CodeExpiresAt != nil {
		t.Errorf("codeExpiresAt = %v, want nil", *fwd.Destinations[0].CodeExpiresAt)
	}
	if fwd.Destinations[0].VerifiedAt == nil || *fwd.Destinations[0].VerifiedAt != 1700000000 {
		t.Errorf("verifiedAt = %v", fwd.Destinations[0].VerifiedAt)
	}
	if fwd.ForwardAll.DestinationID == nil || *fwd.ForwardAll.DestinationID != "01D" {
		t.Errorf("forwardAll = %+v", fwd.ForwardAll)
	}
}

// Off is a null destination, NOT an absent object — a client must be able to
// tell "not forwarding" from "unreadable".
func TestGetForwardingOffReadsAsNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"destinations":[],"forwardAll":{"destinationId":null,"address":null,"paused":false}}`))
	}))
	defer srv.Close()

	fwd, err := m3Client(t, srv.URL).GetForwarding(context.Background(), "01M")
	if err != nil {
		t.Fatalf("GetForwarding: %v", err)
	}
	if fwd.ForwardAll.DestinationID != nil || fwd.ForwardAll.Address != nil {
		t.Errorf("forwardAll = %+v, want nulls", fwd.ForwardAll)
	}
}

// Adding names an address; the code goes to the destination, never into this
// response.
func TestAddForwardingDestination(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"id":"01D","address":"m•@el•••.test","state":"pending","expiresAt":1700086400}`))
	}))
	defer srv.Close()

	dest, err := m3Client(t, srv.URL).AddForwardingDestination(context.Background(), "01M", "me@elsewhere.test")
	if err != nil {
		t.Fatalf("AddForwardingDestination: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/mailboxes/01M/forwarding/destinations" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotBody["address"] != "me@elsewhere.test" {
		t.Errorf("body = %v", gotBody)
	}
	if dest.State != "pending" || dest.ExpiresAt != 1700086400 {
		t.Errorf("dest = %+v", dest)
	}
}

func TestVerifyForwardingDestination(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"id":"01D","address":"m•@el•••.test","state":"verified"}`))
	}))
	defer srv.Close()

	dest, err := m3Client(t, srv.URL).VerifyForwardingDestination(context.Background(), "01M", "01D", "ABCD1234")
	if err != nil {
		t.Fatalf("VerifyForwardingDestination: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/mailboxes/01M/forwarding/destinations/01D/verify" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotBody["code"] != "ABCD1234" {
		t.Errorf("body = %v", gotBody)
	}
	if dest.State != "verified" {
		t.Errorf("state = %q", dest.State)
	}
}

func TestDeleteForwardingDestination(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Write([]byte(`{"deleted":true}`))
	}))
	defer srv.Close()

	if err := m3Client(t, srv.URL).DeleteForwardingDestination(context.Background(), "01M", "01D"); err != nil {
		t.Fatalf("DeleteForwardingDestination: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/mailboxes/01M/forwarding/destinations/01D" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
}

// The three forward-all verbs are three different HTTP methods on one path, and
// each answers the whole document back.
func TestForwardAllVerbs(t *testing.T) {
	const doc = `{"destinations":[],"forwardAll":{"destinationId":"01D","address":"me@elsewhere.test","paused":true}}`
	for _, tc := range []struct {
		name       string
		call       func(c *Client) (*Forwarding, error)
		wantMethod string
		wantBody   map[string]any
	}{
		{
			name:       "set",
			call:       func(c *Client) (*Forwarding, error) { return c.SetForwardAll(context.Background(), "01M", "01D") },
			wantMethod: http.MethodPut,
			wantBody:   map[string]any{"destinationId": "01D"},
		},
		{
			name:       "pause",
			call:       func(c *Client) (*Forwarding, error) { return c.PauseForwardAll(context.Background(), "01M", true) },
			wantMethod: http.MethodPatch,
			wantBody:   map[string]any{"paused": true},
		},
		{
			name:       "clear",
			call:       func(c *Client) (*Forwarding, error) { return c.ClearForwardAll(context.Background(), "01M") },
			wantMethod: http.MethodDelete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				body, _ := io.ReadAll(r.Body)
				if len(body) > 0 {
					_ = json.Unmarshal(body, &gotBody)
				}
				w.Write([]byte(doc))
			}))
			defer srv.Close()

			fwd, err := tc.call(m3Client(t, srv.URL))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotMethod != tc.wantMethod || gotPath != "/api/v1/mailboxes/01M/forwarding/all" {
				t.Errorf("%s %s, want %s", gotMethod, gotPath, tc.wantMethod)
			}
			for k, want := range tc.wantBody {
				if gotBody[k] != want {
					t.Errorf("body[%s] = %v, want %v", k, gotBody[k], want)
				}
			}
			if tc.wantBody == nil && gotBody != nil {
				t.Errorf("body = %v, want none", gotBody)
			}
			if !fwd.ForwardAll.Paused {
				t.Errorf("paused = false, want the document echoed back")
			}
		})
	}
}
