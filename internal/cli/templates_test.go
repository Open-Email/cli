package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

/*
 * Prepared emails and platform mail from the terminal (core
 * docs/templated-mail-design.md §VI, §VII and §X "CLI"): `templates` under an
 * account key, `admin mail-slots` under an operator's, and the two account
 * settings templated mail added to `accounts`.
 *
 * These drive the REAL command tree through Execute against a fake core, so
 * they pin what a person types and what goes over the wire, exit code
 * included, rather than a helper one layer down.
 */

// coreCall is one request the fake core received.
type coreCall struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any
}

// mailCore answers `whoami` as the principal given and everything else through
// `answer`, recording every call.
type mailCore struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []coreCall
	whoami string
	answer func(r *http.Request) (int, string)
}

func newMailCore(t *testing.T, whoami string, answer func(r *http.Request) (int, string)) *mailCore {
	t.Helper()
	f := &mailCore{t: t, whoami: whoami, answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := coreCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Body); err != nil {
				t.Errorf("request body is not a JSON object: %s", raw)
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/whoami" {
			_, _ = w.Write([]byte(f.whoami))
			return
		}
		status, body := 404, `{"error":"not_found"}`
		if f.answer != nil {
			status, body = f.answer(r)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// callsTo returns the recorded calls other than whoami.
func (f *mailCore) callsTo() []coreCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []coreCall{}
	for _, c := range f.calls {
		if c.Path != "/api/v1/auth/whoami" {
			out = append(out, c)
		}
	}
	return out
}

const (
	accountWhoami = `{"type":"account","accountId":"ACC1","keyId":"K1","domains":null}`
	systemWhoami  = `{"type":"system","accountId":null,"keyId":"KS","domains":null}`
)

// runCLI executes the real command tree with args against core at apiURL and
// returns stdout, stderr and the exit code.
func runCLI(t *testing.T, apiURL string, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	t.Setenv("OPENEMAIL_NO_UPDATE_NOTIFIER", "1")
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", apiURL)
	t.Setenv("OPENEMAIL_API_KEY", "oek_test")
	t.Setenv("NO_COLOR", "1")

	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Args = append([]string{"openemail"}, args...)
	os.Stdout, os.Stderr = outW, errW
	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&stdout, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&stderr, errR) }()

	code := Execute()

	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	os.Args, os.Stdout, os.Stderr = oldArgs, oldOut, oldErr
	return stdout.String(), stderr.String(), code
}

// writeFile puts content in a temp file and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func bodyJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const cliTemplate = `{"slug":"order-shipped","name":"Order shipped","from":"orders@acme.test","replyTo":null,` +
	`"variables":{"orderRef":{"description":"The order","sample":"A-1"}},"defaultLang":"en","version":3,` +
	`"languages":["en","de"],"createdAt":1757000000,"updatedAt":1757700000}`

const cliDetail = `{"slug":"order-shipped","name":"Order shipped","from":"orders@acme.test","replyTo":null,` +
	`"variables":{"orderRef":{"description":"The order","sample":"A-1"}},"defaultLang":"en","version":3,` +
	`"languages":["en","de"],"createdAt":1757000000,"updatedAt":1757700000,"variants":[` +
	`{"lang":"de","subject":"Versandt {{orderRef}}","text":"Hallo","html":null,"updatedAt":1757600000},` +
	`{"lang":"en","subject":"Shipped {{orderRef}}","text":"Hello","html":"<p>Hello</p>","updatedAt":1757700000}]}`

/* ── templates: whose account ─────────────────────────────────────────────── */

func TestTemplatesListUsesTheKeysOwnAccount(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"templates":[` + cliTemplate + `]}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	calls := core.callsTo()
	if len(calls) != 1 || calls[0].Method != "GET" || calls[0].Path != "/api/v1/accounts/ACC1/templates" {
		t.Fatalf("calls = %+v", calls)
	}
	for _, want := range []string{"order-shipped", "Order shipped", "orders@acme.test", "en, de"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
}

func TestTemplatesListAsJSONIsCoresAnswer(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"templates":[` + cliTemplate + `]}`
	})
	out, _, code := runCLI(t, core.srv.URL, "--json", "templates", "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 || got[0]["slug"] != "order-shipped" {
		t.Fatalf("--json output = %s (%v)", out, err)
	}
}

func TestTemplatesNeedAnAccountForASystemKey(t *testing.T) {
	core := newMailCore(t, systemWhoami, nil)
	_, errOut, code := runCLI(t, core.srv.URL, "templates", "list")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage): %s", code, errOut)
	}
	if !strings.Contains(errOut, "--account") {
		t.Fatalf("the refusal should name --account: %s", errOut)
	}
	if len(core.callsTo()) != 0 {
		t.Fatalf("no template call may be made: %+v", core.callsTo())
	}
}

func TestTemplatesTakeAnExplicitAccountFromASystemKey(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, `{"templates":[]}` })
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "list", "--account", "ACC9"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := core.callsTo()[0].Path; got != "/api/v1/accounts/ACC9/templates" {
		t.Fatalf("path = %s", got)
	}
}

func TestTemplatePositionalsAreCheckedBeforeAnyRequest(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	for _, args := range [][]string{
		{"templates", "get", "Order Shipped"},
		{"templates", "get", "_lead"},
		{"templates", "variants", "get", "order-shipped", "english_US"},
		{"admin", "mail-slots", "get", "Not A Slot"},
	} {
		_, errOut, code := runCLI(t, core.srv.URL, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2: %s", args, code, errOut)
		}
	}
	if len(core.callsTo()) != 0 {
		t.Fatalf("no request may leave for a malformed argument: %+v", core.callsTo())
	}
}

/* ── templates: the directory ─────────────────────────────────────────────── */

func TestTemplatesGetShowsDetailsAndLanguages(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliDetail })
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "get", "order-shipped")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := core.callsTo()[0].Path; got != "/api/v1/accounts/ACC1/templates/order-shipped" {
		t.Fatalf("path = %s", got)
	}
	for _, want := range []string{"orders@acme.test", "Shipped {{orderRef}}", "Versandt {{orderRef}}", "orderRef", "The order", "A-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output lacks %q:\n%s", want, out)
		}
	}
}

func TestTemplatesCreateSendsTheDeclarationsFile(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliTemplate })
	vars := writeFile(t, "vars.json", `{"orderRef":{"description":"The order","sample":"A-1"}}`)
	_, errOut, code := runCLI(t, core.srv.URL, "templates", "create", "order-shipped",
		"--name", "Order shipped", "--from", "orders@acme.test", "--reply-to", "help@acme.test",
		"--default-lang", "en", "--variables-file", vars)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := core.callsTo()[0]
	want := map[string]any{
		"slug": "order-shipped", "name": "Order shipped", "from": "orders@acme.test", "replyTo": "help@acme.test",
		"defaultLang": "en", "variables": map[string]any{"orderRef": map[string]any{"description": "The order", "sample": "A-1"}},
	}
	if c.Method != "POST" || c.Path != "/api/v1/accounts/ACC1/templates" || bodyJSON(t, c.Body) != bodyJSON(t, want) {
		t.Fatalf("call = %s %s %v", c.Method, c.Path, c.Body)
	}
}

func TestTemplatesCreateRequiresNameAndFrom(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	if _, _, code := runCLI(t, core.srv.URL, "templates", "create", "order-shipped", "--name", "X"); code != 2 {
		t.Fatalf("exit %d, want 2 without --from", code)
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("no request without --from")
	}
}

func TestTemplatesUpdateSendsOnlyWhatChanged(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliTemplate })
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "update", "order-shipped", "--reply-to", "", "--default-lang", "de"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := core.callsTo()[0]
	// An emptied --reply-to clears it (JSON null); nothing else is sent.
	if c.Method != "PATCH" || bodyJSON(t, c.Body) != `{"defaultLang":"de","replyTo":null}` {
		t.Fatalf("call = %s %v", c.Method, c.Body)
	}
}

func TestTemplatesUpdateRefusesAnEmptyPatch(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	if _, _, code := runCLI(t, core.srv.URL, "templates", "update", "order-shipped"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestTemplatesDeleteConfirms(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","deleted":true}`
	})
	// No TTY to answer the prompt: refused without --yes, and nothing sent.
	if _, _, code := runCLI(t, core.srv.URL, "templates", "delete", "order-shipped"); code != 2 {
		t.Fatalf("exit %d, want 2 without --yes", code)
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("nothing may be deleted without confirmation")
	}
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "delete", "order-shipped", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if c := core.callsTo()[0]; c.Method != "DELETE" || c.Path != "/api/v1/accounts/ACC1/templates/order-shipped" {
		t.Fatalf("call = %s %s", c.Method, c.Path)
	}
}

/* ── templates: one language's copy ───────────────────────────────────────── */

func TestTemplateVariantPutReadsBodiesFromFiles(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","lang":"de","stored":true,"changed":true}`
	})
	text := writeFile(t, "de.txt", "Hallo {{orderRef}}\n")
	html := writeFile(t, "de.html", "<p>Hallo</p>\n")
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "variants", "put", "order-shipped", "de",
		"--subject", "Versandt {{orderRef}}", "--text-file", text, "--html-file", html)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := core.callsTo()[0]
	want := map[string]any{"subject": "Versandt {{orderRef}}", "text": "Hallo {{orderRef}}\n", "html": "<p>Hallo</p>\n"}
	if c.Method != "PUT" || c.Path != "/api/v1/accounts/ACC1/templates/order-shipped/variants/de" || bodyJSON(t, c.Body) != bodyJSON(t, want) {
		t.Fatalf("call = %s %s %v", c.Method, c.Path, c.Body)
	}
	// Status lines go to stderr, keeping stdout for data.
	if out != "" || !strings.Contains(errOut, "next send") {
		t.Errorf("a stored change should say, on stderr, it is live from the next send:\nstdout: %s\nstderr: %s", out, errOut)
	}
}

func TestTemplateVariantPutWithoutHTMLSendsNull(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","lang":"de","stored":true,"changed":false}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "variants", "put", "order-shipped", "de", "--subject", "S", "--text", "T")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := bodyJSON(t, core.callsTo()[0].Body); got != `{"html":null,"subject":"S","text":"T"}` {
		t.Fatalf("body = %s", got)
	}
	if !strings.Contains(strings.ToLower(errOut), "unchanged") {
		t.Errorf("an identical PUT should say nothing changed:\n%s%s", out, errOut)
	}
}

func TestTemplateVariantPutNeedsSubjectAndText(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	if _, _, code := runCLI(t, core.srv.URL, "templates", "variants", "put", "order-shipped", "de", "--subject", "S"); code != 2 {
		t.Fatalf("exit %d, want 2 without a text body", code)
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("no request without a text body")
	}
}

func TestTemplateVariantGetPrintsTheSource(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliDetail })
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "variants", "get", "order-shipped", "en")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"Shipped {{orderRef}}", "Hello", "<p>Hello</p>"} {
		if !strings.Contains(out, want) {
			t.Errorf("variant output lacks %q:\n%s", want, out)
		}
	}
	// A language with no copy is an error naming it, not an empty print.
	_, errOut, code = runCLI(t, core.srv.URL, "templates", "variants", "get", "order-shipped", "fr")
	if code == 0 || !strings.Contains(errOut, "fr") {
		t.Fatalf("missing language: exit %d, %s", code, errOut)
	}
}

func TestTemplateVariantDelete(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","lang":"de","removed":true}`
	})
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "variants", "delete", "order-shipped", "de", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if c := core.callsTo()[0]; c.Method != "DELETE" || c.Path != "/api/v1/accounts/ACC1/templates/order-shipped/variants/de" {
		t.Fatalf("call = %s %s", c.Method, c.Path)
	}
}

/* ── templates: render and send ───────────────────────────────────────────── */

const cliRendered = `{"slug":"order-shipped","langUsed":"de","templateVersion":3,"subject":"Versandt A-9","text":"Hallo",` +
	`"html":null,"source":"stored","variables":[{"name":"orderRef","description":"The order","sample":"A-1","bodies":["subject"],"filledFromSample":false}]}`

func TestTemplatesRenderSendsValuesAndPrintsTheMessage(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliRendered })
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "render", "order-shipped",
		"--lang", "de", "--var", "orderRef=A-9", "--var", "note=a=b", "--preview")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := core.callsTo()[0]
	// A value may itself contain '=': only the first one splits.
	want := map[string]any{"lang": "de", "vars": map[string]any{"orderRef": "A-9", "note": "a=b"}, "preview": true}
	if c.Path != "/api/v1/accounts/ACC1/templates/order-shipped/render" || bodyJSON(t, c.Body) != bodyJSON(t, want) {
		t.Fatalf("call = %s %v", c.Path, c.Body)
	}
	for _, want := range []string{"Versandt A-9", "Hallo", "orderRef"} {
		if !strings.Contains(out, want) {
			t.Errorf("render output lacks %q:\n%s", want, out)
		}
	}
}

func TestTemplatesRenderTypedValuesComeFromTheVarsFile(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) { return 200, cliRendered })
	vars := writeFile(t, "vars.json", `{"vip":true,"total":12,"orderRef":"A-1"}`)
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "render", "order-shipped", "--vars-file", vars, "--var", "orderRef=A-2"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	// The file keeps its types; a --var given as well wins for its name.
	want := map[string]any{"vars": map[string]any{"vip": true, "total": 12, "orderRef": "A-2"}}
	if got := bodyJSON(t, core.callsTo()[0].Body); got != bodyJSON(t, want) {
		t.Fatalf("body = %s", got)
	}
}

func TestTemplatesRenderPreviewsADraftWithoutStoringIt(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, strings.Replace(cliRendered, `"source":"stored"`, `"source":"draft"`, 1)
	})
	text := writeFile(t, "draft.txt", "Neu {{orderRef}}")
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "render", "order-shipped", "--lang", "de",
		"--subject", "Neu", "--text-file", text, "--preview")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := map[string]any{"lang": "de", "preview": true, "draft": map[string]any{"subject": "Neu", "text": "Neu {{orderRef}}", "html": nil}}
	if got := bodyJSON(t, core.callsTo()[0].Body); got != bodyJSON(t, want) {
		t.Fatalf("body = %s", got)
	}
	if !strings.Contains(strings.ToLower(errOut), "draft") {
		t.Errorf("a draft render should say it rendered a draft:\n%s%s", out, errOut)
	}
	if len(core.callsTo()) != 1 {
		t.Fatalf("a draft render is ONE call, and stores nothing: %+v", core.callsTo())
	}
}

func TestTemplatesRenderRefusesAMalformedVar(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	if _, _, code := runCLI(t, core.srv.URL, "templates", "render", "order-shipped", "--var", "orderRef"); code != 2 {
		t.Fatalf("exit %d, want 2 for a --var with no '='", code)
	}
}

func TestTemplatesSendToEachRecipientWithTheBatchID(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","langUsed":"en","templateVersion":3,"deliveryId":"batch-7","results":[` +
			`{"address":"a@remote.test","status":"queued","deliveryId":"batch-7.r1"},` +
			`{"address":"b@remote.test","status":"delivered","deliveryId":"batch-7.r2"}]}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "send", "order-shipped",
		"--to", "a@remote.test", "--to", "b@remote.test", "--var", "orderRef=A-1", "--lang", "en", "--delivery-id", "batch-7")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	c := core.callsTo()[0]
	want := map[string]any{
		"lang": "en", "vars": map[string]any{"orderRef": "A-1"},
		"to": []any{map[string]any{"email": "a@remote.test"}, map[string]any{"email": "b@remote.test"}},
	}
	if c.Path != "/api/v1/accounts/ACC1/templates/order-shipped/send" || bodyJSON(t, c.Body) != bodyJSON(t, want) {
		t.Fatalf("call = %s %v", c.Path, c.Body)
	}
	if c.Header.Get("X-Delivery-Id") != "batch-7" {
		t.Fatalf("X-Delivery-Id = %q", c.Header.Get("X-Delivery-Id"))
	}
	for _, want := range []string{"a@remote.test", "queued", "b@remote.test", "delivered"} {
		if !strings.Contains(out, want) {
			t.Errorf("send output lacks %q:\n%s", want, out)
		}
	}
}

func TestTemplatesSendMintsABatchIDWhenNoneIsGiven(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","langUsed":"en","templateVersion":3,"deliveryId":"x","results":[{"address":"a@remote.test","status":"queued","deliveryId":"x.r1"}]}`
	})
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "send", "order-shipped", "--to", "a@remote.test"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if id := core.callsTo()[0].Header.Get("X-Delivery-Id"); len(id) != 26 {
		t.Fatalf("X-Delivery-Id = %q; want a fresh ULID so a retry can converge", id)
	}
}

func TestTemplatesSendReadsPerRecipientValuesFromAFile(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","langUsed":"en","templateVersion":3,"deliveryId":"x","results":[]}`
	})
	to := writeFile(t, "to.json", `[{"email":"a@remote.test","name":"Ann","vars":{"orderRef":"A-1"}},{"email":"b@remote.test","vars":{"orderRef":"B-2"}}]`)
	if _, errOut, code := runCLI(t, core.srv.URL, "templates", "send", "order-shipped", "--to-file", to); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := []any{
		map[string]any{"email": "a@remote.test", "name": "Ann", "vars": map[string]any{"orderRef": "A-1"}},
		map[string]any{"email": "b@remote.test", "vars": map[string]any{"orderRef": "B-2"}},
	}
	if got := bodyJSON(t, core.callsTo()[0].Body["to"]); got != bodyJSON(t, want) {
		t.Fatalf("to = %s", got)
	}
}

func TestTemplatesSendNeedsARecipient(t *testing.T) {
	core := newMailCore(t, accountWhoami, nil)
	if _, _, code := runCLI(t, core.srv.URL, "templates", "send", "order-shipped"); code != 2 {
		t.Fatalf("exit %d, want 2 with no recipient", code)
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("nothing may be sent with no recipient")
	}
}

func TestTemplatesSendReportsAPartialBatchAndHowToRetryIt(t *testing.T) {
	core := newMailCore(t, accountWhoami, func(*http.Request) (int, string) {
		return 207, `{"slug":"order-shipped","langUsed":"en","templateVersion":3,"deliveryId":"batch-9","results":[` +
			`{"address":"a@remote.test","status":"queued","deliveryId":"batch-9.r1"},` +
			`{"address":"b@remote.test","status":"failed","error":"rate_limited","retryAfterSeconds":120,"deliveryId":"batch-9.r2"}]}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "templates", "send", "order-shipped",
		"--to", "a@remote.test", "--to", "b@remote.test", "--delivery-id", "batch-9")
	if code != 1 {
		t.Fatalf("exit %d, want 1 when a recipient failed", code)
	}
	if !strings.Contains(out, "rate_limited") {
		t.Errorf("the failed row should carry core's reason:\n%s", out)
	}
	// The retry hint names the SAME batch id: resending with it converges, so
	// the recipient already sent to is not mailed twice.
	if !strings.Contains(errOut+out, "--delivery-id batch-9") {
		t.Errorf("a partial batch should say how to retry it:\n%s\n%s", out, errOut)
	}
}

/* ── admin mail-slots ─────────────────────────────────────────────────────── */

const cliSlots = `{"slots":[{"slug":"oe_recovery_reset_code","title":"Reset code","channel":"email_sending",` +
	`"constraints":["no_urls","text_only"],"variables":[{"name":"code","required":true,"description":"The code","sample":"123456"}],` +
	`"defaultLanguages":["en"],"overriddenLanguages":["de"]}]}`

const cliSlot = `{"slug":"oe_recovery_reset_code","title":"Reset code","channel":"email_sending","constraints":["no_urls","text_only"],` +
	`"variables":[{"name":"code","required":true,"description":"The code","sample":"123456"}],"defaultLanguages":["en"],` +
	`"defaults":[{"lang":"en","subject":"Your code","text":"Code: {{code}}"}],` +
	`"overrides":[{"lang":"de","subject":"Ihr Code","text":"Code: {{code}}","html":null,"updatedAt":1757700000}]}`

func TestMailSlotsListShowsTheRegistry(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, cliSlots })
	out, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := core.callsTo()[0].Path; got != "/api/v1/system/mail-slots" {
		t.Fatalf("path = %s", got)
	}
	for _, want := range []string{"oe_recovery_reset_code", "Reset code", "no_urls", "de"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
}

func TestMailSlotsGetShowsContractDefaultAndOverride(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, cliSlot })
	out, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "get", "oe_recovery_reset_code")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"code", "The code", "123456", "Your code", "Ihr Code"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output lacks %q:\n%s", want, out)
		}
	}
}

func TestMailSlotsPutAndRevert(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(r *http.Request) (int, string) {
		if r.Method == http.MethodDelete {
			return 200, `{"slug":"oe_recovery_reset_code","lang":"de","removed":true}`
		}
		return 200, `{"slug":"oe_recovery_reset_code","lang":"de","stored":true}`
	})
	text := writeFile(t, "de.txt", "Code: {{code}}")
	if _, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "put", "oe_recovery_reset_code", "de",
		"--subject", "Ihr Code", "--text-file", text); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if _, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "delete", "oe_recovery_reset_code", "de", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	calls := core.callsTo()
	if calls[0].Method != "PUT" || calls[0].Path != "/api/v1/system/mail-slots/oe_recovery_reset_code/variants/de" ||
		bodyJSON(t, calls[0].Body) != `{"html":null,"subject":"Ihr Code","text":"Code: {{code}}"}` {
		t.Fatalf("put = %s %s %v", calls[0].Method, calls[0].Path, calls[0].Body)
	}
	if calls[1].Method != "DELETE" || calls[1].Path != "/api/v1/system/mail-slots/oe_recovery_reset_code/variants/de" {
		t.Fatalf("delete = %s %s", calls[1].Method, calls[1].Path)
	}
}

func TestMailSlotsRenderADraft(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"oe_recovery_reset_code","langUsed":"de","source":"draft","subject":"Neu","text":"Code: 123456","html":null,"variables":[]}`
	})
	out, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "render", "oe_recovery_reset_code",
		"--lang", "de", "--subject", "Neu", "--text", "Code: {{code}}")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := map[string]any{"lang": "de", "draft": map[string]any{"subject": "Neu", "text": "Code: {{code}}", "html": nil}}
	if c := core.callsTo()[0]; c.Path != "/api/v1/system/mail-slots/oe_recovery_reset_code/render" || bodyJSON(t, c.Body) != bodyJSON(t, want) {
		t.Fatalf("call = %s %v", c.Path, c.Body)
	}
	if !strings.Contains(out, "Code: 123456") {
		t.Errorf("render output lacks the rendered text:\n%s", out)
	}
}

func TestMailSlotsTestSendsToTheNamedAddress(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) {
		return 200, `{"slug":"oe_recovery_reset_code","langUsed":"de","sent":true}`
	})
	if _, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "test", "oe_recovery_reset_code",
		"--to", "ops@acme.test", "--lang", "de"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if c := core.callsTo()[0]; c.Path != "/api/v1/system/mail-slots/oe_recovery_reset_code/test" ||
		bodyJSON(t, c.Body) != `{"lang":"de","to":"ops@acme.test"}` {
		t.Fatalf("call = %s %v", c.Path, c.Body)
	}
	// --to is required: a test send needs somebody to receive it.
	if _, _, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "test", "oe_recovery_reset_code"); code != 2 {
		t.Fatalf("exit %d, want 2 without --to", code)
	}
}

func TestMailSlotsTestSaysWhySendingRefused(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) {
		return 502, `{"error":"platform_refused","reason":"slot_test_budget"}`
	})
	_, errOut, code := runCLI(t, core.srv.URL, "admin", "mail-slots", "test", "oe_recovery_reset_code", "--to", "ops@acme.test")
	if code == 0 || !strings.Contains(errOut, "slot_test_budget") {
		t.Fatalf("exit %d; the refusal should carry core's reason: %s", code, errOut)
	}
}

/* ── accounts: the two settings templated mail added ──────────────────────── */

const cliAccountID = "01KXRS3SHN1N35G4YETVADSN0R"

const cliAccount = `{"id":"01KXRS3SHN1N35G4YETVADSN0R","name":"Acme","maxMailboxes":null,"maxMailTemplates":5,` +
	`"noticeLanguage":"de","sendHold":null,"sendMsgsPerDay":null,"sendRcptsPerDay":null,"storageLimitBytes":null,` +
	`"vanityHosts":false,"semantic":false,"createdAt":1757000000}`

func TestAccountsUpdateSetsTheTemplateCeilingAndNoticeLanguage(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--max-mail-templates", "5"}, `{"maxMailTemplates":5}`},
		// 0 is core's unlimited; null is the platform default.
		{[]string{"--max-mail-templates", "unlimited"}, `{"maxMailTemplates":0}`},
		{[]string{"--max-mail-templates", "default"}, `{"maxMailTemplates":null}`},
		{[]string{"--notice-language", "DE-at"}, `{"noticeLanguage":"de-at"}`},
		{[]string{"--notice-language", "default"}, `{"noticeLanguage":null}`},
	} {
		core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, cliAccount })
		args := append([]string{"accounts", "update", cliAccountID}, tc.args...)
		if _, errOut, code := runCLI(t, core.srv.URL, args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", tc.args, code, errOut)
		}
		c := core.callsTo()[0]
		if c.Method != "PATCH" || bodyJSON(t, c.Body) != tc.want {
			t.Errorf("%v: body = %s, want %s", tc.args, bodyJSON(t, c.Body), tc.want)
		}
	}
}

func TestAccountsUpdateRefusesABadCeilingOrLanguage(t *testing.T) {
	core := newMailCore(t, systemWhoami, nil)
	for _, args := range [][]string{
		{"--max-mail-templates", "-1"},
		{"--max-mail-templates", "lots"},
		{"--notice-language", "english"},
		{"--notice-language", "x_y"},
	} {
		if _, _, code := runCLI(t, core.srv.URL, append([]string{"accounts", "update", cliAccountID}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if len(core.callsTo()) != 0 {
		t.Fatal("no request for a malformed value")
	}
}

func TestAccountsGetShowsTheTemplateCeilingAndNoticeLanguage(t *testing.T) {
	core := newMailCore(t, systemWhoami, func(*http.Request) (int, string) { return 200, cliAccount })
	out, errOut, code := runCLI(t, core.srv.URL, "accounts", "get", cliAccountID)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "Mail templates") || !strings.Contains(out, "Notice language") {
		t.Fatalf("account view lacks the two rows:\n%s", out)
	}
	if !strings.Contains(out, "German (de)") && !strings.Contains(out, " de") {
		t.Errorf("notice language not shown:\n%s", out)
	}
}

func TestFmtTemplateCeilingNamesTheThreeStates(t *testing.T) {
	five, zero := int64(5), int64(0)
	if got := fmtTemplateCeiling(nil); got != "platform default" {
		t.Errorf("nil = %q", got)
	}
	if got := fmtTemplateCeiling(&zero); got != "unlimited" {
		t.Errorf("0 = %q", got)
	}
	if got := fmtTemplateCeiling(&five); got != "5" {
		t.Errorf("5 = %q", got)
	}
}
