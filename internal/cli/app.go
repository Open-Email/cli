package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/openemail/openemail-cli/internal/config"
	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/openemail/openemail-cli/internal/secrets"
	"github.com/spf13/cobra"
)

// Version is the CLI version (overridden at build time via -ldflags).
var Version = "0.1.0-dev"

// Environment variable names (flag > env > profile precedence).
const (
	envAPIKey    = "OPENEMAIL_API_KEY"
	envAPIURL    = "OPENEMAIL_API_URL"
	envProfile   = "OPENEMAIL_PROFILE"
	envNoKeyring = "OPENEMAIL_NO_KEYRING"
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
	flagProfile   string
	flagAPIURL    string
	flagAPIKey    string
	flagJSON      bool
	flagNoColor   bool
	flagDebug     bool
	flagNoKeyring bool
	flagMailbox   string // -m/--mailbox on mailbox-scoped groups

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

	// API URL: flag > env > profile > default.
	a.apiURL = firstNonEmpty(a.flagAPIURL, os.Getenv(envAPIURL), a.profile.APIURL, config.DefaultAPIURL)

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
	return a.client()
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
