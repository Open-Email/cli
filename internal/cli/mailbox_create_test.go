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
		out := map[string]any{"id": "MB1", "accountId": "ACC1", "primaryAddress": nil, "ephemeral": body["ephemeral"] == true}
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
	if got := core.bodies[0]; got["primaryAddress"] != "alice@acme.test" || got["withoutAddress"] != nil || got["pimOnly"] != nil || got["ephemeral"] != nil {
		t.Fatalf("body = %v, want primaryAddress and no withoutAddress or ephemeral", got)
	}
}

func TestMailboxCreateWithoutAddressSendsNoAddress(t *testing.T) {
	core := newCreateCore(t)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "create", "--without-address")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.bodies) != 1 {
		t.Fatalf("want one create, got %v", core.paths)
	}
	if got := core.bodies[0]; got["withoutAddress"] != true || got["primaryAddress"] != nil || got["pimOnly"] != nil || got["ephemeral"] != nil {
		t.Fatalf("body = %v, want withoutAddress true, no primaryAddress or ephemeral, and not the pre-#140 pimOnly", got)
	}
	// The create is not the whole job for this shape: nothing can sign in until
	// a credential names a username, so the CLI says how.
	if !strings.Contains(errOut, "openemail credentials create MB1 --username") {
		t.Fatalf("stderr %q does not point at the credential mint", errOut)
	}
}

func TestMailboxCreateEphemeralReachesCore(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"with address", []string{"--address", "disposable@acme.test"}},
		{"without address", []string{"--without-address"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := newCreateCore(t)
			args := append([]string{"mailboxes", "create", "--ephemeral"}, tc.args...)
			_, errOut, code := runCLI(t, core.srv.URL, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if len(core.bodies) != 1 || core.bodies[0]["ephemeral"] != true {
				t.Fatalf("create bodies = %v, want exactly one create with ephemeral true", core.bodies)
			}
		})
	}
}

func TestMailboxCreateExplicitFalseOmitsEphemeral(t *testing.T) {
	core := newCreateCore(t)
	_, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "create", "--address", "alice@acme.test", "--ephemeral=false")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if len(core.bodies) != 1 {
		t.Fatalf("want one create, got %v", core.paths)
	}
	if got := core.bodies[0]; got["ephemeral"] != nil {
		t.Fatalf("body = %v, want ordinary creation to omit ephemeral", got)
	}
}

// Older Core versions may accept the create body but ignore an unknown flag.
// The resulting ordinary mailbox exists, so name it and refuse to claim the
// requested permanent-delete policy was established.
func TestMailboxCreateRequiresEphemeralAcknowledgment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		accepted bool
	}{
		{"omitted", `{"id":"MB_CREATED","primaryAddress":"disposable@acme.test"}`, false},
		{"false", `{"id":"MB_CREATED","primaryAddress":"disposable@acme.test","ephemeral":false}`, false},
		{"true", `{"id":"MB_CREATED","primaryAddress":"disposable@acme.test","ephemeral":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := newMailCore(t, accountWhoami, func(r *http.Request) (int, string) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/identities" {
					return 200, tc.response
				}
				return 404, `{"error":"not_found"}`
			})
			out, errOut, code := runCLI(t, core.srv.URL, "mailboxes", "create", "--address", "disposable@acme.test", "--ephemeral", "--json")
			calls := core.callsTo()
			if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Body["ephemeral"] != true {
				t.Fatalf("calls = %v, want one create requesting ephemeral true", calls)
			}
			if tc.accepted {
				var got map[string]any
				if code != 0 {
					t.Fatalf("exit %d: %s", code, errOut)
				}
				if err := json.Unmarshal([]byte(out), &got); err != nil || got["id"] != "MB_CREATED" || got["ephemeral"] != true {
					t.Fatalf("stdout = %q (%v), want acknowledged ephemeral mailbox", out, err)
				}
				return
			}
			if code == 0 || !strings.Contains(errOut, "MB_CREATED") || !strings.Contains(strings.ToLower(errOut), "ephemeral") {
				t.Fatalf("exit %d, stderr %q; want an ephemeral policy error naming the created identity", code, errOut)
			}
			if out != "" {
				t.Fatalf("stdout = %q, unacknowledged ephemeral creation must not emit success data", out)
			}
		})
	}
}

// Core refuses both shapes with 400; the CLI names the flags instead, and
// never sends the request.
func TestMailboxCreateTakesExactlyOneOfAddressAndWithoutAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"neither", []string{"mailboxes", "create"}, "--address is required"},
		{"empty address", []string{"mailboxes", "create", "--address", " "}, "--address is required"},
		{"both", []string{"mailboxes", "create", "--without-address", "--address", "alice@acme.test"}, "--without-address takes no --address"},
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
