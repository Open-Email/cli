package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"
	"time"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newSendCmd(a *app) *cobra.Command {
	var (
		from, to, subject, text, bodyFile string
		file, envFrom, envTo, sourceHost  string
		meta, deliveryID                  string
		save, bounce                      bool
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Submit an outbound message (raw MIME from --file/stdin, or compose from --from/--to/--subject)",
		Long: "Submit one outbound message to a single recipient.\n\n" +
			"Raw mode:     openemail send --file msg.eml --from me@x --to you@y\n" +
			"              cat msg.eml | openemail send --from me@x --to you@y\n" +
			"Compose mode: openemail send --from me@x --to you@y --subject Hi --text 'Hello'\n\n" +
			"Compose mode is entered when --subject/--text/--body-file is set. The envelope\n" +
			"defaults to --from/--to; override with --envelope-from/--envelope-to.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			compose := subject != "" || text != "" || bodyFile != ""

			// Envelope resolution: explicit overrides, else --from/--to.
			ef := firstNonEmpty(envFrom, from)
			et := firstNonEmpty(envTo, to)
			if ef == "" || et == "" {
				return usageError(errors.New("need a sender and recipient — pass --from/--to (or --envelope-from/--envelope-to)"))
			}

			var getBody func() (io.ReadCloser, error)
			var length int64
			var cleanup func() = func() {}

			if compose {
				if from == "" || to == "" {
					return usageError(errors.New("compose mode needs --from and --to"))
				}
				body := text
				if bodyFile != "" {
					b, rerr := os.ReadFile(bodyFile)
					if rerr != nil {
						return rerr
					}
					body = string(b)
				}
				msg := composeMIME(from, to, subject, body)
				getBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(msg)), nil }
				length = int64(len(msg))
			} else {
				getBody, length, cleanup, err = openRawBody(file)
				if err != nil {
					return err
				}
			}
			defer cleanup()

			opts := coreapi.OutboundOptions{
				EnvelopeFrom: ef, EnvelopeTo: et, SourceHost: sourceHost, DeliveryMeta: meta,
				DeliveryID: firstNonEmpty(deliveryID, newDeliveryID()),
			}
			// Only send the strict save/bounce flags when the user set them.
			opts.Save = boolPtrIfChanged(cmd, "save", save)
			opts.Bounce = boolPtrIfChanged(cmd, "bounce", bounce)

			res, raw, err := client.SubmitOutbound(cmd.Context(), opts, getBody, length)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) { renderDeliverResult(a.out, res, true) })
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "sender address (From header + envelope from)")
	cmd.Flags().StringVar(&to, "to", "", "recipient address (To header + envelope to; exactly one)")
	cmd.Flags().StringVar(&subject, "subject", "", "subject (compose mode)")
	cmd.Flags().StringVar(&text, "text", "", "plain-text body (compose mode)")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read the plain-text body from this file (compose mode)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "raw MIME message file (raw mode; default stdin)")
	cmd.Flags().StringVar(&envFrom, "envelope-from", "", "override envelope MAIL FROM")
	cmd.Flags().StringVar(&envTo, "envelope-to", "", "override envelope RCPT TO")
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "X-Source-Host (stored in deliveryMeta)")
	cmd.Flags().StringVar(&meta, "meta", "", "X-Delivery-Meta JSON object (≤8KB)")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID; reused on retry)")
	cmd.Flags().BoolVar(&save, "save", false, "write a Sent copy in the sender's mailbox")
	cmd.Flags().BoolVar(&bounce, "bounce", false, "deliver a DSN on terminal relay failure")
	return cmd
}

// renderDeliverResult prints the delivery trichotomy to stderr. withSent enables
// the Sent-copy note (outbound only).
func renderDeliverResult(p *Printer, res *coreapi.DeliverResult, withSent bool) {
	switch res.Status {
	case "delivered":
		if res.Duplicate {
			p.Successf("delivered (duplicate — already stored) — message %s", res.MessageID)
		} else {
			p.Successf("delivered — message %s", res.MessageID)
		}
		if res.ThreadID != nil {
			p.Msgf("  thread %s", *res.ThreadID)
		}
	case "filtered":
		p.Successf("filtered by the recipient's Sieve script — nothing stored")
	case "queued":
		p.Successf("queued — webhook/remote/group/relay; final outcome appears in the traffic log")
	default:
		p.Successf("%s", res.Status)
	}
	if withSent && res.SentCopy != "" {
		p.Msgf("  sent copy: %s", res.SentCopy)
	}
}

// composeMIME builds a minimal RFC 5322 text/plain message.
func composeMIME(from, to, subject, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	if subject != "" {
		fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	}
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", newDeliveryID(), hostFromAddress(from))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	// Normalize line endings to CRLF.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

func hostFromAddress(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "openemail.local"
}
