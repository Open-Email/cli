package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newCredentialsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credentials",
		Aliases: []string{"cred", "creds"},
		Short:   "Manage a mailbox's IMAP/SMTP credentials (account or system callers)",
		Long: "Create, list, and revoke a mailbox's IMAP/SMTP/JMAP/DAV credentials.\n\n" +
			"An app-password credential generates a one-time token (`oemp_...`) that also\n" +
			"works as a mailbox-scoped bearer token for the CLI and HTTP API.",
	}
	cmd.AddCommand(
		newCredentialCreateCmd(a),
		newCredentialListCmd(a),
		newCredentialRevokeCmd(a),
	)
	return cmd
}

func newCredentialCreateCmd(a *app) *cobra.Command {
	var kind, username, password, name, expiresIn string
	cmd := &cobra.Command{
		Use:     "create <mailboxId>",
		Aliases: []string{"new"},
		Short:   "Create a mailbox credential: generate an app password (shown once) or set a password",
		Long: "Create a mailbox credential. --username defaults to the mailbox's primary\n" +
			"address; an address-shaped username must route to this mailbox, while an\n" +
			"@-free username skips that check — how an identity created --without-address\n" +
			"gets a login for DAV/JMAP clients (it has no address to default to, so\n" +
			"--username is required there).",
		Example: "  openemail credentials create 01J00000000000000000000MBX --name \"Thunderbird\"\n" +
			"  openemail credentials create 01J00000000000000000000MBX --expires-in 30d\n" +
			"  openemail credentials create 01J00000000000000000000MBX --kind password",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKind, err := credentialKind(kind)
			if err != nil {
				return usageError(err)
			}
			in := coreapi.CredentialCreateInput{Kind: apiKind, Username: username}
			if cmd.Flags().Changed("name") {
				in.Name = &name
			}
			if expiresIn != "" {
				if apiKind != "app_password" {
					return usageError(errors.New("--expires-in applies only to kind app-password"))
				}
				d, derr := parseLifetime(expiresIn)
				if derr != nil {
					return usageError(fmt.Errorf("--expires-in: %w", derr))
				}
				in.ExpiresInSeconds = int64(d.Seconds())
			}
			if apiKind == "password" {
				if !cmd.Flags().Changed("password") {
					if promptTTY() {
						p, rerr := readSecretRaw("New password: ")
						if rerr != nil {
							return rerr
						}
						password = p
					} else if !term.IsTerminal(int(os.Stdin.Fd())) {
						p, rerr := readSecretRaw("")
						if rerr != nil {
							return rerr
						}
						password = p
					} else {
						return usageError(errors.New("--password is required for a password credential"))
					}
				}
				if password == "" {
					return usageError(errors.New("password must not be empty"))
				}
				in.Password = password
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			cred, err := client.CreateCredential(cmd.Context(), args[0], in)
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(cred, nil)
				return nil
			}
			a.out.Successf("Created %s credential %s (username %s)", sanitizeCell(cred.Kind), sanitizeCell(cred.ID), sanitizeCell(cred.Username))
			if cred.Token != "" {
				a.out.Warnf("This app-password token is shown only once — store it now.")
				fmt.Fprintln(a.out.out, cred.Token)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "app-password", "app-password (generated, shown once) | password (you set it)")
	cmd.Flags().StringVar(&username, "username", "", "login username (default: the primary address; required for an identity created --without-address, as a bare name with no @)")
	cmd.Flags().StringVar(&password, "password", "", "password (kind=password; prompted if omitted on a TTY)")
	cmd.Flags().StringVar(&name, "name", "", "human label for the credential")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "app-password lifetime, e.g. 12h or 30d (default: never expires)")
	_ = cmd.RegisterFlagCompletionFunc("kind", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"app-password", "password"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func newCredentialListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <mailboxId>",
		Aliases: []string{"ls"},
		Short:   "List a mailbox's credentials (never shows secrets)",
		Example: "  openemail credentials list 01J00000000000000000000MBX",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			creds, err := client.ListCredentials(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			now := time.Now().Unix()
			a.out.Emit(map[string]any{"credentials": creds}, func(w io.Writer) {
				rows := make([][]string, 0, len(creds))
				for _, c := range creds {
					status := "active"
					switch {
					case c.RevokedAt != nil:
						status = "revoked"
					case c.ExpiresAt != nil && *c.ExpiresAt <= now:
						status = "expired"
					}
					rows = append(rows, []string{
						c.ID, c.Kind, c.Username, strOr(c.Name, "—"),
						fmtEpoch(c.CreatedAt), fmtEpochPtr(c.LastUsedAt), fmtEpochPtr(c.ExpiresAt), status,
					})
				}
				printTable(w, a.out, []string{"ID", "KIND", "USERNAME", "NAME", "CREATED", "LAST USED", "EXPIRES", "STATUS"}, rows)
			})
			return nil
		},
	}
}

func newCredentialRevokeCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "revoke <mailboxId> <credentialId>",
		Aliases: []string{"rm", "delete"},
		Short:   "Revoke a mailbox credential",
		Example: "  openemail credentials revoke 01J00000000000000000000MBX 01J0000000000000000000CRED --yes",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(fmt.Sprintf("Revoke credential %s?", args[1])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if err := client.RevokeCredential(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"revoked": true, "id": args[1]}, func(w io.Writer) {
				a.out.Successf("Revoked credential %s", sanitizeCell(args[1]))
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// credentialKind maps the CLI's --kind value to core's wire value.
func credentialKind(k string) (string, error) {
	switch k {
	case "app-password", "app_password":
		return "app_password", nil
	case "password":
		return "password", nil
	default:
		return "", fmt.Errorf("invalid --kind %q: expected app-password or password", k)
	}
}
