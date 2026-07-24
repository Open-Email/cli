package cli

import (
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/Open-Email/cli/internal/secrets"
	"github.com/spf13/cobra"
)

func newLogoutCmd(a *app) *cobra.Command {
	var keepKey bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Revoke this profile's key and remove stored credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runLogout(cmd, keepKey)
		},
	}
	cmd.Flags().BoolVar(&keepKey, "keep-key", false, "do not revoke the key on the server, only remove it locally")
	return cmd
}

func (a *app) runLogout(cmd *cobra.Command, keepKey bool) error {
	ctx := cmd.Context()
	if a.profile.KeyStorage == "" && a.token == "" {
		a.out.Msgf("Not logged in for profile %q.", a.profileName)
		return nil
	}

	// Revoke the CLI's own minted key (best-effort — a already-revoked or missing
	// key is not an error worth failing logout over).
	if !keepKey && a.profile.Role == coreapi.PrincipalAccount && a.profile.KeyID != "" && a.token != "" {
		client, err := a.client()
		if err == nil {
			if rerr := client.RevokeAPIKey(ctx, a.profile.KeyID); rerr != nil && !coreapi.IsNotFound(rerr) {
				a.out.Warnf("could not revoke key %s on the server: %v", a.profile.KeyID, rerr)
			}
		}
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
	a.cfg.SetProfile(a.profileName, prof)
	if err := a.cfg.Save(); err != nil {
		return err
	}

	a.out.Successf("Logged out of profile %q.", a.profileName)
	return nil
}
