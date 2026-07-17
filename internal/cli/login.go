package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/openemail/openemail-cli/internal/config"
	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/openemail/openemail-cli/internal/secrets"
	"github.com/spf13/cobra"
)

func newLoginCmd(a *app) *cobra.Command {
	var (
		name    string
		noMint  bool
		mailbox string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate and store an API key for this profile",
		Long: "Authenticate to an OpenEmail deployment.\n\n" +
			"Paste an existing account API key (oek_…). For an account key the CLI\n" +
			"immediately mints a dedicated per-device key of its own, stores it in your\n" +
			"OS keychain, and discards the pasted key. A mailbox app password (oemp_…)\n" +
			"logs in to a single mailbox with limited scope.\n\n" +
			"Non-interactive: pass --api-key or set OPENEMAIL_API_KEY.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runLogin(cmd, name, noMint, mailbox)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name for the minted key (default: openemail-cli @ <hostname>)")
	cmd.Flags().BoolVar(&noMint, "no-mint", false, "store the pasted key as-is instead of minting a per-device key")
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "default mailbox id (for mailbox app-password logins)")
	return cmd
}

func (a *app) runLogin(cmd *cobra.Command, name string, noMint bool, mailbox string) error {
	ctx := cmd.Context()

	// The login key comes from --api-key or the env var — never the stored
	// profile secret (that would be circular).
	inputKey := firstNonEmpty(a.flagAPIKey, os.Getenv(envAPIKey))
	if inputKey == "" {
		if !promptTTY() {
			return usageError(fmt.Errorf("no API key provided — pass --api-key or set %s", envAPIKey))
		}
		key, err := readSecret(fmt.Sprintf("Paste your OpenEmail API key for %s: ", a.apiURL))
		if err != nil {
			return err
		}
		inputKey = key
	}
	inputKey = strings.TrimSpace(inputKey)
	if inputKey == "" {
		return usageError(fmt.Errorf("empty API key"))
	}

	client, err := a.clientWithToken(inputKey)
	if err != nil {
		return err
	}
	identity, err := client.Resolve(ctx)
	if err != nil {
		if coreapi.IsUnauthorized(err) {
			return fmt.Errorf("the key was not accepted by %s (401)", a.apiURL)
		}
		return err
	}

	// Whatever key this profile held before — revoked only once the new login is
	// fully committed, so a re-login doesn't leave the old key alive on the server.
	oldKeyID, oldRole := a.profile.KeyID, a.profile.Role

	var (
		finalToken = inputKey
		role       = identity.Type
		accountID  = identity.AccountID
		keyID      string
		keyName    string
		mintedID   string // set iff we minted a key this run; rolled back on failure
	)

	switch identity.Type {
	case coreapi.PrincipalAccount:
		if noMint {
			a.out.Msgf("Storing the provided account key as-is (--no-mint).")
		} else {
			if name == "" {
				host, _ := os.Hostname()
				if host == "" {
					host = "unknown-host"
				}
				name = "openemail-cli @ " + host
			}
			created, cerr := client.CreateAPIKey(ctx, name, "", "")
			if cerr != nil {
				return fmt.Errorf("mint per-device key: %w", cerr)
			}
			finalToken = created.Token
			keyID = created.ID
			keyName = created.Name
			mintedID = created.ID
			if accountID == "" && created.AccountID != nil {
				accountID = *created.AccountID
			}
		}
	case coreapi.PrincipalSystem:
		a.out.Warnf("This is a SYSTEM key — full operator access to every tenant. Storing as-is.")
	case coreapi.PrincipalMailbox:
		a.out.Msgf("Logged in as a single mailbox (limited scope). Directory and admin commands are unavailable.")
	default:
		return fmt.Errorf("could not classify the key")
	}

	// If anything after minting fails (secret store or config write), the minted
	// key would otherwise be stranded on the server with no local record. Roll it
	// back unless the login commits.
	committed := false
	defer func() {
		if mintedID != "" && !committed {
			if rerr := client.RevokeAPIKey(ctx, mintedID); rerr == nil {
				a.out.Warnf("rolled back the minted key after login did not complete")
			}
		}
	}()

	backend, warn, serr := secrets.Save(a.cfg.ConfigDir(), a.profileName, finalToken, a.noKeyring())
	if warn != nil {
		a.out.Warnf("%v", warn)
	}
	if serr != nil {
		return serr
	}

	prof := a.profile
	prof.APIURL = a.apiURL
	prof.Role = role
	prof.AccountID = accountID
	prof.KeyStorage = backend
	prof.KeyID = keyID
	prof.KeyName = keyName
	if mailbox != "" {
		prof.DefaultMailbox = mailbox
	}
	a.cfg.SetProfile(a.profileName, prof)
	if a.cfg.DefaultProfile == "" {
		a.cfg.DefaultProfile = a.profileName
	}
	if err := a.cfg.Save(); err != nil {
		return err
	}
	committed = true

	// Now that a freshly minted key is stored and recorded, revoke the device key
	// it replaced (best-effort). Gated on mintedID so a --no-mint login — where we
	// store a user-managed key and may even be pasting the old key back — never
	// revokes anything the user is managing themselves.
	if mintedID != "" && oldKeyID != "" && oldKeyID != mintedID && oldRole == coreapi.PrincipalAccount {
		if rerr := client.RevokeAPIKey(ctx, oldKeyID); rerr != nil && !coreapi.IsNotFound(rerr) {
			a.out.Warnf("could not revoke the previous key %s: %v", oldKeyID, rerr)
		}
	}

	a.emitLoginResult(prof, backend)
	return nil
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
