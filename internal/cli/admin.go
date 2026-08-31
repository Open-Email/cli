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
		newAdminHoldCmd(a),
		newAdminReleaseCmd(a),
		newAdminVerifySendingCmd(a),
		newAdminReindexCmd(a),
		newAdminVerifyLoginCmd(a),
		newAdminPickupCmd(a),
		newAdminSuppressionsCmd(a),
		newAdminDkimCmd(a),
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
	var password, clientIP string
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
			res, err := client.VerifyLogin(cmd.Context(), args[0], password, clientIP)
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
				// Present only when Can send is no, and it is the field this
				// command exists for: "I can't send" has two answers that look
				// identical to the user and could not be more different to whoever
				// is debugging it — an abuse freeze the sender's MTA bounces off,
				// and a billing hold their MTA is politely queueing behind.
				if res.SendHold != "" {
					rows = append(rows, []string{"Send hold", sendHoldLabel(res.SendHold)})
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
	cmd.Flags().StringVar(&clientIP, "client-ip", "", "end client's IP to forward (adds the per-client attempt-throttle dimension)")
	return cmd
}

func newAdminPickupCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pickup",
		Short: "pop3-fetch pickup ingest/report (system-only)",
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
		Use:   "ingest <pickupId>",
		Short: "Ingest a fetched RFC822 message for a pickup source (pop3-fetch path)",
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
		Use:   "report <pickupId> --status <ok|partial|auth_failed|error>",
		Short: "Record a pickup run's outcome (pop3-fetch callback)",
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
				in.UploadedCount = &uploaded
			}
			if cmd.Flags().Changed("deleted") {
				in.DeletedCount = &deleted
			}
			if cmd.Flags().Changed("failed") {
				in.FailedCount = &faild
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

// sendHoldLabel names core's two send-hold modes in the terms an operator acts
// on — what the sending MTA is told, and what happens to mail already queued.
// An unrecognized mode is passed through verbatim rather than dropped: a third
// mode core adds later must still be visible here.
//
// "disabled" is deliberately not called an abuse freeze. Core computes it as a
// UNION (src/endpoints/credentials.ts): this mailbox's send_disabled, OR a
// permanent hold on its domain or account, OR the sending domain not being
// enabled, OR the address not resolving to this mailbox at all. Several of
// those are ordinary configuration an operator can undo in a minute, so the
// label describes the EFFECT — which is identical across all of them — and
// leaves the cause to `openemail admin domain`/`account`.
func sendHoldLabel(hold string) string {
	switch hold {
	case "disabled":
		return "disabled (permanent stop — 550 at the MTA, queued mail bounces; cause may be the mailbox, its domain or its account)"
	case "paused":
		return "paused (reversible hold — 451 + Retry-After; queued mail waits)"
	case "unverified":
		return "unverified (the domain's send records were never proven live — publish the bounce + DKIM records and re-run `domains create`)"
	}
	return hold
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

// sendHoldScopes are the three resources a send hold applies to, spelled as the
// CLI's own nouns. One verb over three scopes, because the API is one field
// (`sendHold`) at every scope and an operator at 2am should not have to
// remember which noun carried which flag.
var sendHoldScopes = map[string]bool{"domain": true, "mailbox": true, "account": true}

// applySendHold writes ONE resource's `sendHold` and prints the resource back.
// hold is "paused", "disabled" or "" (release — marshals as JSON null, which is
// what clears BOTH storage columns in core).
func (a *app) applySendHold(cmd *cobra.Command, scope, id, hold string) error {
	if !sendHoldScopes[scope] {
		return usageError(errors.New("scope must be one of: domain, mailbox, account"))
	}
	client, err := a.authedClient()
	if err != nil {
		return err
	}
	var value any
	if hold != "" {
		value = hold
	}
	patch := map[string]any{"sendHold": value}
	verb := map[string]string{"paused": "Paused", "disabled": "Stopped", "": "Released"}[hold]
	switch scope {
	case "domain":
		d, err := client.UpdateDomain(cmd.Context(), id, patch)
		if err != nil {
			return err
		}
		a.out.Emit(d, func(w io.Writer) {
			a.out.Successf("%s sending for domain %s", verb, d.Domain)
			printDomain(w, a.out, d)
		})
	case "mailbox":
		m, err := client.UpdateMailbox(cmd.Context(), id, patch)
		if err != nil {
			return err
		}
		a.out.Emit(m, func(w io.Writer) {
			a.out.Successf("%s sending for mailbox %s", verb, m.ID)
			printMailbox(w, a.out, m)
		})
	case "account":
		acc, err := client.UpdateAccount(cmd.Context(), id, patch)
		if err != nil {
			return err
		}
		a.out.Emit(acc, func(w io.Writer) {
			a.out.Successf("%s sending for account %s — every mailbox on every domain it owns", verb, acc.ID)
			printAccount(w, a.out, acc)
		})
	}
	return nil
}

func newAdminHoldCmd(a *app) *cobra.Command {
	var pause, stop bool
	cmd := &cobra.Command{
		Use:   "hold <domain|mailbox|account> <id> (--pause | --stop)",
		Short: "Hold or stop outbound sending for one domain, mailbox or account",
		Long: "Operator enforcement over outbound mail, at any of the three scopes. Two modes:\n\n" +
			"  --pause  REVERSIBLE HOLD. Submissions are refused temporarily (429), so an\n" +
			"           SMTP client gets a 451 and ITS OWN queue holds the mail, and the relay\n" +
			"           backlog is deferred rather than bounced. Use it for non-payment, or\n" +
			"           while investigating something you expect to clear.\n" +
			"  --stop   PERMANENT STOP. Submissions are refused (403), so an SMTP client gets\n" +
			"           550 and gives up, and mail already queued in the relay is bounced.\n" +
			"           Use it when you want the sending to END (abuse).\n\n" +
			"Pausing destroys no mail; stopping does. If you are not sure which you want, pause —\n" +
			"a hold can be escalated to a stop (run this again with --stop), but bounced mail cannot\n" +
			"be recalled. A hold outliving the relay's ~3.2-day retry window dead-letters that backlog\n" +
			"(preserved and redrivable, but no longer automatic).\n\n" +
			"Neither touches inbound: a held domain, mailbox or account still receives its mail, and\n" +
			"every mailbox stays readable. System keys only — a tenant that could lift its own hold\n" +
			"does not have one. Release with `openemail admin release`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Usage errors before authentication: exactly one mode, whatever
			// credentials the caller holds.
			if pause == stop {
				return usageError(errors.New("pass exactly one of --pause or --stop"))
			}
			hold := "paused"
			if stop {
				hold = "disabled"
			}
			return a.applySendHold(cmd, args[0], args[1], hold)
		},
	}
	cmd.Flags().BoolVar(&pause, "pause", false, "reversible HOLD: submissions deferred (429 → 451), queued mail held")
	cmd.Flags().BoolVar(&stop, "stop", false, "permanent STOP: submissions refused (403 → 550), queued mail bounced")
	return cmd
}

func newAdminReleaseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "release <domain|mailbox|account> <id>",
		Short: "Lift a send hold or stop (the inverse of `admin hold`)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.applySendHold(cmd, args[0], args[1], "")
		},
	}
}

func newAdminVerifySendingCmd(a *app) *cobra.Command {
	var revoke bool
	cmd := &cobra.Command{
		Use:   "verify-sending <domain>",
		Short: "Assert (or --revoke) a domain's send-records verdict outright",
		Long: "Sending is normally EARNED: the owner publishes the bounce and DKIM records and re-runs\n" +
			"`domains create`, and core flips `sendVerified` when they resolve. This is the operator\n" +
			"override for the cases that never onboard (a platform domain) or must be walked back\n" +
			"(--revoke). System keys only. Not a hold: a revoked domain reads as \"not yet verified\",\n" +
			"which is the state a tenant can fix themselves — use `admin hold` for enforcement.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			d, err := client.UpdateDomain(cmd.Context(), args[0], map[string]any{"sendVerified": !revoke})
			if err != nil {
				return err
			}
			a.out.Emit(d, func(w io.Writer) {
				if revoke {
					a.out.Successf("Revoked the send-records verdict for %s", d.Domain)
				} else {
					a.out.Successf("Marked %s's send records as verified", d.Domain)
				}
				printDomain(w, a.out, d)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "withdraw the verdict instead of asserting it")
	return cmd
}
