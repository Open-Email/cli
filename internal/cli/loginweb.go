package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

// Browser login — the handshake that replaces pasting an account key.
//
// The CLI opens the console's consent page, a signed-in human approves, and a
// one-time code comes back over a channel this process opened itself. Two
// flows, because a terminal is not always beside a browser:
//
//   - LOOPBACK (RFC 8252 §7.3 + PKCE, RFC 7636): a listener on 127.0.0.1:0 and
//     a redirect. The default whenever a browser can plausibly be opened.
//   - DEVICE (RFC 8628): a short code the human types on any other machine.
//     For SSH, containers, and anything with no desktop.
//
// This is not OAuth and does not pretend to be: what comes back is the same
// `oek_…` account key core has always issued. The WIRE FORMAT is the spec's, so
// that a real authorization server can land behind these endpoints later
// without this file changing. See console/docs/cli-login-design.md.

// webLoginResult is what a completed browser flow yields. The key was minted by
// the console on the user's behalf, so the CLI must NOT mint another — but it
// owns this one exactly as if it had (see runLogin's rollback and revoke).
//
// APIURL is the deployment the console says this key authenticates against.
// It is what makes a console/API mismatch recoverable: the key can be revoked
// against the one API that would accept the revocation, instead of stranded.
// Empty when the console predates the field.
type webLoginResult struct {
	Token   string
	KeyID   string
	KeyName string
	APIURL  string
}

// mintedKey is the console's redemption answer, on both flows. Shared so the
// two paths cannot drift on what counts as an acceptable key.
type mintedKey struct {
	Token   string `json:"token"`
	KeyID   string `json:"key_id"`
	KeyName string `json:"key_name"`
	APIURL  string `json:"api_url"`
}

// result validates the answer before it can become a login. A missing key id is
// refused exactly as hard as a missing token: an unidentified key is one the
// rollback cannot roll back and `logout` cannot revoke, so accepting it trades
// a retry now for a live credential this machine can never retire. The name is
// stripped of control characters (sanitizeCell) because it is console-chosen
// text that ends up on a terminal — login's own result line prints it outside a
// table, where nothing else would.
func (m mintedKey) result() (*webLoginResult, error) {
	if m.Token == "" {
		return nil, errors.New("the console returned no key")
	}
	if m.KeyID == "" {
		return nil, errors.New("the console returned a key with no id — nothing here could revoke it later")
	}
	return &webLoginResult{
		Token:   m.Token,
		KeyID:   m.KeyID,
		KeyName: sanitizeCell(m.KeyName),
		APIURL:  m.APIURL,
	}, nil
}

// errAuthDenied is the human clicking Cancel. Distinct from every transport
// failure because it is the one outcome that must not suggest retrying.
var errAuthDenied = errors.New("authorization was declined in the browser")

// How long a human has to finish. The console expires a loopback grant in 5
// minutes and a device grant in 15; matching those here means the CLI gives up
// when its grant does, rather than waiting on something already dead.
const (
	loopbackDeadline = 5 * time.Minute
	deviceDeadline   = 15 * time.Minute
)

// deviceSlowDownStep is how much the poll interval grows on `slow_down`. A
// variable only so the test that covers that branch does not have to spend five
// real seconds inside it.
var deviceSlowDownStep = 5 * time.Second

// safeBrowserURI reports whether a URL the console supplied may be handed to
// the desktop's URL opener. The same rule resolveConsoleURL applies to the
// console itself — https anywhere, plain http only where it cannot leave the
// machine — because `openBrowser` runs whatever handler the OS has registered
// for the scheme, and neither `file:` nor `javascript:` is a sign-in page.
func safeBrowserURI(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	switch {
	case strings.EqualFold(u.Scheme, "https"):
		return true
	case strings.EqualFold(u.Scheme, "http"):
		return isLoopbackHost(u.Hostname())
	}
	return false
}

func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// pkcePair returns a verifier and its S256 challenge. The verifier never leaves
// this process until redemption, which is what makes an intercepted code
// useless — on a shared machine any local process can reach the loopback port.
func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomBase64URL(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// consoleClient is the HTTP client for the console origin. Short timeouts: every
// call here is a small JSON round trip a human is waiting on.
var consoleClient = &http.Client{Timeout: 30 * time.Second}

// postConsole sends one JSON request to the console and decodes the answer.
// A non-2xx is returned as consoleError so callers can branch on the RFC 8628
// error vocabulary rather than on status codes.
func postConsole(ctx context.Context, base, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openemail-cli/"+Version)
	res, err := consoleClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact %s: %w", base, err)
	}
	defer res.Body.Close()

	// Bounded: this endpoint answers a small JSON object, and an unbounded read
	// here would let a hostile or broken origin exhaust memory on a login.
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response from %s: %w", base, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return &consoleError{Status: res.StatusCode, Code: envelope.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type consoleError struct {
	Status int
	Code   string
}

func (e *consoleError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("console returned HTTP %d", e.Status)
}

func consoleErrCode(err error) string {
	var ce *consoleError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

/* ── loopback ────────────────────────────────────────────────────────────── */

// The page the browser lands on after the redirect. Deliberately inert: no
// script, no external asset, and nothing that could be framed usefully. It is
// the last thing the human sees of this flow, so it says which terminal to go
// back to and nothing else.
const callbackPage = `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<title>%s</title><style>body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:32rem;
padding:0 1rem;color:#12141A;background:#F7F8FA}h1{font-size:1.25rem}
@media(prefers-color-scheme:dark){body{color:#F7F8FA;background:#12141A}}</style></head>
<body><h1>%s</h1><p>%s</p></body></html>`

func writeCallbackPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// This page is a local dead end; nothing may embed it or cache it.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	fmt.Fprintf(w, callbackPage, title, title, message)
}

type callbackResult struct {
	code string
	err  error
}

// listenLoopbackFn binds the callback port. A seam, like openBrowserFn, because
// the address the listener actually gets is invisible everywhere else in the
// flow — the redirect URI is built from the port alone — and "only this machine
// can answer with a code" is the assumption the whole loopback flow rests on.
var listenLoopbackFn = func() (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	return l, nil
}

// loopbackLogin runs the redirect flow and returns a minted key.
//
// The listener is bound BEFORE the URL is built, because the URL has to name
// the port that was actually granted — binding :0 and taking what the OS gives
// is what keeps two tools (or two shells) from fighting over a fixed port.
func loopbackLogin(ctx context.Context, out *Printer, consoleURL, keyName string) (*webLoginResult, error) {
	listener, err := listenLoopbackFn()
	if err != nil {
		return nil, err
	}
	defer listener.Close()

	verifier, challenge, err := pkcePair()
	if err != nil {
		return nil, err
	}
	state, err := randomBase64URL(16)
	if err != nil {
		return nil, err
	}

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	query := url.Values{}
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("redirect_uri", redirectURI)
	query.Set("name", keyName)
	authURL := consoleURL + "/cli?" + query.Encode()

	results := make(chan callbackResult, 1)
	// Non-blocking: the flow ends on the FIRST answer, and a browser that
	// reloads the callback (or a probe that follows it) would otherwise park a
	// handler goroutine on a channel nobody reads again.
	finish := func(r callbackResult) {
		select {
		case results <- r:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// A mismatched state is somebody else's request arriving at our port.
		// Refused WITHOUT completing the flow: signalling here would let any
		// local process cancel a login it cannot otherwise touch.
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			writeCallbackPage(w, http.StatusBadRequest, "Not this login",
				"This response does not belong to the sign-in running in your terminal.")
			return
		}
		if errCode := q.Get("error"); errCode != "" {
			writeCallbackPage(w, http.StatusOK, "Not authorized",
				"No key was created. You can close this tab.")
			finish(callbackResult{err: errAuthDenied})
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, http.StatusBadRequest, "Something went wrong",
				"The console did not send an authorization code. Run the command again.")
			finish(callbackResult{err: errors.New("no authorization code in the callback")})
			return
		}
		writeCallbackPage(w, http.StatusOK, "You are signed in",
			"Return to your terminal — you can close this tab.")
		finish(callbackResult{code: code})
	})
	// Anything else the browser asks for (a favicon, a stray probe) is answered
	// and ignored; only /callback with a matching state finishes the flow.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	out.Msgf("Opening %s", out.Bold(consoleURL+"/cli"))
	if !openBrowserFn(authURL) {
		out.Msgf("Could not open a browser automatically.")
	}
	// Printed either way: an SSH session opens a browser on the WRONG machine,
	// and the only recovery is the human copying this line.
	out.Msgf("If it did not open, visit this URL to authorize:")
	out.Msgf("  %s", authURL)
	out.Msgf("Waiting for authorization…")

	waitCtx, cancel := context.WithTimeout(ctx, loopbackDeadline)
	defer cancel()

	var code string
	select {
	case result := <-results:
		if result.err != nil {
			return nil, result.err
		}
		code = result.code
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out waiting for authorization after %s", loopbackDeadline)
	}

	var minted mintedKey
	if err := postConsole(ctx, consoleURL, "/api/cli/token", map[string]string{
		"code":          code,
		"code_verifier": verifier,
		"redirect_uri":  redirectURI,
	}, &minted); err != nil {
		return nil, redemptionError(err)
	}
	return minted.result()
}

// redemptionError turns the spec's error vocabulary into something a person can
// act on. `invalid_grant` is the interesting one: it means the code was already
// spent, expired, or did not match the verifier — all of which mean "start over"
// and none of which mean "your credentials are wrong".
func redemptionError(err error) error {
	switch consoleErrCode(err) {
	case "access_denied":
		return errAuthDenied
	case "expired_token":
		return errors.New("the authorization expired before it was completed — run `openemail login` again")
	case "invalid_grant":
		return errors.New("that authorization is no longer valid — run `openemail login` again")
	case "mint_failed":
		return errors.New("the console could not create a key for that organization")
	case "slow_down":
		// Only the device loop can honor this by backing off. Everywhere else it
		// arrives as a bare `slow_down (HTTP 429)`, which names the spec's field
		// rather than telling the person what to do about it.
		return errTooManyAttempts
	}
	return err
}

// errTooManyAttempts is the console throttling sign-ins, said once so the two
// paths that report it cannot drift apart.
var errTooManyAttempts = errors.New("too many sign-in attempts from this network — wait a minute and try again")

/* ── device ──────────────────────────────────────────────────────────────── */

// deviceLogin runs RFC 8628's flow: show a short code, let the human approve it
// wherever they have a browser, and poll until they do.
//
// `openBrowser` gates the convenience launch of verification_uri_complete —
// false when the user said --no-browser, which must mean what it says even on
// the flow that does not depend on a browser being here.
func deviceLogin(ctx context.Context, out *Printer, consoleURL, keyName string, openBrowser bool) (*webLoginResult, error) {
	var grant struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := postConsole(ctx, consoleURL, "/api/cli/device/code",
		map[string]string{"name": keyName}, &grant); err != nil {
		if consoleErrCode(err) == "slow_down" {
			return nil, errTooManyAttempts
		}
		return nil, err
	}
	if grant.DeviceCode == "" || grant.UserCode == "" {
		return nil, errors.New("the console did not start a device authorization")
	}
	// The address the human is told to trust, checked before it is shown as much
	// as before it is opened: this string decides where somebody types a code
	// that mints a key, so a scheme this CLI would not authorize over is refused
	// rather than printed with a caveat.
	if !safeBrowserURI(grant.VerificationURI) {
		return nil, fmt.Errorf("the console gave an unusable verification address (%q)", sanitizeCell(grant.VerificationURI))
	}
	// Both go straight to a terminal that acts on escape sequences, and these
	// two lines are exactly the ones the human is asked to trust — the code they
	// type, the address they visit. Cheap depth: a console that has been taken
	// over has better moves than repainting a line, but not free ones.
	userCode := sanitizeCell(grant.UserCode)
	verificationURI := sanitizeCell(grant.VerificationURI)

	out.Msgf("First copy this one-time code:")
	out.Msgf("")
	out.Msgf("      %s", out.Bold(userCode))
	out.Msgf("")
	out.Msgf("Then open %s and enter it.", out.Bold(verificationURI))
	// The BARE verification URI, never a prefilled one — the human carries the
	// code across, which is what proves they are the one holding this terminal.
	// The console does not emit `verification_uri_complete` for that reason.
	// Opened as a convenience only: this flow exists because the browser may be
	// on another machine entirely, so nothing depends on the launch working.
	if openBrowser {
		openBrowserFn(grant.VerificationURI)
	}
	out.Msgf("Waiting for authorization…")

	interval := time.Duration(max(grant.Interval, 1)) * time.Second
	deadline := deviceDeadline
	if grant.ExpiresIn > 0 {
		deadline = time.Duration(grant.ExpiresIn) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("the code expired before it was approved — run `openemail login` again")
		case <-time.After(interval):
		}

		var minted mintedKey
		err := postConsole(waitCtx, consoleURL, "/api/cli/device/token",
			map[string]string{"device_code": grant.DeviceCode}, &minted)
		if err == nil {
			return minted.result()
		}
		switch consoleErrCode(err) {
		case "authorization_pending":
			continue
		case "slow_down":
			// The spec's contract: back off and keep waiting, never give up.
			interval += deviceSlowDownStep
			continue
		default:
			return nil, redemptionError(err)
		}
	}
}

/* ── deployment identity ─────────────────────────────────────────────────── */

// consoleAdvertisedAPIURL asks the console which core API it fronts, BEFORE any
// grant is started or browser opened. GET /api/config is public and answers
// `apiUrl`; a mismatch with --api-url refuses the login while nothing has been
// minted, which is strictly better than the belt (revoking after the fact).
//
// Tolerant of everything except a positive answer: an older console without the
// field, a transient failure, or junk all return "" and the flow proceeds — the
// redemption response carries the same fact, and refusing a login because a
// pre-flight convenience call hiccuped would be the tail wagging the dog.
func consoleAdvertisedAPIURL(ctx context.Context, base string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/config", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openemail-cli/"+Version)
	res, err := consoleClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return ""
	}
	var cfg struct {
		APIURL string `json:"apiUrl"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.APIURL)
}

// sameAPIOrigin reports whether two API URLs name the same deployment —
// scheme and host:port, case-insensitively, trailing slashes ignored. Origins
// rather than full URLs, because the comparison exists to REFUSE things: a
// path difference on one origin is one deployment written two ways, and a
// false refusal here blocks a legitimate login.
func sameAPIOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimRight(strings.TrimSpace(a), "/"))
	ub, errB := url.Parse(strings.TrimRight(strings.TrimSpace(b), "/"))
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && originHost(ua) == originHost(ub)
}

// originHost is a URL's authority with a redundant port removed, lowercased.
// `https://api.open.email:443` and `https://api.open.email` are one deployment
// written two ways, and reading them as two is not a cosmetic bug here: it
// refuses a correct login up front, and past the mint it REVOKES a key that was
// never wrong. Hostname() on both sides rather than Host, so an IPv6 literal is
// compared to itself with or without its brackets.
func originHost(u *url.URL) string {
	port := u.Port()
	if (strings.EqualFold(u.Scheme, "https") && port == "443") ||
		(strings.EqualFold(u.Scheme, "http") && port == "80") {
		port = ""
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}

/* ── choosing a flow ─────────────────────────────────────────────────────── */

// browserLikelyAvailable reports whether opening a browser here could plausibly
// put it in front of the person running the command.
//
// The SSH check is the one that matters: on a remote host `xdg-open` may well
// succeed and open a browser on a machine nobody is looking at, so a flow that
// waits for a local redirect would hang until it timed out. Better to show a
// code the human can carry to whatever browser they actually have.
func browserLikelyAvailable() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	// A Unix desktop announces itself; macOS and Windows always have a handler.
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}
