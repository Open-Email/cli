package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// A fake core that records the last domain write it received and answers with
// a minimal domain row. Only the REQUEST matters to these tests: the send-only
// switch is a wire fact (`canReceive: false`), and the CLI's whole job here is
// to put it on the wire exactly when asked and never otherwise.
type domainWrites struct {
	mu     sync.Mutex
	method string
	path   string
	body   map[string]any
}

func fakeDomainCore(t *testing.T) (*httptest.Server, *domainWrites) {
	t.Helper()
	rec := &domainWrites{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.method, rec.path = r.Method, r.URL.Path
		rec.body = map[string]any{}
		_ = json.Unmarshal(raw, &rec.body)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// DomainOnboarding embeds Domain, so one flat object serves both the
		// create (domain + records) and the update (domain) responses.
		_, _ = w.Write([]byte(`{"domain":"acme.dev","enabled":true,"canSend":false,"canReceive":false,"sending":false,"receiving":false,"records":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// An app wired to the fake core the way a `--api-key` invocation would be:
// the token is explicit, so authedClient's stored-key guards do not apply.
func domainTestApp(baseURL string) *app {
	return &app{out: newPrinter(false, true), token: "oek_test", tokenSource: "flag", apiURL: baseURL}
}

// --send-only is `sendOnly: true` on the wire — the owner's mode, spelled the
// same way core spells it. The platform then relays mail for the domain to its
// own MX and leaves the MX record out of its DNS checklist.
func TestDomainCreateSendOnlyPutsSendOnlyOnTheWire(t *testing.T) {
	srv, rec := fakeDomainCore(t)
	cmd := newDomainCreateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"acme.dev", "--send-only"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create --send-only: %v", err)
	}
	if rec.method != http.MethodPost || !strings.HasSuffix(rec.path, "/domains") {
		t.Fatalf("expected POST …/domains, got %s %s", rec.method, rec.path)
	}
	if got, ok := rec.body["sendOnly"]; !ok || got != true {
		t.Fatalf("sendOnly = %v (present=%v); want true", got, ok)
	}
}

// Without the flag the CLI asserts NOTHING about receiving: core's default is
// to receive, and a re-run of `create` on an owned domain is the onboarding
// advance — it must not start writing a value the caller never chose.
func TestDomainCreateOmitsSendOnlyByDefault(t *testing.T) {
	srv, rec := fakeDomainCore(t)
	cmd := newDomainCreateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"acme.dev"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, present := rec.body["sendOnly"]; present {
		t.Fatalf("sendOnly must be omitted when --send-only was not given; body = %v", rec.body)
	}
}

// `update --send-only` flips receiving off here; `--send-only=false` turns it
// back on. Both are the one `sendOnly` field, nothing else in the patch.
func TestDomainUpdateSendOnlyPatchesSendOnly(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want bool
	}{{"--send-only", true}, {"--send-only=false", false}} {
		srv, rec := fakeDomainCore(t)
		cmd := newDomainUpdateCmd(domainTestApp(srv.URL))
		cmd.SetArgs([]string{"acme.dev", tc.flag})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("update %s: %v", tc.flag, err)
		}
		if rec.method != http.MethodPatch {
			t.Fatalf("%s: expected PATCH, got %s", tc.flag, rec.method)
		}
		if got := rec.body["sendOnly"]; got != tc.want {
			t.Fatalf("%s: sendOnly = %v; want %v", tc.flag, got, tc.want)
		}
		if len(rec.body) != 1 {
			t.Fatalf("%s: patch carries more than the one switch: %v", tc.flag, rec.body)
		}
	}
}

// `get` must name the MODE: a bare "no" reads as a fault to anyone who does not
// already know that receiving off means the inbound MX lives elsewhere.
func TestReceiveModeNamesSendOnly(t *testing.T) {
	if got := receiveMode(&coreapi.Domain{Enabled: true, Receiving: true}); got != "yes" {
		t.Fatalf("receiving domain rendered %q; want yes", got)
	}
	got := receiveMode(&coreapi.Domain{Enabled: true, SendOnly: true})
	for _, want := range []string{"send-only", "MX", "relayed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("send-only rendered %q; want it to mention %q", got, want)
		}
	}
}
