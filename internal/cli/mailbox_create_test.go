package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// createCore answers POST /api/v1/identities and keeps every request it saw,
// so a test can tell "sent this body" from "never asked core at all".
type createCore struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
}

func newCreateCore(t *testing.T) *createCore {
	t.Helper()
	f := &createCore{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/identities" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		addr, _ := body["primaryAddress"].(string)
		out := map[string]any{"id": "MB1", "accountId": "ACC1", "primaryAddress": nil}
		if addr != "" {
			out["primaryAddress"] = addr
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestMailboxCreateSendsTheAddress(t *testing.T) {
	core := newCreateCore(t)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "create", "--address", "alice@acme.test")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.bodies) != 1 {
		t.Fatalf("want one create, got %v", core.paths)
	}
	if got := core.bodies[0]; got["primaryAddress"] != "alice@acme.test" || got["pimOnly"] != nil {
		t.Fatalf("body = %v, want primaryAddress and no pimOnly", got)
	}
}

func TestMailboxCreatePimOnlySendsNoAddress(t *testing.T) {
	core := newCreateCore(t)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "create", "--pim-only")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.bodies) != 1 {
		t.Fatalf("want one create, got %v", core.paths)
	}
	if got := core.bodies[0]; got["pimOnly"] != true || got["primaryAddress"] != nil {
		t.Fatalf("body = %v, want pimOnly true and no primaryAddress", got)
	}
}

// Core refuses both shapes with 400; the CLI names the flags instead, and
// never sends the request.
func TestMailboxCreateTakesExactlyOneOfAddressAndPimOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"neither", []string{"mailboxes", "create"}, "--address is required"},
		{"empty address", []string{"mailboxes", "create", "--address", " "}, "--address is required"},
		{"both", []string{"mailboxes", "create", "--pim-only", "--address", "alice@acme.test"}, "--pim-only takes no --address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := newCreateCore(t)
			_, errOut, code := runCLI(t, core.srv.URL, tc.args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2 (usage): %s", code, errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("stderr %q does not name %q", errOut, tc.want)
			}
			if len(core.paths) != 0 {
				t.Fatalf("core was asked %v; a usage error must not reach it", core.paths)
			}
		})
	}
}
