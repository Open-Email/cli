package cli

import (
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newMessageSubmitCmd sends a draft that is already stored.
//
// It is the counterpart to `messages compose`, which files a message without
// sending it: compose then submit is the two-step flow a review step wants, and
// unlike re-sending the fields through `send` it mails the exact bytes that were
// filed — the draft row itself becomes the Sent copy.
func newMessageSubmitCmd(a *app) *cobra.Command {
	var (
		deliveryID string
		bounce     bool
	)
	cmd := &cobra.Command{
		Use:     "submit <messageId>",
		Aliases: []string{"send-draft"},
		Short:   "Send a stored draft, byte-for-byte as stored",
		Long: "Sends a message already filed in this mailbox as a draft. Core reads the stored\n" +
			"bytes, derives the envelope from the message's own headers, strips Bcc from\n" +
			"the wire copy, and turns the draft into the Sent copy — nothing is rebuilt, so\n" +
			"what arrives is exactly what you filed.\n\n" +
			"  openemail messages compose --to you@example.com --subject Hi --text ...\n" +
			"  openemail messages submit <messageId>\n\n" +
			"Every From address in the draft must be this mailbox's own — a draft's headers\n" +
			"are client-written, so the right to send under them is re-checked here.\n\n" +
			"The message must still BE a draft: one moved to Trash in the meantime is\n" +
			"refused rather than sent out of the bin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			// Defaulted to a fresh ULID for the same reason `send` does it: a
			// partial failure exits non-zero, the obvious next move is to re-run,
			// and without a key that re-run mails everyone who already succeeded a
			// second time.
			opts := coreapi.SubmitOptions{DeliveryID: firstNonEmpty(deliveryID, newDeliveryID())}
			opts.Bounce = boolPtrIfChanged(cmd, "bounce", bounce)

			res, raw, err := client.SubmitDraft(cmd.Context(), mbx, args[0], opts)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) {
				if res.Pending != nil {
					renderPendingSubmit(a.out, res.Pending)
					return
				}
				renderSendResult(a.out, res.Sent)
			})
			// A submission core is still retrying is NOT a failure — re-running it
			// is the one thing that could send twice, so it must not exit non-zero
			// and invite exactly that.
			if res.Sent != nil {
				for _, r := range res.Sent.Recipients {
					if r.Status == "failed" {
						return silentExit(1)
					}
				}
			}
			return nil
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID; pass the same one to retry safely)")
	cmd.Flags().BoolVar(&bounce, "bounce", true, "deliver a DSN on terminal relay failure (--bounce=false to opt out)")
	return cmd
}

// renderPendingSubmit reports the 202: core kept the submission and is retrying
// it. The message a user needs here is "do not run this again", since the
// natural reading of anything short of "sent" is to retry.
func renderPendingSubmit(p *Printer, s *coreapi.ScheduledSend) {
	p.Warnf("accepted and still sending (%s) — delivery %s", s.State, s.DeliveryID)
	for _, leg := range s.Result {
		line := "  " + leg.Address + ": " + leg.Status
		if leg.Error != "" {
			line += " (" + leg.Error + ")"
		}
		p.Msgf("%s", line)
	}
	if len(s.Result) == 0 {
		for _, addr := range s.Recipients {
			p.Msgf("  %s: pending", addr)
		}
	}
	p.Msgf("")
	p.Msgf("core is retrying this itself — do not submit it again.")
}
