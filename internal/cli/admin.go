package cli

import (
	"errors"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newAdminCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Operator-only maintenance (system credentials required)",
		Long:  "System-only maintenance commands. Hidden unless the active profile is a system key.",
	}
	cmd.AddCommand(
		newAdminReindexCmd(a),
		newAdminVerifyLoginCmd(a),
		newAdminPickupCmd(a),
	)
	a.adminCmd = cmd
	return cmd
}

func newAdminReindexCmd(a *app) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "reindex <mailboxId>",
		Short: "Re-enqueue full-text index jobs for a mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			enqueued, err := client.Reindex(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"enqueued": enqueued}, func(w io.Writer) {
				a.out.Successf("Enqueued %d index job(s) for %s", enqueued, args[0])
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "bound the reindex batch (1–5000)")
	return cmd
}

func newAdminVerifyLoginCmd(a *app) *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "verify-login <username>",
		Short: "Verify IMAP/SMTP credentials and show resolved sendability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("password") {
				if !promptTTY() {
					return usageError(errors.New("--password is required (no TTY to prompt)"))
				}
				pw, rerr := readSecretRaw("Password: ")
				if rerr != nil {
					return rerr
				}
				password = pw
			}
			res, err := client.VerifyLogin(cmd.Context(), args[0], password)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				rows := [][]string{
					{"Identity", res.IdentityID},
					// "—" = a calendar-only identity: no mail store to serve.
					{"Mailbox", strOr(res.MailboxID, "—")},
					{"Account", strOr(res.AccountID, "—")},
					{"Credential", res.CredentialID},
					{"Kind", res.Kind},
					{"Can send", boolYN(res.CanSend)},
				}
				if len(res.Facets) > 0 {
					rows = append(rows, []string{"Facets", joinStrings(res.Facets)})
				}
				if len(res.PermittedFrom) > 0 {
					rows = append(rows, []string{"Permitted from", joinStrings(res.PermittedFrom)})
				}
				printTable(w, a.out, []string{"FIELD", "VALUE"}, rows)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "credential password (prompted if omitted on a TTY)")
	return cmd
}

func newAdminPickupCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pickup",
		Short: "Poppy pickup ingest/report (system-only)",
	}
	cmd.AddCommand(
		newAdminPickupIngestCmd(a),
		newAdminPickupReportCmd(a),
	)
	return cmd
}

func newAdminPickupIngestCmd(a *app) *cobra.Command {
	var file, checksum string
	cmd := &cobra.Command{
		Use:   "ingest <sourceId>",
		Short: "Ingest a fetched RFC822 message for a pickup source (Poppy path)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			getBody, length, cleanup, err := openRawBody(file)
			if err != nil {
				return err
			}
			defer cleanup()
			res, raw, err := client.PickupIngest(cmd.Context(), args[0], getBody, length, checksum)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) { renderDeliverResult(a.out, res, false) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "raw message file (default: stdin)")
	cmd.Flags().StringVar(&checksum, "checksum", "", "X-Checksum (BLAKE3 hex) — the delivery-key suffix")
	return cmd
}

func newAdminPickupReportCmd(a *app) *cobra.Command {
	var (
		status, errMsg, jobID    string
		uploaded, deleted, faild int64
	)
	cmd := &cobra.Command{
		Use:   "report <sourceId> --status <ok|partial|auth_failed|error>",
		Short: "Record a pickup run's outcome (Poppy callback)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			switch status {
			case "ok", "partial", "auth_failed", "error":
			default:
				return usageError(errors.New("--status must be one of ok|partial|auth_failed|error"))
			}
			in := coreapi.PickupReportInput{Status: status, JobID: jobID}
			if cmd.Flags().Changed("error") {
				in.Error = &errMsg
			}
			if cmd.Flags().Changed("uploaded") {
				in.Uploaded = &uploaded
			}
			if cmd.Flags().Changed("deleted") {
				in.Deleted = &deleted
			}
			if cmd.Flags().Changed("failed") {
				in.Failed = &faild
			}
			if err := client.PickupReport(cmd.Context(), args[0], in); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"recorded": true}, func(w io.Writer) {
				a.out.Successf("Recorded %s run for source %s", status, args[0])
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "run status: ok|partial|auth_failed|error (required)")
	cmd.Flags().StringVar(&errMsg, "error", "", "error detail (auth_failed/error)")
	cmd.Flags().StringVar(&jobID, "job-id", "", "opaque job id (echoed, not persisted)")
	cmd.Flags().Int64Var(&uploaded, "uploaded", 0, "messages uploaded (not persisted)")
	cmd.Flags().Int64Var(&deleted, "deleted", 0, "messages deleted (not persisted)")
	cmd.Flags().Int64Var(&faild, "failed", 0, "messages failed (not persisted)")
	return cmd
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
