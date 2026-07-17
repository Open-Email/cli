package cli

import (
	"io"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newWhoamiCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity the current key resolves to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := client.Resolve(cmd.Context())
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{
				"profile":     a.profileName,
				"apiUrl":      a.apiURL,
				"principal":   id.Type,
				"accountId":   id.AccountID,
				"keyId":       a.profile.KeyID,
				"keyName":     a.profile.KeyName,
				"keyStorage":  a.profile.KeyStorage,
				"tokenSource": a.tokenSource,
			}, func(w io.Writer) {
				a.printIdentity(w, id)
			})
			return nil
		},
	}
}

func (a *app) printIdentity(w io.Writer, id coreapi.Identity) {
	rows := [][]string{
		{"Principal", id.Type},
		{"API URL", a.apiURL},
		{"Profile", a.profileName},
	}
	if id.AccountID != "" {
		rows = append(rows, []string{"Account", id.AccountID})
	}
	if a.profile.KeyName != "" {
		rows = append(rows, []string{"Key", a.profile.KeyName + " (" + a.profile.KeyID + ")"})
	}
	if a.profile.DefaultMailbox != "" {
		rows = append(rows, []string{"Default mailbox", a.profile.DefaultMailbox})
	}
	src := a.tokenSource
	if src == "" {
		src = "none"
	}
	store := a.profile.KeyStorage
	if store == "" {
		store = src
	}
	rows = append(rows, []string{"Key source", src + " / " + store})
	printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
}
