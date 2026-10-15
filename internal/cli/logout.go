package cli

import (
	"fmt"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

func newLogoutCmd(a *app) *cobra.Command {
	var keepKey bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials (revoking the CLI's own minted key)",
		Long: "Remove the stored credentials for the active profile.\n\n" +
			"If the profile holds an account API key that the CLI minted during login, it\n" +
			"is revoked on the server (DELETE /api-keys/:id) before removing the local\n" +
			"secret unless --keep-key is passed.",
		Example: "  openemail logout\n" +
			"  openemail logout --profile staging\n" +
			"  openemail logout --keep-key",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runLogout(cmd, keepKey)
		},
	}
	cmd.Flags().BoolVar(&keepKey, "keep-key", false, "do not revoke the key on the server, only remove it locally")
	return cmd
}

func (a *app) runLogout(cmd *cobra.Command, keepKey bool) error {
	ctx := cmd.Context()
	if a.profile.KeyStorage == "" && a.profile.Role == "" && a.profile.KeyID == "" {
		a.out.Msgf("Not logged in for profile %q.", a.profileName)
		return nil
	}

	var storedToken string
	if a.profile.KeyStorage != "" {
		if tok, err := secrets.Load(a.cfg.ConfigDir(), a.profileName, a.profile.KeyStorage); err == nil {
			storedToken = tok
		}
	}

	// Revoke the CLI's own minted key (best-effort — a already-revoked or missing
	// key is not an error worth failing logout over).
	//
	// Only an ACCOUNT key the CLI minted can be revoked from here: `login` records
	// a KeyID in exactly that branch. Everything else — a mailbox app-password
	// login, a --no-mint login, a pasted system key — leaves a credential that is
	// still fully valid after "logout", and saying so is the point. Removing a
	// secret from this laptop is not revocation, and a user who believes it is
	// will not go and revoke it.
	revoked := false
	restricted := false
	if !keepKey && a.profile.Role == coreapi.PrincipalAccount && a.profile.KeyID != "" && storedToken != "" {
		profAPIURL := firstNonEmpty(a.profile.APIURL, config.DefaultAPIURL)
		client, err := coreapi.New(coreapi.Config{
			BaseURL:   profAPIURL,
			Token:     storedToken,
			UserAgent: "openemail-cli/" + Version,
		})
		if err == nil {
			rerr := client.RevokeAPIKey(ctx, a.profile.KeyID)
			switch {
			// 401: the key's own secret no longer authenticates, so it is
			// already revoked or lapsed (`keys revoke` on itself hints logout).
			case rerr == nil, coreapi.IsNotFound(rerr), coreapi.IsUnauthorized(rerr):
				revoked = true
			// A restricted key cannot revoke anything, itself included — the
			// /api-keys surface is account-tier. Not a warning: it is what the
			// key is, and the honest thing is to say the credential outlives
			// this logout rather than to report a failure the user cannot fix.
			case coreapi.IsAccountCredentialsRequired(rerr):
				restricted = true
			default:
				a.out.Warnf("could not revoke key %s on the server: %v", a.profile.KeyID, rerr)
			}
		}
	}
	stillLive := ""
	switch {
	case keepKey:
		stillLive = "the key is still valid on the server (--keep-key)"
	case restricted:
		stillLive = "this key is restricted and cannot revoke itself — revoke it from the console"
	case revoked:
	case a.profile.Role == coreapi.PrincipalAccount && a.profile.KeyID != "" && storedToken != "":
		stillLive = fmt.Sprintf("key %s could not be revoked, so it is still valid on the server; revoke it from the console", a.profile.KeyID)
	case a.profile.Role == coreapi.PrincipalMailbox:
		stillLive = "the app password is still valid — revoke it with `openemail credentials revoke <mailboxId> <credentialId>`"
	case storedToken != "":
		stillLive = "this key was not minted by the CLI, so it is still valid on the server — revoke it where it was created"
	}

	if err := secrets.Delete(a.cfg.ConfigDir(), a.profileName, a.profile.KeyStorage); err != nil {
		a.out.Warnf("could not remove stored key: %v", err)
	}
	// Also sweep any residual plaintext file left by an older backend switch
	// (Delete(File) is a no-op when the file is absent, so this stays quiet in the
	// common keychain case and never touches another profile's file).
	if a.profile.KeyStorage != secrets.File {
		if err := secrets.Delete(a.cfg.ConfigDir(), a.profileName, secrets.File); err != nil {
			a.out.Warnf("could not remove residual key file: %v", err)
		}
	}

	// Clear auth fields; keep the API URL so the profile remains usable for login.
	prof := a.profile
	prof.Role = ""
	prof.AccountID = ""
	prof.KeyStorage = ""
	prof.KeyID = ""
	prof.KeyName = ""
	prof.DefaultMailbox = ""
	a.cfg.SetProfile(a.profileName, prof)
	if err := a.cfg.Save(); err != nil {
		return err
	}

	a.out.Successf("Removed stored credentials for profile %q.", a.profileName)
	if stillLive != "" {
		a.out.Warnf("%s", stillLive)
	}
	return nil
}
