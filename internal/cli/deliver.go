package cli

import (
	"errors"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
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
		Short: "RCPT-time recipient pre-flight (200 accepted / 404 unknown / 403 receiving_disabled|sender_blocked)",
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
				// 403 carries two unrelated meanings and core's own contract says
				// to split them by ERROR CODE, never by status: insufficient_scope
				// is a refusal to answer at all (this credential may not ask), not
				// a fact about anyone's mailbox. Rendering it as accepted:false
				// would report a refusal nobody ever pronounced — the same mistake
				// that makes an MTA 550 mail for its own misconfiguration.
				if coreapi.IsInsufficientScope(err) {
					a.out.Warnf("%s", insufficientScopeHint())
					return err
				}
				// A rejection is a normal outcome; render it and exit non-zero.
				// sender_blocked is the third meaning of 403 (an address-list rule
				// refused this MAIL FROM, so the verdict depends on --from) and is
				// as much a verdict as the other two.
				switch code := coreapi.Code(err); code {
				case "unknown_address", "receiving_disabled", "sender_blocked":
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
	cmd.Flags().StringVar(&from, "from", "", "SMTP MAIL FROM — without it core cannot run the address-list sender gate (sender_blocked)")
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

// insufficientScopeHint says which credential to reach for when core refuses to
// run the RCPT gate at all.
//
// The wording is load-bearing, which is why it is a function with a test rather
// than a string literal inside the handler. Core refuses ONLY mailbox
// principals here — `DeliverCheck.handle` returns insufficient_scope for
// `principal.type === "mailbox"` and nothing else, and `checkRecipient` is
// typed to exclude that one type. An ACCOUNT key runs the gate fine, scoped to
// its own domains: a recipient outside them collapses to 404 rather than 403.
//
// So the obvious phrasing — "needs a system key" — is wrong in the expensive
// direction. It sends an operator after the one credential most of them cannot
// get, to fix a problem their existing account key already solves, and it
// quietly teaches that the RCPT gate is operator-only when it is not.
func insufficientScopeHint() string {
	return "this credential may not run the RCPT gate — a mailbox app password cannot; " +
		"use an account or system key. No recipient verdict was reached"
}
