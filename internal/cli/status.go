package cli

import (
	"io"

	"github.com/spf13/cobra"
)

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check core reachability and authentication",
		Args:  cobra.NoArgs,
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
			if authenticated {
				if id, rerr := client.Resolve(ctx); rerr == nil {
					principal = id.Type
				} else {
					resolveErr = rerr
					authenticated = false
				}
			}

			a.out.Emit(map[string]any{
				"apiUrl":        a.apiURL,
				"healthy":       healthy,
				"authenticated": authenticated,
				"principal":     principal,
			}, func(w io.Writer) {
				if healthy {
					a.out.Successf("core reachable at %s", a.apiURL)
				} else {
					a.out.Warnf("core NOT reachable at %s: %v", a.apiURL, healthErr)
				}
				switch {
				case principal != "":
					a.out.Msgf("authenticated as %s (profile %q)", principal, a.profileName)
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
