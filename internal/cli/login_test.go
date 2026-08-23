package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

// The lifecycle half of `login`: which key this machine ends up holding, and
// what happens to the one it held before.
//
// The browser flow makes that question sharper than it looks. The console does
// the minting, so it is tempting to treat a browser login as "somebody else's
// key, stored as-is" — but the key belongs to THIS machine exactly as a
// self-minted one does, and if it is not recorded that way the CLI leaks one
// live key per re-login and strands a fresh one whenever storing it fails.

// fakeCore answers the two calls `login` makes: whoami, and the key surface.
type fakeCore struct {
	*httptest.Server
	mu      sync.Mutex
	revoked []string
	minted  int
	// mintStatus, when non-zero, is what POST /api-keys answers instead of a key.
	mintStatus int
	mintCode   string
	// revokeStatus, when non-zero, is what DELETE /api-keys/:id answers — an
	// outage, a 500, a deployment that is momentarily not there. The attempt is
	// still recorded, so a case can assert the CLI tried.
	revokeStatus int
}

// failRevocations makes every DELETE answer status. Under the same lock the
// handler reads it with, since the handler runs on the server's goroutine.
func (f *fakeCore) failRevocations(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeStatus = status
}

func newFakeCore() *fakeCore {
	fc := &fakeCore{}
	fc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "account", "accountId": "acc_1", "keyId": "key_current",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api-keys"):
			fc.mu.Lock()
			defer fc.mu.Unlock()
			if fc.mintStatus != 0 {
				w.WriteHeader(fc.mintStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": fc.mintCode})
				return
			}
			fc.minted++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "key_minted", "name": "cli", "accountId": "acc_1", "token": "oek_minted",
			})
		case r.Method == http.MethodDelete:
			fc.mu.Lock()
			defer fc.mu.Unlock()
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			fc.revoked = append(fc.revoked, parts[len(parts)-1])
			if fc.revokeStatus != 0 {
				w.WriteHeader(fc.revokeStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "internal_error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"revoked": true})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
		}
	}))
	return fc
}

func (f *fakeCore) revokedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// capturingPrinter keeps the warnings a case is about; data output stays
// discarded, as in quietPrinter.
func capturingPrinter() (*Printer, *bytes.Buffer) {
	warnings := &bytes.Buffer{}
	return &Printer{out: io.Discard, err: warnings}, warnings
}

// loginFixture is an `app` pointed at throwaway config and secret storage.
func loginFixture(t *testing.T, apiURL string, profile config.Profile) *app {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The OS keychain is not this test's business, and prompting for it would
	// hang CI — the file backend writes under the temp config dir above.
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	t.Setenv("OPENEMAIL_API_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile.APIURL = apiURL
	cfg.SetProfile("default", profile)
	return &app{
		cfg:         cfg,
		out:         quietPrinter(),
		profileName: "default",
		profile:     profile,
		apiURL:      apiURL,
	}
}

// runLoginWith drives runLogin with a real (cancellable) command context.
func runLoginWith(t *testing.T, a *app, opts loginOpts) error {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return a.runLogin(cmd, opts)
}

// consoleIssuing stands in for the console: it hands back a key it minted.
func consoleIssuing(keyID string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "oek_from_console", "key_id": keyID, "key_name": "openemail-cli @ laptop",
		})
	}))
}

func TestBrowserLoginOwnsItsKey(t *testing.T) {
	core := newFakeCore()
	defer core.Close()
	console := consoleIssuing("key_from_console")
	defer console.Close()

	// A profile that already holds a CLI key — the one a re-login replaces.
	a := loginFixture(t, core.URL, config.Profile{
		Role:       "account",
		KeyID:      "key_previous",
		KeyStorage: secrets.File,
		ConsoleURL: console.URL,
	})

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	if err := runLoginWith(t, a, loginOpts{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	spy.check(t)

	// The console minted; the CLI must not mint a second key for one machine.
	if core.minted != 0 {
		t.Fatalf("the CLI minted %d keys of its own after a browser login", core.minted)
	}

	stored, _ := a.cfg.Profile("default")
	if stored.KeyID != "key_from_console" {
		t.Fatalf("profile records key %q, want the console's", stored.KeyID)
	}
	// The point of the test: a browser login owns its key, so the key it
	// replaced is revoked. Recording it as somebody else's would leak one live
	// key per re-login, silently, forever.
	if got := core.revokedKeys(); len(got) != 1 || got[0] != "key_previous" {
		t.Fatalf("revoked %v, want [key_previous]", got)
	}

	// And the secret that ends up on disk is the one the console issued.
	secret, err := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "oek_from_console" {
		t.Fatalf("stored %q, want the console's token", secret)
	}
}

func TestBrowserLoginRollsBackWhenItCannotBeStored(t *testing.T) {
	core := newFakeCore()
	defer core.Close()
	console := consoleIssuing("key_orphan")
	defer console.Close()

	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this case depends on")
	}
	a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})
	// Make storing the secret fail, which is the first thing that happens after
	// the key exists. A read-only config directory is the honest version of it:
	// a full disk, a locked-down home, a keychain outage with nowhere to fall
	// back to. Anything in this window strands the key unless it is rolled back.
	home := filepath.Dir(a.cfg.ConfigDir())
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	if err := runLoginWith(t, a, loginOpts{}); err == nil {
		t.Fatal("expected the login to fail when nothing can be stored")
	}
	spy.check(t)

	// The key the console minted must not be left alive with no local record of
	// it. Self-revocation is the only move available — the console kept nothing.
	if got := core.revokedKeys(); len(got) != 1 || got[0] != "key_orphan" {
		t.Fatalf("revoked %v, want the orphaned key to be rolled back", got)
	}
}

func TestAFailedRollbackNamesTheKeyItCouldNotRevoke(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this case depends on")
	}
	core := newFakeCore()
	// The rollback itself fails now — the case the old code swallowed whole,
	// warning only when the revocation SUCCEEDED.
	core.failRevocations(http.StatusInternalServerError)
	defer core.Close()
	console := consoleIssuing("key_orphan")
	defer console.Close()

	a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})
	out, warnings := capturingPrinter()
	a.out = out
	// Nothing can be stored (see the rollback test above), so the login fails
	// with a key already minted — and then the rollback fails too.
	home := filepath.Dir(a.cfg.ConfigDir())
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	if err := runLoginWith(t, a, loginOpts{}); err == nil {
		t.Fatal("expected the login to fail when nothing can be stored")
	}
	spy.check(t)

	// A live key, and this warning is the only trace of it that will ever
	// exist: after this process exits nothing on the machine knows it was made.
	got := warnings.String()
	if !strings.Contains(got, "key_orphan") || !strings.Contains(got, core.URL) {
		t.Fatalf("a failed rollback must name the key and where it lives, got:\n%s", got)
	}
}

func TestAMismatchedKeyThatSurvivedSaysSoInstead(t *testing.T) {
	core := newFakeCore()
	defer core.Close()
	// The deployment the console fronts, refusing the revocation.
	otherCore := newFakeCore()
	otherCore.failRevocations(http.StatusInternalServerError)
	defer otherCore.Close()

	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/config" {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "oek_stray", "key_id": "key_stray", "key_name": "cli",
			"api_url": otherCore.URL,
		})
	}))
	defer console.Close()

	a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})
	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	err := runLoginWith(t, a, loginOpts{})
	spy.check(t)
	if err == nil {
		t.Fatal("a key minted for another deployment must not be accepted")
	}
	msg := err.Error()
	// The whole point: the message must not claim a revocation that never
	// landed. A user told the key is dead never goes and revokes it.
	if strings.Contains(msg, "was revoked") {
		t.Fatalf("the message claims a revocation that failed:\n%s", msg)
	}
	if !strings.Contains(msg, "key_stray") || !strings.Contains(msg, otherCore.URL) {
		t.Fatalf("the message must name the key and where to revoke it:\n%s", msg)
	}
	// Tried once, and only once: the branch owns this revocation, so the
	// deferred rollback must not send a second DELETE and report its 404.
	if got := otherCore.revokedKeys(); len(got) != 1 || got[0] != "key_stray" {
		t.Fatalf("revocation attempts %v, want exactly one for key_stray", got)
	}
}

func TestAFailedReLoginKeepsTheCredentialThatWorked(t *testing.T) {
	core := newFakeCore()
	defer core.Close()
	console := consoleIssuing("key_from_console")
	defer console.Close()

	a := loginFixture(t, core.URL, config.Profile{
		Role:       "account",
		KeyID:      "key_previous",
		KeyStorage: secrets.File,
		ConsoleURL: console.URL,
	})
	if _, _, err := secrets.Save(a.cfg.ConfigDir(), "default", "oek_previous", true); err != nil {
		t.Fatal(err)
	}
	// Fail the CONFIG write and nothing else: a directory where the file goes
	// cannot be renamed over. The secret store is a different path under the
	// same config dir, so this leaves the login failing exactly in the window
	// where the new key is stored and the profile still points at the old one.
	if err := os.Mkdir(filepath.Join(a.cfg.ConfigDir(), "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	if err := runLoginWith(t, a, loginOpts{}); err == nil {
		t.Fatal("expected the login to fail when the config cannot be written")
	}
	spy.check(t)

	// The new key is rolled back on the server, so leaving it on disk would
	// point this profile at a revoked token while the credential that worked a
	// second ago is gone — every later command 401s with no way back but a
	// login the user does not know they need.
	secret, err := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File)
	if err != nil {
		t.Fatalf("the previous credential was destroyed: %v", err)
	}
	if secret != "oek_previous" {
		t.Fatalf("stored %q, want the credential the profile still points at", secret)
	}
	if got := core.revokedKeys(); len(got) != 1 || got[0] != "key_from_console" {
		t.Fatalf("revoked %v, want the new key rolled back", got)
	}
}

func TestPastedRestrictedKeyIsStoredRatherThanRefused(t *testing.T) {
	core := newFakeCore()
	core.mintStatus = http.StatusForbidden
	core.mintCode = "account_credentials_required"
	defer core.Close()

	a := loginFixture(t, core.URL, config.Profile{})
	a.flagAPIKey = "oek_restricted"

	if err := runLoginWith(t, a, loginOpts{}); err != nil {
		t.Fatalf("a restricted key must still log in: %v", err)
	}

	stored, _ := a.cfg.Profile("default")
	if stored.KeyID != "" {
		t.Fatalf("recorded key id %q for a key the CLI does not own", stored.KeyID)
	}
	if got := core.revokedKeys(); len(got) != 0 {
		t.Fatalf("revoked %v, but this login owns nothing to revoke", got)
	}
	secret, err := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "oek_restricted" {
		t.Fatalf("stored %q, want the key that was supplied", secret)
	}
}

func TestPastedAccountKeyStillMintsItsOwn(t *testing.T) {
	core := newFakeCore()
	defer core.Close()

	a := loginFixture(t, core.URL, config.Profile{})
	a.flagAPIKey = "oek_pasted"

	if err := runLoginWith(t, a, loginOpts{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if core.minted != 1 {
		t.Fatalf("minted %d keys, want exactly one per-device key", core.minted)
	}
	secret, err := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File)
	if err != nil {
		t.Fatal(err)
	}
	// The pasted key is discarded — what is kept is the key minted for this
	// machine, which is the whole point of minting one.
	if secret != "oek_minted" {
		t.Fatalf("stored %q, want the minted key", secret)
	}
}

func TestLoginRefusesAConsoleFrontingAnotherAPI(t *testing.T) {
	core := newFakeCore()
	defer core.Close()
	// A console that honestly says it fronts a DIFFERENT deployment.
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/config" {
			_ = json.NewEncoder(w).Encode(map[string]any{"apiUrl": "https://api.elsewhere.test"})
			return
		}
		t.Errorf("nothing past /api/config should be reached, got %s %s", r.Method, r.URL.Path)
	}))
	defer console.Close()

	a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})

	// The refusal must land BEFORE anything exists: no browser, no grant.
	opened := false
	original := openBrowserFn
	openBrowserFn = func(string) bool { opened = true; return true }
	t.Cleanup(func() { openBrowserFn = original })

	err := runLoginWith(t, a, loginOpts{})
	if err == nil || !strings.Contains(err.Error(), "--console-url") {
		t.Fatalf("want a refusal naming the flag, got %v", err)
	}
	if opened {
		t.Fatal("a mismatched deployment must be refused before a browser opens")
	}
	if got := core.revokedKeys(); len(got) != 0 {
		t.Fatalf("nothing was minted, so nothing should be revoked — got %v", got)
	}
}

func TestBrowserLoginRevokesAKeyMintedForAnotherAPI(t *testing.T) {
	// The API this login TARGETS — must never see the key.
	core := newFakeCore()
	defer core.Close()
	// The API the console actually fronts — the one place the revocation works.
	otherCore := newFakeCore()
	defer otherCore.Close()

	// A console whose pre-flight is mute (an older build), so the mismatch is
	// only discoverable from the redemption response — the belt, not the braces.
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/config" {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "oek_stray", "key_id": "key_stray", "key_name": "cli",
			"api_url": otherCore.URL,
		})
	}))
	defer console.Close()

	a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})
	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	err := runLoginWith(t, a, loginOpts{})
	spy.check(t)
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("want a mismatch error saying the key was revoked, got %v", err)
	}
	// The whole point: the revocation goes to the deployment the key BELONGS
	// to, not the one this login was aimed at.
	if got := otherCore.revokedKeys(); len(got) != 1 || got[0] != "key_stray" {
		t.Fatalf("advertised API saw revocations %v, want [key_stray]", got)
	}
	if got := core.revokedKeys(); len(got) != 0 {
		t.Fatalf("the targeted API saw revocations %v, want none", got)
	}
	if _, lerr := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File); lerr == nil {
		t.Fatal("a refused login must store nothing")
	}
}

func TestLoginRefusesToGuessAConsoleForACustomDeployment(t *testing.T) {
	core := newFakeCore()
	defer core.Close()

	a := loginFixture(t, core.URL, config.Profile{})
	err := runLoginWith(t, a, loginOpts{})
	if err == nil || !strings.Contains(err.Error(), "--console-url") {
		t.Fatalf("want a refusal naming the flag, got %v", err)
	}
	// Nothing was contacted and nothing was stored.
	if _, lerr := secrets.Load(a.cfg.ConfigDir(), "default", secrets.File); lerr == nil {
		t.Fatal("a refused login must store nothing")
	}
}

// deviceFlowConsole stands in for the console as the DEVICE flow reaches it: it
// answers the RFC 8628 endpoints and counts the grants it was asked for, which
// is how a case can tell which of the two flows a login actually ran.
type deviceFlowConsole struct {
	*httptest.Server
	grants atomic.Int32
}

func newDeviceFlowConsole(keyID string) *deviceFlowConsole {
	dc := &deviceFlowConsole{}
	dc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/config":
			_, _ = w.Write([]byte("{}"))
		case "/api/cli/device/code":
			dc.grants.Add(1)
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "https://console.test/cli",
				// A second, so the poll loop is not a stopwatch.
				"expires_in": 900, "interval": 1,
			})
		case "/api/cli/device/token":
			writeJSON(w, 200, map[string]any{
				"token": "oek_from_device", "key_id": keyID, "key_name": "openemail-cli @ laptop",
			})
		default:
			writeJSON(w, 404, map[string]any{"error": "not_found"})
		}
	}))
	return dc
}

// --device and --no-browser pick the flow; the machine gets a say only when
// neither was passed. Read the other way round, a desktop that asked for a
// device code is sent down the loopback path instead, where it waits out the
// full five-minute deadline for a redirect from a browser on another machine.
func TestDeviceFlagsBeatAnAvailableBrowser(t *testing.T) {
	cases := []struct {
		name string
		opts loginOpts
		// The URL the flow may hand to a browser; "" when it must open none.
		wantOpened string
	}{
		// The device flow still offers to open the verification page — a
		// convenience, since the code the human carries is what authorizes.
		{"--device", loginOpts{device: true}, "https://console.test/cli"},
		{"--no-browser", loginOpts{noBrowser: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A machine that plainly HAS a browser, so nothing but the flag can
			// be choosing the flow.
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_TTY", "")
			t.Setenv("DISPLAY", ":0")

			core := newFakeCore()
			defer core.Close()
			console := newDeviceFlowConsole("key_from_device")
			defer console.Close()

			a := loginFixture(t, core.URL, config.Profile{ConsoleURL: console.URL})

			// The loopback flow binds its callback port before it does anything
			// else, so refusing the bind is what turns "it took the other path"
			// into a failed login here instead of a five-minute wait in front of
			// a real user.
			originalListen := listenLoopbackFn
			listenLoopbackFn = func() (net.Listener, error) {
				return nil, errors.New("the loopback flow ran for a login that asked for a device code")
			}
			t.Cleanup(func() { listenLoopbackFn = originalListen })

			opened := ""
			originalBrowser := openBrowserFn
			openBrowserFn = func(raw string) bool { opened = raw; return true }
			t.Cleanup(func() { openBrowserFn = originalBrowser })

			if err := runLoginWith(t, a, tc.opts); err != nil {
				t.Fatalf("login: %v", err)
			}
			if console.grants.Load() != 1 {
				t.Fatalf("the console was asked for %d device grants, want exactly one", console.grants.Load())
			}
			if opened != tc.wantOpened {
				t.Fatalf("opened %q, want %q", opened, tc.wantOpened)
			}
			stored, _ := a.cfg.Profile("default")
			if stored.KeyID != "key_from_device" {
				t.Fatalf("profile records key %q, want the one the device flow issued", stored.KeyID)
			}
		})
	}
}

// The device flow is chosen without a browser, and the loopback flow with one.
func TestFlowSelection(t *testing.T) {
	t.Run("SSH forces the device flow", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
		if browserLikelyAvailable() {
			t.Fatal("a remote shell must not wait for a local browser redirect")
		}
	})
	t.Run("a desktop keeps the loopback flow", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_TTY", "")
		t.Setenv("DISPLAY", ":0")
		if !browserLikelyAvailable() {
			t.Fatal("a desktop session should use the browser redirect")
		}
	})
}
