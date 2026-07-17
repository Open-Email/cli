package cli

import (
	"errors"
	"io"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newDeliverCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deliver",
		Short: "MTA-facing delivery: RCPT pre-flight check and test inbound injection",
	}
	cmd.AddCommand(
		newDeliverCheckCmd(a),
		newDeliverInboundCmd(a),
	)
	return cmd
}

func newDeliverCheckCmd(a *app) *cobra.Command {
	var to, from, ip, ptr, helo string
	cmd := &cobra.Command{
		Use:   "check --to <address>",
		Short: "RCPT-time recipient pre-flight (200 accepted / 404 unknown / 403 receiving_disabled)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if to == "" {
				return usageError(errors.New("--to is required"))
			}
			accepted, err := client.CheckRecipient(cmd.Context(), to, from, ip, ptr, helo)
			if err != nil {
				// A rejection is a normal outcome; render it and exit non-zero.
				if code := coreapi.Code(err); code == "unknown_address" || code == "receiving_disabled" {
					a.out.Emit(map[string]any{"accepted": false, "reason": code}, func(w io.Writer) {
						a.out.Warnf("recipient %s rejected: %s", to, code)
					})
					return silentExit(1)
				}
				return err
			}
			a.out.Emit(map[string]any{"accepted": accepted}, func(w io.Writer) {
				a.out.Successf("recipient %s accepted", to)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "recipient address to check (required)")
	cmd.Flags().StringVar(&from, "from", "", "SMTP MAIL FROM (advisory; currently unused by core)")
	cmd.Flags().StringVar(&ip, "ip", "", "connecting client IP (advisory)")
	cmd.Flags().StringVar(&ptr, "ptr", "", "client reverse-DNS (advisory)")
	cmd.Flags().StringVar(&helo, "helo", "", "client HELO/EHLO hostname (advisory)")
	return cmd
}

func newDeliverInboundCmd(a *app) *cobra.Command {
	var file, from, to, sourceHost, meta, deliveryID string
	cmd := &cobra.Command{
		Use:   "inbound --from <env-from> --to <env-to>",
		Short: "Inject a raw inbound message through the routing ladder (from --file/stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if to == "" {
				return usageError(errors.New("--to (envelope recipient) is required"))
			}
			getBody, length, cleanup, err := openRawBody(file)
			if err != nil {
				return err
			}
			defer cleanup()
			opts := coreapi.InboundOptions{
				EnvelopeFrom: from, EnvelopeTo: to, SourceHost: sourceHost, DeliveryMeta: meta,
				DeliveryID: firstNonEmpty(deliveryID, newDeliveryID()),
			}
			res, raw, err := client.DeliverInbound(cmd.Context(), opts, getBody, length)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) { renderDeliverResult(a.out, res, false) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "raw MIME message file (default: stdin)")
	cmd.Flags().StringVar(&from, "from", "", "envelope MAIL FROM (empty = null sender, allowed inbound)")
	cmd.Flags().StringVar(&to, "to", "", "envelope RCPT TO (required)")
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "X-Source-Host (stored in deliveryMeta)")
	cmd.Flags().StringVar(&meta, "meta", "", "X-Delivery-Meta JSON object (≤8KB)")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID)")
	return cmd
}
