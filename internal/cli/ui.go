package cli

import (
	"context"
	"errors"
	"os"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/openemail/openemail-cli/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newUICmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ui",
		Aliases: []string{"console"},
		Short:   "Browse the platform in an interactive console (read-only)",
		Long: "Open a full-screen console: a sidebar of directory resources (mailboxes,\n" +
			"domains, routes, patterns, API keys) with tables, detail views, and a live\n" +
			"per-mailbox message list fed by the events WebSocket.\n\n" +
			"v1 is read-only — mutations stay with the flag commands. Requires an\n" +
			"account or system key; mailbox app passwords cannot browse the directory.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return errors.New("ui needs an interactive terminal (stdin and stdout must be TTYs)")
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel() // ends every event subscription the console opened

			// The sidebar is gated by principal. A stored profile key already
			// knows its role (login recorded it) — trust it and open instantly;
			// only flag/env tokens pay the Resolve probe, which also validates
			// the token up front (a bad key fails here, not as a blank screen).
			role := ""
			if a.tokenSource == "profile" &&
				(a.profile.Role == coreapi.PrincipalAccount || a.profile.Role == coreapi.PrincipalSystem) {
				role = a.profile.Role
			} else {
				ident, rerr := client.Resolve(ctx)
				if rerr != nil {
					return rerr
				}
				role = ident.Type
			}
			if role == coreapi.PrincipalMailbox {
				return errors.New("the console needs an account or system key — a mailbox app password cannot browse the directory (run `openemail login`)")
			}

			return tui.Run(ctx, tui.Options{
				Client:  client,
				Role:    role,
				APIURL:  a.apiURL,
				Token:   a.token,
				Profile: a.profileName,
				Version: Version,
			})
		},
	}
	return cmd
}
