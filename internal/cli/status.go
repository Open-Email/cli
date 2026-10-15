package cli

import (
	"io"

	"github.com/spf13/cobra"
)

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check core reachability and authentication",
		Long: "Check whether the configured OpenEmail core API is reachable (/health) and,\n" +
			"if credentials are configured for the active profile, verify that the bearer\n" +
			"token resolves via /auth/whoami.",
		Example: "  openemail status\n" +
			"  openemail status --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			client, err := a.client()
			if err != nil {
				return err
			}

			healthErr := client.Health(ctx)
			healthy := healthErr == nil

			principal := ""
			authenticated := a.token != ""
			var resolveErr error
			var skipAuthErr error
			if authenticated {
				authed, aerr := a.authedClient()
				if aerr != nil {
					skipAuthErr = aerr
					authenticated = false
				} else if id, rerr := authed.Resolve(ctx); rerr == nil {
					principal = id.Type
				} else {
					resolveErr = rerr
					authenticated = false
				}
			}

			result := map[string]any{
				"apiUrl":        a.apiURL,
				"healthy":       healthy,
				"authenticated": authenticated,
				"principal":     principal,
			}
			// Why there is a key but no principal: not sent (it belongs to
			// another deployment) or rejected by core. The human view warns.
			switch {
			case skipAuthErr != nil:
				result["authError"] = skipAuthErr.Error()
			case resolveErr != nil:
				result["authError"] = resolveErr.Error()
			}
			a.out.Emit(result, func(w io.Writer) {
				if healthy {
					a.out.Successf("core reachable at %s", a.apiURL)
				} else {
					a.out.Warnf("core NOT reachable at %s: %v", a.apiURL, healthErr)
				}
				switch {
				case principal != "":
					a.out.Msgf("authenticated as %s (profile %q)", principal, a.profileName)
				case skipAuthErr != nil:
					a.out.Warnf("%v", skipAuthErr)
				case resolveErr != nil:
					a.out.Warnf("stored key rejected: %v", resolveErr)
				default:
					a.out.Msgf("not authenticated — run `openemail login`")
				}
				_ = w
			})
			return nil
		},
	}
}
