package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

// loginOpts is how the flags reach runLogin. A struct rather than six
// parameters because the acquisition path is decided from their combination,
// and reading that decision needs the flags named at the point of use.
type loginOpts struct {
	name      string
	noMint    bool
	mailbox   string
	device    bool
	noBrowser bool
	paste     bool
}

func newLoginCmd(a *app) *cobra.Command {
	var opts loginOpts
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate and store an API key for this profile",
		Long: "Authenticate to an OpenEmail deployment.\n\n" +
			"By default this opens a browser: you approve the request in the console,\n" +
			"which mints a key for the organization you choose and hands it back. The\n" +
			"key is stored in your OS keychain and nothing is pasted anywhere.\n\n" +
			"Without a usable browser (over SSH, in a container) the CLI falls back to\n" +
			"a device code you can approve from any other machine; --device forces it.\n\n" +
			"Non-interactive: pass --api-key or set OPENEMAIL_API_KEY. For an account\n" +
			"key the CLI then mints a dedicated per-device key of its own and discards\n" +
			"the one supplied. A mailbox app password (oemp_…) logs in to a single\n" +
			"mailbox with limited scope.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runLogin(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.name, "name", "", "name for the minted key (default: openemail-cli @ <hostname>)")
	f.BoolVar(&opts.noMint, "no-mint", false, "store the pasted key as-is instead of minting a per-device key")
	f.StringVar(&opts.mailbox, "mailbox", "", "default mailbox id (for mailbox app-password logins)")
	f.BoolVar(&opts.device, "device", false, "authorize with a device code instead of a local browser redirect")
	f.BoolVar(&opts.noBrowser, "no-browser", false, "never launch a browser (implies --device)")
	f.BoolVar(&opts.paste, "paste", false, "paste an existing API key instead of authorizing in a browser")
	return cmd
}

// defaultKeyName names the key after the machine it will live on. One person
// has several, and a list of identical "openemail-cli" rows is a list nobody
// can revoke selectively.
func defaultKeyName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return "openemail-cli @ " + host
}

func (a *app) runLogin(cmd *cobra.Command, opts loginOpts) error {
	ctx := cmd.Context()

	// The login key comes from --api-key or the env var — never the stored
	// profile secret (that would be circular).
	inputKey := strings.TrimSpace(firstNonEmpty(a.flagAPIKey, os.Getenv(envAPIKey)))
	keyName := defaultKeyName(opts.name)

	// How this login acquires a credential, in precedence order: a key supplied
	// for this invocation, an explicit --paste, else the browser handshake.
	var web *webLoginResult
	if inputKey == "" && !opts.paste {
		consoleURL, err := a.resolveConsoleURL()
		if err != nil {
			return err
		}
		// Deployment identity, checked while nothing exists yet: the console
		// says which core API its keys belong to, and a mismatch with this
		// login's --api-url is refused HERE — before a browser opens, before a
		// grant row is written, before anything needs revoking. The redemption
		// response repeats the fact as the belt to these braces.
		if advertised := consoleAdvertisedAPIURL(ctx, consoleURL); advertised != "" && !sameAPIOrigin(advertised, a.apiURL) {
			return usageError(fmt.Errorf(
				"the console at %s serves the API at %s, but this login targets %s — nothing was authorized.\n"+
					"Pass the --console-url that belongs to %s, or --api-url %s.",
				consoleURL, advertised, a.apiURL, a.apiURL, advertised))
		}
		if opts.device || opts.noBrowser || !browserLikelyAvailable() {
			web, err = deviceLogin(ctx, a.out, consoleURL, keyName, !opts.noBrowser)
		} else {
			web, err = loopbackLogin(ctx, a.out, consoleURL, keyName)
		}
		if err != nil {
			if errors.Is(err, errAuthDenied) {
				return &ExitError{Code: 4, Err: err}
			}
			return err
		}
	}

	if web == nil && inputKey == "" {
		if !promptTTY() {
			return usageError(fmt.Errorf("no API key provided — pass --api-key or set %s", envAPIKey))
		}
		key, err := readSecret(fmt.Sprintf("Paste your OpenEmail API key for %s: ", a.apiURL))
		if err != nil {
			return err
		}
		inputKey = strings.TrimSpace(key)
		if inputKey == "" {
			return usageError(fmt.Errorf("empty API key"))
		}
	}

	// From here the two paths converge on one token, whoever produced it.
	finalToken := inputKey
	if web != nil {
		finalToken = web.Token
	}

	var (
		keyID    string
		keyLabel string
		// managedKeyID is set iff THIS login produced a key the CLI owns — one
		// it minted, or one the console minted for it. It is what licenses both
		// the rollback below and the revoke of the key being replaced; the
		// distinction is "did this login create the credential", not "who called
		// core", which is why the browser path sets it too.
		managedKeyID string
	)
	// Where a rollback revocation must be sent. Normally this login's own API —
	// but a console-minted key belongs to whatever deployment the console
	// advertised, and on a mismatch that is the ONE place a revocation works.
	revokeAPIURL := a.apiURL
	if web != nil {
		keyID, keyLabel = web.KeyID, web.KeyName
		managedKeyID = web.KeyID
		if web.APIURL != "" {
			revokeAPIURL = web.APIURL
		}
	}

	// If anything from here to commit fails — a mismatched deployment, a 401, a
	// failed secret store or config write — the new key would be stranded on the
	// server with no local record. Roll it back unless the login commits.
	//
	// Armed BEFORE the whoami call, because for a browser-minted key the key
	// already exists: a classification failure is inside the window this defer
	// exists for. It revokes with the new key itself (see revokeMintedKey), which
	// an unscoped account key may do — it reaches any of the account's keys, its
	// own included.
	committed := false
	defer func() {
		if managedKeyID == "" || committed {
			return
		}
		// Detached from ctx, and deliberately: the likeliest reason this defer is
		// running at all is the Ctrl-C that cancelled ctx, and a revocation sent
		// on a cancelled context fails before it is dialled — leaving exactly the
		// stranded key the rollback exists to prevent.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if rerr := a.revokeMintedKey(rollbackCtx, revokeAPIURL, finalToken, managedKeyID); rerr != nil {
			// The one thing that must never be silent. Nothing on this machine
			// will hold that key after this process exits, so if the user is not
			// told which key on which deployment survived, nobody ever revokes it.
			a.out.Warnf("could not roll back key %s on %s (%v) — revoke it there by hand", managedKeyID, revokeAPIURL, rerr)
			return
		}
		a.out.Warnf("rolled back the new key after login did not complete")
	}()

	// The belt to the pre-flight's braces: the console named the deployment this
	// key belongs to, and it is not the one this login targets. Refusing here —
	// after revoking against the ADVERTISED API, the one place a revocation is
	// accepted — is what turns "a key stranded where nothing can reach it" into
	// "nothing happened".
	if web != nil && web.APIURL != "" && !sameAPIOrigin(web.APIURL, a.apiURL) {
		// Revoked here rather than left to the defer, because the sentence the
		// user reads has to be the one that actually happened: "revoked" is a
		// claim, and claiming it while the revocation silently failed tells
		// somebody a live key is dead. Clearing managedKeyID hands the outcome
		// to this branch alone — the defer would otherwise DELETE the same key a
		// second time and report the 404 as a failed rollback.
		rerr := a.revokeMintedKey(ctx, revokeAPIURL, finalToken, managedKeyID)
		managedKeyID = ""
		if rerr != nil {
			return fmt.Errorf(
				"the console issued a key for %s, but this login targets %s — nothing was stored, and the key could NOT be revoked (%v).\n"+
					"Revoke key %s at %s yourself: no credential left on this machine can reach it.\n"+
					"Pass the --console-url that belongs to %s, or --api-url %s.",
				web.APIURL, a.apiURL, rerr, keyID, web.APIURL, a.apiURL, web.APIURL)
		}
		return fmt.Errorf(
			"the console issued a key for %s, but this login targets %s — the key was revoked and nothing was stored.\n"+
				"Pass the --console-url that belongs to %s, or --api-url %s.",
			web.APIURL, a.apiURL, a.apiURL, web.APIURL)
	}

	client, err := a.clientWithToken(finalToken)
	if err != nil {
		return err
	}
	// Classified from core, never from the console's answer: role and account
	// are core's facts about this bearer, and a login that trusted a middleman
	// for them would record whatever that middleman said.
	identity, err := client.Resolve(ctx)
	if err != nil {
		if coreapi.IsUnauthorized(err) {
			if web != nil {
				return fmt.Errorf("the key the console issued was not accepted by %s (401)", a.apiURL)
			}
			return fmt.Errorf("the key was not accepted by %s (401)", a.apiURL)
		}
		return err
	}

	// Whatever key this profile held before — revoked only once the new login is
	// fully committed, so a re-login doesn't leave the old key alive on the server.
	// oldBackend lets us delete a stale secret if this login lands in a different
	// store (e.g. a keychain outage forced a file fallback last time).
	oldKeyID, oldRole, oldBackend := a.profile.KeyID, a.profile.Role, a.profile.KeyStorage

	role := identity.Type
	accountID := identity.AccountID

	switch {
	case web != nil:
		// The console already minted (and the identity fields above were read
		// with its key); minting again would leave two keys for one machine,
		// one of which nothing would ever revoke.
	case identity.Type == coreapi.PrincipalAccount && opts.noMint:
		a.out.Msgf("Storing the provided account key as-is (--no-mint).")
	case identity.Type == coreapi.PrincipalAccount:
		created, cerr := client.CreateAPIKey(ctx, keyName, "", "")
		switch {
		case cerr == nil:
			finalToken = created.Token
			keyID, keyLabel = created.ID, created.Name
			managedKeyID = created.ID
			if accountID == "" && created.AccountID != nil {
				accountID = *created.AccountID
			}
			// A restricted key cannot mint (the /api-keys surface is account-tier),
			// so this is not a failure — it is what a delegated key is FOR. Store
			// what we were given and say why there is no per-device key, because
			// the difference shows up later at logout, which cannot revoke it.
		case coreapi.IsAccountCredentialsRequired(cerr):
			a.out.Warnf("this key is restricted and cannot mint keys — storing it as provided")
			a.out.Msgf("`openemail logout` will remove it locally but cannot revoke it on the server.")
		default:
			return fmt.Errorf("mint per-device key: %w", cerr)
		}
	case identity.Type == coreapi.PrincipalSystem:
		a.out.Warnf("This is a SYSTEM key — full operator access to every tenant. Storing as-is.")
	case identity.Type == coreapi.PrincipalMailbox:
		a.out.Msgf("Logged in as a single mailbox (limited scope). Directory and admin commands are unavailable.")
	default:
		return fmt.Errorf("could not classify the key")
	}

	// Read the outgoing secret BEFORE it is overwritten. Everything between the
	// store and the config write can still fail, and the rollback above then
	// revokes the new key — so without a copy of the old one, a failed re-login
	// leaves the machine holding a revoked token while the credential that was
	// working seconds ago is gone from the keychain. Best-effort: a first login
	// has none, and its absence is no reason to refuse this one.
	previousSecret := ""
	if oldBackend != "" {
		if prev, lerr := secrets.Load(a.cfg.ConfigDir(), a.profileName, oldBackend); lerr == nil {
			previousSecret = prev
		}
	}
	// Put the old secret back where the (unchanged) config still points, so a
	// failure from here on costs the login and nothing else.
	restorePrevious := func() {
		if previousSecret == "" {
			return
		}
		if _, _, rerr := secrets.Save(a.cfg.ConfigDir(), a.profileName, previousSecret, oldBackend == secrets.File); rerr != nil {
			a.out.Warnf("could not put the previous key back for profile %q: %v — run `openemail login` again", a.profileName, rerr)
		}
	}
	backend, warn, serr := secrets.Save(a.cfg.ConfigDir(), a.profileName, finalToken, a.noKeyring())
	if warn != nil {
		a.out.Warnf("%v", warn)
	}
	if serr != nil {
		// A store that failed may still have destroyed what was there — the file
		// backend truncates before it writes — so this failure needs the same
		// repair as the ones below it.
		restorePrevious()
		return serr
	}

	prof := a.profile
	prof.APIURL = a.apiURL
	prof.Role = role
	prof.AccountID = accountID
	prof.KeyStorage = backend
	prof.KeyID = keyID
	prof.KeyName = keyLabel
	// Remember where this profile authorizes, so a later `login` on a custom
	// deployment needs the flag exactly once.
	if consoleURL := firstNonEmpty(a.flagConsoleURL, os.Getenv(envConsoleURL)); consoleURL != "" {
		prof.ConsoleURL = strings.TrimRight(consoleURL, "/")
	}
	if opts.mailbox != "" {
		prof.DefaultMailbox = opts.mailbox
	}
	if err := a.cfg.SetProfile(a.profileName, prof); err != nil {
		restorePrevious()
		return err
	}
	if a.cfg.DefaultProfile == "" {
		a.cfg.DefaultProfile = a.profileName
	}
	if err := a.cfg.Save(); err != nil {
		restorePrevious()
		return err
	}
	committed = true

	// If the secret landed in a different store than the profile used before,
	// delete the stale copy so a switched credential never lingers on disk — in
	// particular a plaintext file left behind by a past keychain outage that a
	// later keychain login would otherwise never clean up. Best-effort.
	if oldBackend != "" && oldBackend != backend {
		if derr := secrets.Delete(a.cfg.ConfigDir(), a.profileName, oldBackend); derr != nil {
			a.out.Warnf("could not remove the previous credential store (%s): %v", oldBackend, derr)
		}
	}

	// Now that the new key is stored and recorded, revoke the device key it
	// replaced (best-effort). Gated on managedKeyID so a --no-mint or restricted
	// login — where we store a user-managed key and may even be storing the old
	// key back — never revokes anything the user is managing themselves. A 404
	// is the normal answer when the previous key belonged to another account,
	// which happens whenever this login chose a different organization.
	if managedKeyID != "" && oldKeyID != "" && oldKeyID != managedKeyID && oldRole == coreapi.PrincipalAccount {
		if rerr := client.RevokeAPIKey(ctx, oldKeyID); rerr != nil &&
			!coreapi.IsNotFound(rerr) && !coreapi.IsAccountCredentialsRequired(rerr) {
			a.out.Warnf("could not revoke the previous key %s: %v", oldKeyID, rerr)
		}
	}

	a.emitLoginResult(prof, backend)
	return nil
}

// rollbackTimeout bounds a revocation nobody is waiting for. Short, because it
// runs while the command is already on its way out — but not zero, since the
// alternative to a slow revocation here is a live key nobody knows about.
const rollbackTimeout = 15 * time.Second

// revokeMintedKey revokes a key this login created, using the key ITSELF —
// the only credential this process is guaranteed to hold, since the browser
// path never sees another and the console kept nothing it could revoke with.
// It returns the failure rather than swallowing it: the caller is the only one
// who knows whether the user is about to be told that key is dead.
func (a *app) revokeMintedKey(ctx context.Context, apiURL, token, keyID string) error {
	rc, err := coreapi.New(coreapi.Config{
		BaseURL:   apiURL,
		Token:     token,
		UserAgent: "openemail-cli/" + Version,
	})
	if err != nil {
		return err
	}
	return rc.RevokeAPIKey(ctx, keyID)
}

func (a *app) emitLoginResult(prof config.Profile, backend string) {
	if a.out.JSON() {
		a.out.Emit(map[string]any{
			"profile":    a.profileName,
			"apiUrl":     prof.APIURL,
			"role":       prof.Role,
			"accountId":  prof.AccountID,
			"keyId":      prof.KeyID,
			"keyName":    prof.KeyName,
			"keyStorage": backend,
		}, nil)
		return
	}
	a.out.Successf("Logged in to %s", a.out.Bold(prof.APIURL))
	a.out.Msgf("  profile:  %s", a.profileName)
	a.out.Msgf("  role:     %s", prof.Role)
	if prof.AccountID != "" {
		a.out.Msgf("  account:  %s", prof.AccountID)
	}
	if prof.KeyName != "" {
		a.out.Msgf("  key:      %s (%s)", prof.KeyName, prof.KeyID)
	}
	a.out.Msgf("  key store: %s", backend)
}
