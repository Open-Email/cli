package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/config"
)

// scopeCore is a core whose mint either echoes the domain scope it was given
// or — the case that matters — silently ignores it, which is exactly what a
// deployment older than migration 0078 does: the request schema strips unknown
// keys, so `domains` is dropped and an ACCOUNT-WIDE key comes back 200.
type scopeCore struct {
	*httptest.Server
	mu           sync.Mutex
	echo         bool
	sentDomains  []string
	revoked      []string
	revokeStatus int
}

func newScopeCore(echo bool) *scopeCore {
	sc := &scopeCore{echo: echo}
	sc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		sc.mu.Lock()
		defer sc.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api-keys"):
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Domains []string `json:"domains"`
			}
			_ = json.Unmarshal(raw, &body)
			sc.sentDomains = body.Domains
			out := map[string]any{"id": "key_new", "name": "delegate", "token": "oek_new", "role": "account"}
			if sc.echo {
				out["domains"] = body.Domains
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodDelete:
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			sc.revoked = append(sc.revoked, parts[len(parts)-1])
			if sc.revokeStatus != 0 {
				w.WriteHeader(sc.revokeStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "internal_error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"revoked": true})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
		}
	}))
	return sc
}

func (s *scopeCore) revokedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

func (s *scopeCore) domainsSent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sentDomains...)
}

// keysFixture is an app authenticated by an explicit token, which is the
// `--api-key` path: it skips the stored-key/host guard that has nothing to do
// with this command.
func keysFixture(t *testing.T, apiURL string) *app {
	t.Helper()
	a := loginFixture(t, apiURL, config.Profile{Role: "account"})
	a.token = "oek_test"
	a.tokenSource = "flag"
	return a
}

func runKeysCreate(t *testing.T, a *app, args ...string) error {
	t.Helper()
	cmd := newKeysCreateCmd(a)
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

// The ordinary path: a scope asked for, recorded, and echoed.
func TestKeysCreateSendsTheDomainScope(t *testing.T) {
	core := newScopeCore(true)
	defer core.Close()
	a := keysFixture(t, core.URL)

	if err := runKeysCreate(t, a, "delegate", "--domain", "a.test", "--domain", "b.test"); err != nil {
		t.Fatalf("keys create: %v", err)
	}
	if got := core.domainsSent(); len(got) != 2 || got[0] != "a.test" || got[1] != "b.test" {
		t.Fatalf("domains sent = %v, want [a.test b.test]", got)
	}
	// Nothing to undo when the mint did what was asked.
	if got := core.revokedKeys(); len(got) != 0 {
		t.Fatalf("revoked %v, want none", got)
	}
}

// THE PROPERTY THIS COMMAND EXISTS TO HOLD. A core that ignores the scope hands
// back a key over the whole account. Printing it — even with a warning — would
// put a wider credential than the one asked for into somebody's terminal and
// then their deployment, with no way to narrow it afterwards and nothing later
// in the key's life to notice. So the key is revoked and the command fails.
func TestKeysCreateRevokesAKeyMintedWithoutTheScope(t *testing.T) {
	core := newScopeCore(false)
	defer core.Close()
	a := keysFixture(t, core.URL)

	err := runKeysCreate(t, a, "delegate", "--domain", "a.test")
	if err == nil {
		t.Fatal("want a refusal when core ignored --domain, got success")
	}
	if !strings.Contains(err.Error(), "unscoped") {
		t.Fatalf("error should name the cause, got %v", err)
	}
	if got := core.revokedKeys(); len(got) != 1 || got[0] != "key_new" {
		t.Fatalf("revoked %v, want [key_new] — the key exists on the account until it is", got)
	}
}

// A revoke that fails leaves a live key nobody asked for, so the error has to
// name it: at that point only a human can finish the job.
func TestKeysCreateNamesTheKeyItCouldNotRevoke(t *testing.T) {
	core := newScopeCore(false)
	core.revokeStatus = http.StatusInternalServerError
	defer core.Close()
	a := keysFixture(t, core.URL)

	err := runKeysCreate(t, a, "delegate", "--domain", "a.test")
	if err == nil {
		t.Fatal("want a refusal, got success")
	}
	if !strings.Contains(err.Error(), "key_new") {
		t.Fatalf("error must name the stranded key, got %v", err)
	}
}

// And the check must not fire on the ordinary account-wide mint, which every
// existing caller makes and which echoes no scope precisely because none was
// asked for.
func TestKeysCreateWithoutAScopeIsUnaffected(t *testing.T) {
	core := newScopeCore(false)
	defer core.Close()
	a := keysFixture(t, core.URL)

	if err := runKeysCreate(t, a, "production"); err != nil {
		t.Fatalf("unscoped mint should succeed: %v", err)
	}
	if got := core.domainsSent(); got != nil {
		t.Fatalf("no scope asked for, so none should be sent: %v", got)
	}
	if got := core.revokedKeys(); len(got) != 0 {
		t.Fatalf("revoked %v, want none", got)
	}
}
