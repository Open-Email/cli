package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

// Version is the CLI version (overridden at build time via -ldflags).
var Version = "0.1.0-dev"

// Environment variable names (flag > env > profile precedence).
const (
	envAPIKey     = "OPENEMAIL_API_KEY"
	envAPIURL     = "OPENEMAIL_API_URL"
	envConsoleURL = "OPENEMAIL_CONSOLE_URL"
	envProfile    = "OPENEMAIL_PROFILE"
	envNoKeyring  = "OPENEMAIL_NO_KEYRING"
)

// ExitError carries a specific process exit code. 0 ok, 1 error, 2 usage,
// 4 auth required (gh convention).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func authRequired(msg string) *ExitError {
	return &ExitError{Code: 4, Err: errors.New(msg)}
}

func usageError(err error) *ExitError { return &ExitError{Code: 2, Err: err} }

// errSilent marks an ExitError whose exit code matters but whose message has
// already been rendered (e.g. a `sieve check` report) — printError skips it.
var errSilent = errors.New("")

func silentExit(code int) *ExitError { return &ExitError{Code: code, Err: errSilent} }

// app is the resolved runtime for one invocation.
type app struct {
	cfg    *config.File
	out    *Printer
	stdout io.Writer // raw stdout sink (JSON lines from `watch`); defaults to os.Stdout

	// persistent flags
	flagProfile    string
	flagAPIURL     string
	flagConsoleURL string
	flagAPIKey     string
	flagJSON       bool
	flagNoColor    bool
	flagDebug      bool
	flagNoKeyring  bool
	flagMailbox    string // -m/--mailbox on mailbox-scoped groups

	// resolved after preRun
	profileName string
	profile     config.Profile
	apiURL      string
	token       string
	tokenSource string // "flag" | "env" | "profile" | ""

	mailboxCache map[string]string // resolved mailbox ids by input, per invocation
	adminCmd     *cobra.Command    // hidden from help unless the profile is a system key
}

// preRun resolves config, the active profile, api URL, and token per precedence.
// It never fails for missing auth — login must run unauthenticated; commands that
// need a token call authedClient().
func (a *app) preRun() error {
	a.out = newPrinter(a.flagJSON, a.flagNoColor)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	a.cfg = cfg

	a.profileName = cfg.ResolveProfileName(firstNonEmpty(a.flagProfile, os.Getenv(envProfile)))
	a.profile, _ = cfg.Profile(a.profileName)

	// API URL: flag > env > profile > default. Normalized here, once, because
	// everything downstream compares it as a STRING — against the stored
	// profile's URL, and against DefaultAPIURL to decide whether this is our
	// production deployment. `--api-url https://api.open.email/` is the same
	// deployment as the default, and a trailing slash used to make it a custom
	// one that `login` then refused to find a console for.
	a.apiURL = strings.TrimRight(firstNonEmpty(a.flagAPIURL, os.Getenv(envAPIURL), a.profile.APIURL, config.DefaultAPIURL), "/")

	// Token: flag > env > stored profile secret.
	switch {
	case a.flagAPIKey != "":
		a.token, a.tokenSource = a.flagAPIKey, "flag"
	case os.Getenv(envAPIKey) != "":
		a.token, a.tokenSource = os.Getenv(envAPIKey), "env"
	case a.profile.KeyStorage != "":
		tok, lerr := secrets.Load(cfg.ConfigDir(), a.profileName, a.profile.KeyStorage)
		if lerr != nil {
			if !errors.Is(lerr, secrets.ErrNotFound) {
				a.out.Warnf("could not read stored key for profile %q: %v", a.profileName, lerr)
			}
		} else {
			a.token, a.tokenSource = tok, "profile"
		}
	}

	return nil
}

// eagerProfileRole resolves the active profile's role WITHOUT full preRun, for
// deciding admin-group help visibility. Cobra does not run PersistentPreRunE when
// --help is requested, so the visibility must be decided at command-tree
// construction. Best-effort: honors OPENEMAIL_PROFILE + the config default (not a
// late --profile flag), which covers the logged-in-system-key case; the admin
// commands still function regardless of visibility (core enforces the real gate).
func eagerProfileRole() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	name := cfg.ResolveProfileName(os.Getenv(envProfile))
	p, _ := cfg.Profile(name)
	return p.Role
}

// client builds an unauthenticated-capable client (used by login and /health).
func (a *app) client() (*coreapi.Client, error) {
	return coreapi.New(coreapi.Config{
		BaseURL:   a.apiURL,
		Token:     a.token,
		UserAgent: "openemail-cli/" + Version,
	})
}

// clientWithToken builds a client bound to a specific token (login uses this
// before the token is persisted).
func (a *app) clientWithToken(token string) (*coreapi.Client, error) {
	return coreapi.New(coreapi.Config{
		BaseURL:   a.apiURL,
		Token:     token,
		UserAgent: "openemail-cli/" + Version,
	})
}

// authedClient requires a resolved token, else an auth-required exit error.
func (a *app) authedClient() (*coreapi.Client, error) {
	if a.token == "" {
		return nil, authRequired(fmt.Sprintf("not authenticated for profile %q — run `openemail login`", a.profileName))
	}
	// A STORED key belongs to the deployment it was minted against. --api-url and
	// OPENEMAIL_API_URL redirect the request but do not change which secret is
	// loaded, so pointing a logged-in profile at a staging host (or any host)
	// used to send the production bearer there — a credential handed to whoever
	// runs the other end, from a flag that reads like it only changes an address.
	// The default profile points at PROD, which makes this the easy mistake.
	//
	// A key supplied explicitly for this invocation (--api-key / OPENEMAIL_API_KEY)
	// is the caller saying which credential goes with which host, so it is never
	// blocked.
	if a.tokenSource == "profile" && a.profile.APIURL != "" && a.apiURL != a.profile.APIURL {
		return nil, authRequired(fmt.Sprintf(
			"profile %q holds a key for %s, but this call targets %s — a stored key is not sent to another deployment.\n"+
				"Pass --api-key (or OPENEMAIL_API_KEY) with a key for %s, or `openemail login` on a separate --profile.",
			a.profileName, a.profile.APIURL, a.apiURL, a.apiURL))
	}
	if a.tokenSource == "profile" && strings.HasPrefix(a.apiURL, "http://") &&
		!strings.HasPrefix(a.apiURL, "http://localhost") && !strings.HasPrefix(a.apiURL, "http://127.0.0.1") {
		a.out.Warnf("sending a stored key over plain HTTP to %s", a.apiURL)
	}
	return a.client()
}

// resolveConsoleURL answers where `login` should open a browser.
//
// The console is a DIFFERENT host from the API (app.open.email vs
// api.open.email), so it cannot be derived from --api-url — and guessing is the
// one thing that must not happen here. A custom API URL with no console URL is
// an error naming the flag, never a silent fall back to our production console:
// that would send somebody's authorization for a staging or self-hosted
// deployment to a login screen belonging to a different platform.
func (a *app) resolveConsoleURL() (string, error) {
	explicit := firstNonEmpty(a.flagConsoleURL, os.Getenv(envConsoleURL), a.profile.ConsoleURL)
	if explicit == "" {
		if a.apiURL != config.DefaultAPIURL {
			return "", usageError(fmt.Errorf(
				"no console URL for %s — pass --console-url (or set %s) for the deployment this API belongs to,\n"+
					"or use --api-key for a key you already hold", a.apiURL, envConsoleURL))
		}
		explicit = config.DefaultConsoleURL
	}
	trimmed := strings.TrimRight(strings.TrimSpace(explicit), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", usageError(fmt.Errorf("console URL %q is not an http(s) URL", explicit))
	}
	// A minted key comes back over this connection. Plain HTTP is allowed only
	// where it cannot leave the machine, which is how `wrangler dev` is used.
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", usageError(fmt.Errorf(
			"refusing to authorize over plain HTTP to %s — a minted key would travel unencrypted", trimmed))
	}
	return trimmed, nil
}

// isLoopbackHost reports whether a host can only be reached from this machine.
// Case-insensitive because DNS names are, and `http://LOCALHOST:8788` is a
// `wrangler dev` console that works — refusing it would be this CLI disagreeing
// with the resolver about what one name means.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// noKeyring reports whether the OS keychain should be bypassed for the file
// backend (--no-keyring or OPENEMAIL_NO_KEYRING truthy).
func (a *app) noKeyring() bool {
	if a.flagNoKeyring {
		return true
	}
	switch os.Getenv(envNoKeyring) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// moreHint tells the user how to fetch the next page (human output only).
func (a *app) moreHint(next string) {
	if next != "" {
		a.out.Msgf("more results — pass --cursor %s (or --all)", next)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
