package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Open-Email/cli/internal/coreapi"
)

// A fake core that records the last domain write it received and answers with
// a minimal domain row. Only the REQUEST matters to these tests: the send-only
// switch is a wire fact (`sendOnly: true`), and the CLI's whole job here is
// to put it on the wire exactly when asked and never otherwise. Since core made
// the create/patch bodies STRICT, putting a retired spelling on the wire is a
// 400 rather than a silently dropped field, which is what these pin.
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
		_, _ = w.Write([]byte(`{"domain":"acme.dev","enabled":true,"sendOnly":true,"sendVerified":false,"sendHold":null,"sending":false,"receiving":false,"records":[]}`))
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

// Core made the domain create and patch bodies STRICT
// (`additionalProperties: false`): a field it does not recognise — a retired
// pre-0062 spelling such as canSend or canReceive above all — is now a 400
// validation_failed rather than a value quietly dropped. UpdateDomain takes a
// free-form map, so nothing in the type system stops a command putting a dead
// key on the wire; this walks the flags a user can actually pass and checks
// every key that comes out against the vendored contract.
func TestDomainWritesUseOnlyFieldsTheStrictBodyAccepts(t *testing.T) {
	create := requestBodyProperties(t, "/api/v1/domains", "post")
	patch := requestBodyProperties(t, "/api/v1/domains/{domain}", "patch")

	srv, rec := fakeDomainCore(t)
	cmd := newDomainCreateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"acme.dev", "--send-only", "--send-verified", "--fbl", "--dmarc", "--jmap", "--dav", "--itip", "--alias-of", "old.dev", "--platform"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create: %v", err)
	}
	assertBodyKeys(t, "POST /domains", rec.body, create)

	srv, rec = fakeDomainCore(t)
	cmd = newDomainUpdateCmd(domainTestApp(srv.URL))
	cmd.SetArgs([]string{"acme.dev", "--enabled", "--send-only", "--fbl", "--dmarc", "--jmap", "--dav", "--itip", "--alias-of", "old.dev"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertBodyKeys(t, "PATCH /domains/:domain", rec.body, patch)

	// The operator writes go through the same free-form map, so they are pinned
	// here too rather than trusted to have been renamed.
	for _, tc := range []struct {
		name string
		make func(*app) *cobra.Command
		args []string
	}{
		{"admin hold --pause", newAdminHoldCmd, []string{"domain", "acme.dev", "--pause"}},
		{"admin hold --stop", newAdminHoldCmd, []string{"domain", "acme.dev", "--stop"}},
		{"admin release", newAdminReleaseCmd, []string{"domain", "acme.dev"}},
		{"admin verify-sending", newAdminVerifySendingCmd, []string{"acme.dev"}},
	} {
		srv, rec = fakeDomainCore(t)
		cmd = tc.make(domainTestApp(srv.URL))
		cmd.SetArgs(tc.args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		assertBodyKeys(t, tc.name, rec.body, patch)
	}
}

func assertBodyKeys(t *testing.T, what string, body map[string]any, allowed map[string]bool) {
	t.Helper()
	if len(body) == 0 {
		t.Fatalf("%s: recorded an empty body — the walk proves nothing", what)
	}
	for k := range body {
		if !allowed[k] {
			t.Errorf("%s: sends %q, which the strict body rejects (accepted: %v)", what, k, sortedKeys(allowed))
		}
	}
}

// requestBodyProperties reads one operation's request-body property names out
// of the vendored snapshot, following a $ref when the schema is a component.
func requestBodyProperties(t *testing.T, path, method string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi.snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v (run `make sync-spec`)", err)
	}
	var spec struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	op, ok := spec.Paths[path][method]
	if !ok {
		t.Fatalf("snapshot has no %s %s", method, path)
	}
	var wrapper struct {
		RequestBody struct {
			Content map[string]struct {
				Schema struct {
					Ref        string                     `json:"$ref"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(op, &wrapper); err != nil {
		t.Fatalf("parse %s %s: %v", method, path, err)
	}
	sch := wrapper.RequestBody.Content["application/json"].Schema
	props := sch.Properties
	if sch.Ref != "" {
		name := sch.Ref[strings.LastIndex(sch.Ref, "/")+1:]
		props = spec.Components.Schemas[name].Properties
	}
	if len(props) == 0 {
		t.Fatalf("%s %s: no request-body properties in the snapshot", method, path)
	}
	out := make(map[string]bool, len(props))
	for k := range props {
		out[k] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
