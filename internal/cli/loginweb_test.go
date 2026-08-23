package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Open-Email/cli/internal/config"
)

// quietPrinter renders the flow's progress lines into nothing — these tests are
// about what travels over the wire, and a test suite that prints a login banner
// per case is unreadable.
func quietPrinter() *Printer {
	return &Printer{out: io.Discard, err: io.Discard}
}

// browserSpy stands in for the human: it receives the authorization URL the CLI
// would open, and answers it however the case wants.
type browserSpy struct {
	visited chan *url.URL
	seen    atomic.Value // *url.URL — what the CLI asked the browser to open
	// Failures are RECORDED rather than fatal: this half runs on its own
	// goroutine, and t.Fatal there stops the wrong stack (go vet says so) —
	// leaving the login to hang for its full timeout instead of failing.
	failure atomic.Value // string
}

func newBrowserSpy() *browserSpy { return &browserSpy{visited: make(chan *url.URL, 1)} }

// respond does what the console does after a click: redirect the browser to the
// CLI's loopback listener with the code (or an error) plus the state it sent.
// Runs on its own goroutine, so it reports through the spy, never through t.
func (b *browserSpy) respond(params map[string]string) {
	select {
	case authURL := <-b.visited:
		b.seen.Store(authURL)
		if err := deliverCallback(authURL, params); err != nil {
			b.failure.Store(err.Error())
		}
	case <-time.After(5 * time.Second):
		b.failure.Store("the CLI never opened a browser")
	}
}

// deliverCallback plays one browser hit at the CLI's loopback listener: take the
// URL the CLI opened, and answer its redirect_uri with these parameters. A value
// of "__state__" is replaced by the state the CLI actually sent. Separate from
// respond because a login can be hit MORE than once, and what happens then is
// the whole question in the foreign-state case.
func deliverCallback(authURL *url.URL, params map[string]string) error {
	redirect, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		return fmt.Errorf("unparsable redirect_uri: %w", err)
	}
	q := redirect.Query()
	for k, v := range params {
		if v == "__state__" {
			v = authURL.Query().Get("state")
		}
		q.Set(k, v)
	}
	redirect.RawQuery = q.Encode()
	res, err := http.Get(redirect.String())
	if err != nil {
		return fmt.Errorf("callback: %w", err)
	}
	res.Body.Close()
	return nil
}

// check surfaces anything the browser half recorded, on the test's goroutine.
func (b *browserSpy) check(t *testing.T) {
	t.Helper()
	if failure, ok := b.failure.Load().(string); ok && failure != "" {
		t.Fatalf("browser stand-in: %s", failure)
	}
}

// stubConsole is the console's redemption endpoint, recording what it was sent.
type stubConsole struct {
	*httptest.Server
	lastBody atomic.Value // map[string]any
	polls    atomic.Int32
}

func newStubConsole(handler func(path string, body map[string]any, w http.ResponseWriter)) *stubConsole {
	sc := &stubConsole{}
	sc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sc.lastBody.Store(body)
		if r.URL.Path == "/api/cli/device/token" {
			sc.polls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(r.URL.Path, body, w)
	}))
	return sc
}

func (s *stubConsole) body() map[string]any {
	v, _ := s.lastBody.Load().(map[string]any)
	return v
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// withBrowserSpy points openBrowser at the spy for one test.
func withBrowserSpy(t *testing.T, spy *browserSpy) {
	t.Helper()
	original := openBrowserFn
	openBrowserFn = func(raw string) bool {
		parsed, err := url.Parse(raw)
		if err != nil {
			return false
		}
		spy.visited <- parsed
		return true
	}
	t.Cleanup(func() { openBrowserFn = original })
}

func TestPKCEPairMatchesTheSpec(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Fatalf("challenge %q is not S256 of the verifier (want %q)", challenge, want)
	}
	// 43 unpadded characters is what a base64url'd SHA-256 is, and what the
	// console validates the shape against.
	if len(challenge) != 43 {
		t.Fatalf("challenge is %d characters, want 43", len(challenge))
	}
	// The verifier's LENGTH is the proof's strength: it is the one secret that
	// makes an intercepted code useless, and a short one is guessable while
	// every other assertion in this test still passes. RFC 7636 allows 43–128
	// characters; 32 random bytes is 43 of them.
	if len(verifier) != 43 {
		t.Fatalf("verifier is %d characters, want 43 (RFC 7636 allows 43–128)", len(verifier))
	}
	if verifier == challenge {
		t.Fatal("the verifier must not be the challenge")
	}
}

func TestLoopbackLoginRedeemsTheCode(t *testing.T) {
	console := newStubConsole(func(path string, _ map[string]any, w http.ResponseWriter) {
		writeJSON(w, 200, map[string]any{"token": "oek_new", "key_id": "key_9", "key_name": "cli @ host"})
	})
	defer console.Close()

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_thecode", "state": "__state__"})

	result, err := loopbackLogin(context.Background(), quietPrinter(), console.URL, "cli @ host")
	spy.check(t)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if result.Token != "oek_new" || result.KeyID != "key_9" {
		t.Fatalf("unexpected result %+v", result)
	}

	sent := console.body()
	if sent["code"] != "oecc_thecode" {
		t.Fatalf("redeemed %v, want the code from the callback", sent["code"])
	}
	// The verifier must reach the console, and it must be the one the challenge
	// in the browser URL was derived from — that pairing IS the PKCE proof, and
	// asserting it here is the only place both halves are visible at once.
	verifier, _ := sent["code_verifier"].(string)
	if verifier == "" {
		t.Fatal("no code_verifier was sent")
	}
	authURL, _ := spy.seen.Load().(*url.URL)
	if authURL == nil {
		t.Fatal("the browser was never sent anywhere")
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); authURL.Query().Get("code_challenge") != want {
		t.Fatal("the verifier redeemed does not match the challenge the browser was given")
	}
	if authURL.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("the challenge method must be declared as S256")
	}
	// State is what keeps another process's callback out of this login, so it
	// has to be unguessable rather than merely present: 16 random bytes is 22
	// base64url characters.
	if state := authURL.Query().Get("state"); len(state) < 22 {
		t.Fatalf("state %q is %d characters — too few to be unguessable", state, len(state))
	}
	// The redirect_uri is re-presented so the console can compare it with what
	// it approved, and it must name loopback and nothing else.
	redirect, _ := sent["redirect_uri"].(string)
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatalf("redirect_uri %q is not a loopback callback", redirect)
	}
}

func TestLoopbackLoginRejectsAForeignState(t *testing.T) {
	// Refusing a foreign callback has two halves, and only the first is obvious:
	// its code must not be redeemed, AND its arrival must not end the login. Any
	// local process can reach the loopback port; if a stranger's callback failed
	// the flow, that is a one-request cancel of a sign-in it cannot otherwise
	// touch, which is a nuisance at best and a downgrade attack at worst.
	t.Run("it cannot cancel a login that is still pending", func(t *testing.T) {
		var redemptions atomic.Int32
		var redeemed atomic.Value // string — the code the console was asked to spend
		console := newStubConsole(func(_ string, body map[string]any, w http.ResponseWriter) {
			redemptions.Add(1)
			code, _ := body["code"].(string)
			redeemed.Store(code)
			writeJSON(w, 200, map[string]any{"token": "oek_new", "key_id": "key_9"})
		})
		defer console.Close()

		spy := newBrowserSpy()
		withBrowserSpy(t, spy)
		go func() {
			authURL := <-spy.visited
			spy.seen.Store(authURL)
			// The stranger's callback arrives first, and is fully answered before
			// the next line runs — http.Get returns only once the handler has.
			if err := deliverCallback(authURL, map[string]string{"code": "oecc_planted", "state": "not-our-state"}); err != nil {
				spy.failure.Store(err.Error())
				return
			}
			// Then the human finishes the sign-in they actually started.
			if err := deliverCallback(authURL, map[string]string{"code": "oecc_real", "state": "__state__"}); err != nil {
				spy.failure.Store(err.Error())
			}
		}()

		// Bounded so a flow that stopped listening fails here rather than hanging
		// until the package timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := loopbackLogin(ctx, quietPrinter(), console.URL, "cli")
		spy.check(t)
		if err != nil {
			t.Fatalf("a stranger's callback ended a login that was still pending: %v", err)
		}
		if result.KeyID != "key_9" {
			t.Fatalf("unexpected result %+v", result)
		}
		if got, _ := redeemed.Load().(string); got != "oecc_real" {
			t.Fatalf("redeemed %q, want the code from the callback that matched", got)
		}
		if n := redemptions.Load(); n != 1 {
			t.Fatalf("%d redemptions, want exactly the one", n)
		}
	})

	t.Run("and on its own it is not a login", func(t *testing.T) {
		redeemed := false
		console := newStubConsole(func(_ string, _ map[string]any, w http.ResponseWriter) {
			redeemed = true
			writeJSON(w, 200, map[string]any{"token": "oek_new", "key_id": "key_9"})
		})
		defer console.Close()

		spy := newBrowserSpy()
		withBrowserSpy(t, spy)
		go spy.respond(map[string]string{"code": "oecc_planted", "state": "not-our-state"})

		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		defer cancel()
		_, err := loopbackLogin(ctx, quietPrinter(), console.URL, "cli")
		if err == nil {
			t.Fatal("a foreign state must not complete the login")
		}
		if redeemed {
			t.Fatal("a code from a mismatched state was redeemed")
		}
	})
}

func TestLoopbackLoginListensOnlyOnLoopback(t *testing.T) {
	// That port accepts an authorization code, and PKCE is the only thing
	// between a code and a minted key. Bound on 0.0.0.0 it is offered to the
	// whole network — and nothing else in the flow would look any different,
	// because the redirect URI is spelled 127.0.0.1 whatever the bind was.
	var bound atomic.Value // net.Addr
	original := listenLoopbackFn
	listenLoopbackFn = func() (net.Listener, error) {
		l, err := original()
		if err == nil {
			bound.Store(l.Addr())
		}
		return l, err
	}
	t.Cleanup(func() { listenLoopbackFn = original })

	console := newStubConsole(func(_ string, _ map[string]any, w http.ResponseWriter) {
		writeJSON(w, 200, map[string]any{"token": "oek_new", "key_id": "key_9"})
	})
	defer console.Close()
	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

	if _, err := loopbackLogin(context.Background(), quietPrinter(), console.URL, "cli"); err != nil {
		t.Fatalf("login: %v", err)
	}
	spy.check(t)

	addr, _ := bound.Load().(*net.TCPAddr)
	if addr == nil {
		t.Fatal("the flow never bound a listener")
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("the callback listener bound %s — anything on the network could answer it with a code", addr)
	}
}

func TestRedemptionWithoutAKeyIDIsRefused(t *testing.T) {
	// A token with no key id disarms everything that could ever retire it: the
	// rollback a failed login runs, and the revocation `logout` does. Storing one
	// is how a machine ends up holding a key it can never hand back.
	t.Run("loopback", func(t *testing.T) {
		console := newStubConsole(func(_ string, _ map[string]any, w http.ResponseWriter) {
			writeJSON(w, 200, map[string]any{"token": "oek_new", "key_name": "cli"})
		})
		defer console.Close()

		spy := newBrowserSpy()
		withBrowserSpy(t, spy)
		go spy.respond(map[string]string{"code": "oecc_x", "state": "__state__"})

		result, err := loopbackLogin(context.Background(), quietPrinter(), console.URL, "cli")
		spy.check(t)
		if err == nil {
			t.Fatalf("an unidentified key was accepted: %+v", result)
		}
	})

	t.Run("device", func(t *testing.T) {
		console := newStubConsole(nil)
		console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/cli/device/code":
				writeJSON(w, 200, map[string]any{
					"device_code": "oecd_dev", "user_code": "WXYZ-1234",
					"verification_uri": "https://console.test/cli", "expires_in": 900, "interval": 1,
				})
			case "/api/cli/device/token":
				writeJSON(w, 200, map[string]any{"token": "oek_device", "key_name": "cli"})
			}
		})
		defer console.Close()
		withBrowserSpy(t, &browserSpy{visited: make(chan *url.URL, 4)})

		result, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli", true)
		if err == nil {
			t.Fatalf("an unidentified key was accepted: %+v", result)
		}
	})
}

func TestLoopbackLoginReportsDenial(t *testing.T) {
	console := newStubConsole(func(_ string, _ map[string]any, w http.ResponseWriter) {
		t.Error("a denied authorization must not be redeemed")
		writeJSON(w, 200, map[string]any{})
	})
	defer console.Close()

	spy := newBrowserSpy()
	withBrowserSpy(t, spy)
	go spy.respond(map[string]string{"error": "access_denied", "state": "__state__"})

	_, err := loopbackLogin(context.Background(), quietPrinter(), console.URL, "cli")
	spy.check(t)
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("want a denial error, got %v", err)
	}
}

func TestLoopbackLoginTranslatesRedemptionFailures(t *testing.T) {
	cases := []struct {
		code   string
		status int
		want   string
	}{
		{"expired_token", 400, "expired"},
		{"invalid_grant", 400, "no longer valid"},
		{"mint_failed", 400, "could not create a key"},
		// Only the device loop can act on `slow_down` by backing off. On this
		// path it used to surface as the spec's field name and an HTTP code.
		{"slow_down", 429, "too many sign-in attempts"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			console := newStubConsole(func(_ string, _ map[string]any, w http.ResponseWriter) {
				writeJSON(w, tc.status, map[string]any{"error": tc.code})
			})
			defer console.Close()

			spy := newBrowserSpy()
			withBrowserSpy(t, spy)
			go spy.respond(map[string]string{"code": "c", "state": "__state__"})

			_, err := loopbackLogin(context.Background(), quietPrinter(), console.URL, "cli")
			spy.check(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDeviceLoginPollsUntilApproved(t *testing.T) {
	console := newStubConsole(func(path string, _ map[string]any, w http.ResponseWriter) {
		switch path {
		case "/api/cli/device/code":
			writeJSON(w, 200, map[string]any{
				"device_code":      "oecd_dev",
				"user_code":        "WXYZ-1234",
				"verification_uri": "https://console.test/cli",
				"expires_in":       900,
				// One second, so the test is not a stopwatch.
				"interval": 1,
			})
		case "/api/cli/device/token":
			writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
		}
	})
	// The third poll succeeds. Rebuilt rather than mutated so the handler stays
	// a pure function of the request.
	approved := make(chan struct{})
	console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/device/code":
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "https://console.test/cli", "expires_in": 900, "interval": 1,
			})
		case "/api/cli/device/token":
			if console.polls.Add(1) < 3 {
				writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
				return
			}
			close(approved)
			writeJSON(w, 200, map[string]any{"token": "oek_device", "key_id": "key_d"})
		}
	})
	defer console.Close()

	// The device flow opens a browser only as a convenience; it must not depend
	// on one, so this test provides a spy that swallows the call.
	spy := newBrowserSpy()
	spy.visited = make(chan *url.URL, 4)
	withBrowserSpy(t, spy)

	result, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli @ server", true)
	if err != nil {
		t.Fatalf("device login: %v", err)
	}
	if result.Token != "oek_device" {
		t.Fatalf("unexpected token %q", result.Token)
	}
	select {
	case <-approved:
	default:
		t.Fatal("the flow returned without the approving poll")
	}
	if console.polls.Load() < 3 {
		t.Fatalf("expected to poll until approved, polled %d times", console.polls.Load())
	}
}

func TestDeviceLoginWithNoBrowserOpensNothing(t *testing.T) {
	console := newStubConsole(nil)
	console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/device/code":
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "https://console.test/cli",
				"expires_in":       900, "interval": 1,
			})
		case "/api/cli/device/token":
			writeJSON(w, 200, map[string]any{"token": "oek_device", "key_id": "key_d"})
		}
	})
	defer console.Close()

	// --no-browser is a promise, and the device flow's convenience launch of
	// the verification URI is exactly where it was being broken.
	opened := false
	original := openBrowserFn
	openBrowserFn = func(string) bool { opened = true; return true }
	t.Cleanup(func() { openBrowserFn = original })

	if _, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli", false); err != nil {
		t.Fatalf("device login: %v", err)
	}
	if opened {
		t.Fatal("--no-browser must not launch a browser, convenience or not")
	}
}

func TestDeviceLoginStopsOnDenial(t *testing.T) {
	console := newStubConsole(nil)
	console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/device/code":
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "https://console.test/cli", "expires_in": 900, "interval": 1,
			})
		case "/api/cli/device/token":
			writeJSON(w, 400, map[string]any{"error": "access_denied"})
		}
	})
	defer console.Close()
	withBrowserSpy(t, &browserSpy{visited: make(chan *url.URL, 4)})

	_, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli", true)
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("want a denial error, got %v", err)
	}
}

func TestDeviceLoginBacksOffOnSlowDown(t *testing.T) {
	// `slow_down` is the console asking for room, not refusing: the loop has to
	// keep waiting AND wait longer. Giving up abandons a login that is still
	// alive; not backing off keeps hammering the thing that just said it was
	// being hammered.
	const step = time.Second
	originalStep := deviceSlowDownStep
	deviceSlowDownStep = step
	t.Cleanup(func() { deviceSlowDownStep = originalStep })

	var mu sync.Mutex
	var polls []time.Time
	console := newStubConsole(nil)
	console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/device/code":
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "https://console.test/cli", "expires_in": 900, "interval": 1,
			})
		case "/api/cli/device/token":
			mu.Lock()
			polls = append(polls, time.Now())
			first := len(polls) == 1
			mu.Unlock()
			if first {
				writeJSON(w, 429, map[string]any{"error": "slow_down"})
				return
			}
			writeJSON(w, 200, map[string]any{"token": "oek_device", "key_id": "key_d"})
		}
	})
	defer console.Close()
	withBrowserSpy(t, &browserSpy{visited: make(chan *url.URL, 4)})

	result, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli", true)
	if err != nil {
		t.Fatalf("slow_down ended the login instead of slowing it: %v", err)
	}
	if result.Token != "oek_device" {
		t.Fatalf("unexpected token %q", result.Token)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(polls) < 2 {
		t.Fatalf("polled %d times, want a retry after the back-off", len(polls))
	}
	// The grant asked for one second, so a retry at about one second is the loop
	// ignoring the back-off. Only ever late under load, never early — which is
	// the direction that keeps this assertion honest.
	if gap := polls[1].Sub(polls[0]); gap < time.Second+step/2 {
		t.Fatalf("retried after %s, want the interval plus the %s back-off", gap, step)
	}
}

func TestDeviceLoginRefusesAnUnusableVerificationAddress(t *testing.T) {
	// This string is printed as the place to go and handed to whatever handler
	// the desktop has registered for its scheme. `javascript:` and `file:` are
	// not sign-in pages, and the console is not the only thing that can end up
	// answering that endpoint.
	console := newStubConsole(nil)
	console.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/cli/device/code" {
			writeJSON(w, 200, map[string]any{
				"device_code": "oecd_dev", "user_code": "WXYZ-1234",
				"verification_uri": "javascript:alert(1)", "expires_in": 900, "interval": 1,
			})
			return
		}
		t.Errorf("polled for a token on a grant that should never have been shown: %s", r.URL.Path)
	})
	defer console.Close()

	opened := ""
	original := openBrowserFn
	openBrowserFn = func(raw string) bool { opened = raw; return true }
	t.Cleanup(func() { openBrowserFn = original })

	if _, err := deviceLogin(context.Background(), quietPrinter(), console.URL, "cli", true); err == nil {
		t.Fatal("a non-http(s) verification address was accepted")
	}
	if opened != "" {
		t.Fatalf("the URL opener was handed %q", opened)
	}
}

func TestConsoleSuppliedKeyNameIsStripped(t *testing.T) {
	// The console chooses this name, and `login` prints it on a line of its own
	// rather than through the table renderer that sanitizes everything else.
	got, err := mintedKey{Token: "oek_new", KeyID: "key_9", KeyName: "cli \x1b[2K@ host"}.result()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got.KeyName, 0x1b) {
		t.Fatalf("an escape sequence survived into the stored key name: %q", got.KeyName)
	}
}

func TestSameAPIOrigin(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// The spellings one deployment legitimately comes in.
		{"https://api.open.email", "https://api.open.email/", true},
		{"https://API.Open.Email", "https://api.open.email", true},
		{"http://localhost:8787", "http://localhost:8787/", true},
		// A default port written out is the same deployment. Reading it as a
		// different one refuses a correct login — and, past the mint, revokes a
		// perfectly good key.
		{"https://api.open.email:443", "https://api.open.email", true},
		{"http://localhost:80/", "http://localhost", true},
		{"https://api.open.email:443", "https://api.open.email:443/", true},
		// Different deployments, however slightly.
		{"https://api.open.email", "https://api.staging.open.email", false},
		{"http://127.0.0.1:8787", "http://127.0.0.1:9999", false},
		{"https://api.open.email:8443", "https://api.open.email", false},
		// 443 is not http's default, so this pair really is two deployments.
		{"http://api.open.email:443", "http://api.open.email", false},
		{"https://api.open.email", "http://api.open.email", false},
		// Garbage compares equal to nothing — a refusal comparison must never
		// answer "same" out of two parse failures.
		{"", "", false},
		{"not a url", "not a url", false},
	}
	for _, tc := range cases {
		if got := sameAPIOrigin(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAPIOrigin(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestResolveConsoleURL(t *testing.T) {
	newApp := func(apiURL string, prof config.Profile) *app {
		return &app{apiURL: apiURL, profile: prof, out: quietPrinter()}
	}

	t.Run("production API defaults to the production console", func(t *testing.T) {
		got, err := newApp(config.DefaultAPIURL, config.Profile{}).resolveConsoleURL()
		if err != nil || got != config.DefaultConsoleURL {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("a custom API with no console URL is an error, not a guess", func(t *testing.T) {
		_, err := newApp("https://api.staging.test", config.Profile{}).resolveConsoleURL()
		if err == nil {
			t.Fatal("a custom deployment must not authorize against the production console")
		}
		if !strings.Contains(err.Error(), "--console-url") {
			t.Fatalf("the error must name the flag that fixes it: %v", err)
		}
	})

	t.Run("the profile's own console URL is used, trailing slash and all", func(t *testing.T) {
		a := newApp("https://api.staging.test", config.Profile{ConsoleURL: "https://app.staging.test/"})
		got, err := a.resolveConsoleURL()
		if err != nil || got != "https://app.staging.test" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("the flag outranks the profile", func(t *testing.T) {
		a := newApp("https://api.staging.test", config.Profile{ConsoleURL: "https://app.staging.test"})
		a.flagConsoleURL = "https://app.other.test"
		got, err := a.resolveConsoleURL()
		if err != nil || got != "https://app.other.test" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("plain HTTP is refused unless it cannot leave the machine", func(t *testing.T) {
		a := newApp("https://api.staging.test", config.Profile{ConsoleURL: "http://app.staging.test"})
		if _, err := a.resolveConsoleURL(); err == nil {
			t.Fatal("a minted key must not travel over plain HTTP")
		}
		local := newApp("https://api.staging.test", config.Profile{ConsoleURL: "http://localhost:8788"})
		if got, err := local.resolveConsoleURL(); err != nil || got != "http://localhost:8788" {
			t.Fatalf("local dev must still work: %q, %v", got, err)
		}
		// Host names are case-insensitive to every resolver in the path; this
		// one being the exception would refuse a console that works.
		shouty := newApp("https://api.staging.test", config.Profile{ConsoleURL: "http://LOCALHOST:8788"})
		if got, err := shouty.resolveConsoleURL(); err != nil || got != "http://LOCALHOST:8788" {
			t.Fatalf("case must not decide whether a host is loopback: %q, %v", got, err)
		}
	})

	t.Run("a trailing slash is not a custom deployment", func(t *testing.T) {
		// Resolved through preRun, because that is where the API URL gets its one
		// spelling — and where `--api-url https://api.open.email/` used to become
		// a deployment with no console, refusing a production login over a slash.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("OPENEMAIL_API_URL", "")
		t.Setenv("OPENEMAIL_CONSOLE_URL", "")
		t.Setenv("OPENEMAIL_PROFILE", "")
		a := &app{flagAPIURL: config.DefaultAPIURL + "/"}
		if err := a.preRun(); err != nil {
			t.Fatal(err)
		}
		got, err := a.resolveConsoleURL()
		if err != nil || got != config.DefaultConsoleURL {
			t.Fatalf("got %q, %v — want the production console", got, err)
		}
	})
}
