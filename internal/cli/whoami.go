package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
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
			// The domain-verification token. It is per-ACCOUNT and stable, so this
			// is where a customer reads the value to publish BEFORE their first
			// domain exists — which is the whole point of it not being per-domain.
			// Best effort: a system key has no account, and whoami must keep
			// working if the read fails.
			verifyToken := ""
			if id.AccountID != "" {
				if acct, aerr := client.GetAccount(cmd.Context(), id.AccountID); aerr == nil && acct.VerificationToken != nil {
					verifyToken = *acct.VerificationToken
				}
			}
			// keyId/keyName/keyStorage describe the STORED profile credential; they
			// are meaningful only when the live token actually came from that profile.
			// An --api-key/env override authenticates as a different key with no local
			// record, so report blanks rather than the unrelated stored key.
			keyID, keyName, keyStore := "", "", ""
			if a.tokenSource == "profile" {
				keyID, keyName, keyStore = a.profile.KeyID, a.profile.KeyName, a.profile.KeyStorage
			}
			a.out.Emit(map[string]any{
				"profile":      a.profileName,
				"apiUrl":       a.apiURL,
				"principal":    id.Type,
				"accountId":    id.AccountID,
				"mailboxId":    id.MailboxID,
				"credentialId": id.CredentialID,
				"keyId":        keyID,
				"keyName":      keyName,
				"keyStorage":   keyStore,
				"tokenSource":  a.tokenSource,
				// Absent, not empty: core says nothing about most keys' kind, and a
				// consumer must not have to tell "" from a kind core deliberately
				// recorded as blank — the same reason idleExpiresAt below is null.
				"keyKind": func() any {
					if id.Kind == "" {
						return nil
					}
					return id.Kind
				}(),
				"idleExpiresAt": func() any {
					if id.IdleExpiresAt == 0 {
						return nil
					}
					return time.Unix(id.IdleExpiresAt, 0).UTC().Format(time.RFC3339)
				}(),
				// Named for what it does, not for the column: this is the domain
				// claim value, never a credential.
				"domainVerificationToken": verifyToken,
			}, func(w io.Writer) {
				a.printIdentity(w, id, verifyToken)
			})
			return nil
		},
	}
}

func (a *app) printIdentity(w io.Writer, id coreapi.Principal, verifyToken string) {
	rows := [][]string{
		{"Principal", id.Type},
		{"API URL", a.apiURL},
		{"Profile", a.profileName},
	}
	if id.AccountID != "" {
		rows = append(rows, []string{"Account", id.AccountID})
	}
	// Known only when core's /auth/whoami answered (a mailbox app password's own
	// mailbox — which is also its identity id — and the credential it minted).
	if id.MailboxID != "" {
		rows = append(rows, []string{"Mailbox", id.MailboxID})
	}
	if id.CredentialID != "" {
		rows = append(rows, []string{"Credential", id.CredentialID})
	}
	// The stored key id/name belong to the profile credential — show them only when
	// the active token came from the profile (an --api-key/env override has none).
	fromProfile := a.tokenSource == "profile"
	if fromProfile && a.profile.KeyName != "" {
		rows = append(rows, []string{"Key", a.profile.KeyName + " (" + a.profile.KeyID + ")"})
	}
	if a.profile.DefaultMailbox != "" {
		rows = append(rows, []string{"Default mailbox", a.profile.DefaultMailbox})
	}
	// When this key stops working if left alone. Shown as a date AND a
	// countdown: the date is what somebody acts on, the countdown is what makes
	// them read it. Absent for every key that does not lapse, which is most.
	if id.IdleExpiresAt > 0 {
		when := time.Unix(id.IdleExpiresAt, 0)
		days := int(time.Until(when).Hours() / 24)
		lapse := when.Format("2006-01-02")
		// Whether it has lapsed comes from the instant, never from the day count:
		// integer division truncates toward zero, so a key that died three hours
		// ago is "0 days" out and would be announced as lapsing today — a dead
		// key described as one that still works.
		switch {
		case when.Before(time.Now()):
			lapse += " (lapsed)"
		case days == 0:
			lapse += " (today)"
		default:
			lapse += fmt.Sprintf(" (in %d days, unless used)", days)
		}
		rows = append(rows, []string{"Lapses", lapse})
	}
	src := a.tokenSource
	if src == "" {
		src = "none"
	}
	store := src
	if fromProfile && a.profile.KeyStorage != "" {
		store = a.profile.KeyStorage
	}
	rows = append(rows, []string{"Key source", src + " / " + store})
	if verifyToken != "" {
		rows = append(rows, []string{"Domain verification", "_openemail.<domain> TXT openemail-verification=" + verifyToken})
	}
	printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
}
