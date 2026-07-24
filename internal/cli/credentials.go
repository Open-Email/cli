package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newCredentialsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credentials",
		Aliases: []string{"cred", "creds"},
		Short:   "Manage a mailbox's IMAP/SMTP credentials (account or system callers)",
	}
	cmd.AddCommand(
		newCredentialCreateCmd(a),
		newCredentialListCmd(a),
		newCredentialRevokeCmd(a),
	)
	return cmd
}

func newCredentialCreateCmd(a *app) *cobra.Command {
	var kind, username, password, name string
	cmd := &cobra.Command{
		Use:   "create <mailboxId>",
		Short: "Create a mailbox credential: generate an app password (shown once) or set a password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			apiKind, err := credentialKind(kind)
			if err != nil {
				return usageError(err)
			}
			in := coreapi.CredentialCreateInput{Kind: apiKind, Username: username}
			if cmd.Flags().Changed("name") {
				in.Name = &name
			}
			if apiKind == "password" {
				if !cmd.Flags().Changed("password") {
					if !promptTTY() {
						return usageError(errors.New("--password is required for a password credential"))
					}
					p, rerr := readSecretRaw("New password: ")
					if rerr != nil {
						return rerr
					}
					password = p
				}
				if password == "" {
					return usageError(errors.New("password must not be empty"))
				}
				in.Password = password
			}
			cred, err := client.CreateCredential(cmd.Context(), args[0], in)
			if err != nil {
				return err
			}
			if a.out.JSON() {
				a.out.Emit(cred, nil)
				return nil
			}
			a.out.Successf("Created %s credential %s (username %s)", cred.Kind, cred.ID, cred.Username)
			if cred.Token != "" {
				a.out.Warnf("This app-password token is shown only once — store it now.")
				fmt.Fprintln(os.Stdout, cred.Token)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "app-password", "app-password (generated, shown once) | password (you set it)")
	cmd.Flags().StringVar(&username, "username", "", "login username (defaults to the mailbox's primary address)")
	cmd.Flags().StringVar(&password, "password", "", "password (kind=password; prompted if omitted on a TTY)")
	cmd.Flags().StringVar(&name, "name", "", "human label for the credential")
	return cmd
}

func newCredentialListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <mailboxId>",
		Aliases: []string{"ls"},
		Short:   "List a mailbox's credentials (never shows secrets)",
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
			a.out.Emit(map[string]any{"credentials": creds}, func(w io.Writer) {
				rows := make([][]string, 0, len(creds))
				for _, c := range creds {
					status := "active"
					if c.RevokedAt != nil {
						status = "revoked"
					}
					rows = append(rows, []string{
						c.ID, c.Kind, c.Username, strOr(c.Name, "—"),
						fmtEpoch(c.CreatedAt), fmtEpochPtr(c.LastUsedAt), status,
					})
				}
				printTable(w, a.out, []string{"ID", "KIND", "USERNAME", "NAME", "CREATED", "LAST USED", "STATUS"}, rows)
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
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Revoke credential %s?", args[1])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			if err := client.RevokeCredential(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"revoked": true, "id": args[1]}, func(w io.Writer) {
				a.out.Successf("Revoked credential %s", args[1])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
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
