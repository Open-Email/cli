package cli

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// bodyFlags are the message-body inputs three verbs share — `compose` (send),
// `threads reply` (send, threaded) and `messages compose` (file without
// sending). They differ in envelope and destination, never in how a body,
// attachment or extra header is expressed, so that part lives here once.
type bodyFlags struct {
	text     string
	bodyFile string
	htmlBody string
	htmlFile string
	attach   []string
	header   []string
}

func (b *bodyFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&b.text, "text", "", "plain-text body")
	f.StringVar(&b.bodyFile, "body-file", "", "read the plain-text body from a file (- for stdin)")
	f.StringVar(&b.htmlBody, "html", "", "HTML body")
	f.StringVar(&b.htmlFile, "html-file", "", "read the HTML body from a file (- for stdin)")
	f.StringArrayVar(&b.attach, "attach", nil, "attach a file (staged via an upload), repeatable")
	f.StringArrayVar(&b.header, "header", nil, "extra header as \"Name: value\", repeatable")
}

// bodies resolves the text and HTML parts. require makes an empty message an
// error — true for anything that gets sent, since a body is the whole point.
func (b *bodyFlags) bodies(cmd *cobra.Command, require bool) (text, html *string, err error) {
	text, err = composeBody(b.text, b.bodyFile, cmd.Flags().Changed("text"))
	if err != nil {
		return nil, nil, err
	}
	html, err = composeBody(b.htmlBody, b.htmlFile, cmd.Flags().Changed("html"))
	if err != nil {
		return nil, nil, err
	}
	if require && text == nil && html == nil {
		return nil, nil, usageError(errors.New("need a body — pass --text, --body-file, --html, or --html-file"))
	}
	return text, html, nil
}

func (b *bodyFlags) headers() (map[string]string, error) {
	if len(b.header) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(b.header))
	for _, h := range b.header {
		if strings.ContainsAny(h, "\r\n") {
			return nil, usageError(fmt.Errorf("invalid --header %q: header cannot contain newlines", h))
		}
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, usageError(fmt.Errorf("invalid --header %q: expected Name: value", h))
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return out, nil
}

// stage uploads every --attach file. It runs before the message is submitted so
// a failed upload cannot leave a half-composed message sent.
func (b *bodyFlags) stage(cmd *cobra.Command, a *app, client *coreapi.Client, mailboxID string) ([]coreapi.SendAttachment, error) {
	var out []coreapi.SendAttachment
	for _, path := range b.attach {
		att, err := uploadAttachment(cmd, a, client, mailboxID, path)
		if err != nil {
			return nil, err
		}
		out = append(out, *att)
	}
	return out, nil
}

func newComposeCmd(a *app) *cobra.Command {
	var (
		body                     bodyFlags
		from, subject, inReplyTo string
		to, cc, bcc, replyTo     []string
		references               []string
		deliveryID               string
		save, bounce             bool
	)
	cmd := &cobra.Command{
		Use:     "compose",
		Aliases: []string{"mail"},
		Short:   "Send a message with attachments to one or more recipients (server builds the MIME)",
		Long: "Send a structured message: JSON in, the server assembles the RFC 5322 MIME and\n" +
			"answers with one result per recipient.\n\n" +
			"  openemail compose --from me@x --to you@y --subject Hi --text 'Hello'\n" +
			"  openemail compose --from me@x --to a@y --to b@y --cc c@z --attach report.pdf\n\n" +
			"Each --attach file is staged with an upload first, so attachment bytes never\n" +
			"ride inside the JSON body. Addresses take a bare address or \"Name <addr>\".\n\n" +
			"A send to several recipients can partly fail (exit 1, per-recipient table).\n" +
			"To retry ONLY the failures, re-run with the same --delivery-id the first\n" +
			"attempt reported: the recipients who already received it are not mailed again.\n\n" +
			"For raw RFC 5322 bytes you already hold, use `openemail send --file`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if from == "" {
				return usageError(errors.New("--from is required (it must resolve to the sending mailbox)"))
			}
			if len(to)+len(cc)+len(bcc) == 0 {
				return usageError(errors.New("need at least one recipient — pass --to, --cc, or --bcc"))
			}
			fromAddr, err := parseSendAddress(from)
			if err != nil {
				return usageError(fmt.Errorf("--from: %w", err))
			}
			req := coreapi.SendRequest{From: *fromAddr, Subject: subject, InReplyTo: inReplyTo, References: references}
			for _, spec := range [...]struct {
				flag string
				vals []string
				dst  *[]coreapi.SendAddress
			}{
				{"--to", to, &req.To}, {"--cc", cc, &req.Cc},
				{"--bcc", bcc, &req.Bcc}, {"--reply-to", replyTo, &req.ReplyTo},
			} {
				list, perr := parseSendAddresses(spec.vals)
				if perr != nil {
					return usageError(fmt.Errorf("%s: %w", spec.flag, perr))
				}
				*spec.dst = list
			}

			if req.Text, req.HTML, err = body.bodies(cmd, true); err != nil {
				return err
			}
			if req.Headers, err = body.headers(); err != nil {
				return err
			}

			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if req.Attachments, err = body.stage(cmd, a, client, mbx); err != nil {
				return err
			}

			// The idempotency key is what makes the per-recipient result useful:
			// a 207 exits non-zero, the obvious next move is to re-run, and
			// without a key that re-run mails everyone who already succeeded a
			// second time. Defaulted to a fresh ULID so a single send is still
			// one call, and settable so a retry converges.
			opts := coreapi.SendOptions{DeliveryID: firstNonEmpty(deliveryID, newDeliveryID())}
			opts.Save = boolPtrIfChanged(cmd, "save", save)
			opts.Bounce = boolPtrIfChanged(cmd, "bounce", bounce)

			res, raw, err := client.SendMessage(cmd.Context(), mbx, req, opts)
			if err != nil {
				return err
			}
			a.out.EmitRaw(raw, func(w io.Writer) { renderSendResult(a.out, res) })
			// A partial failure is reported per recipient (207); exit non-zero so
			// a script notices without having to parse the table.
			for _, r := range res.Recipients {
				if r.Status == "failed" {
					return silentExit(1)
				}
			}
			return nil
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().StringVar(&from, "from", "", "sender: address or \"Name <addr>\" (must resolve to this mailbox)")
	cmd.Flags().StringArrayVar(&to, "to", nil, "recipient, repeatable")
	cmd.Flags().StringArrayVar(&cc, "cc", nil, "Cc recipient, repeatable")
	cmd.Flags().StringArrayVar(&bcc, "bcc", nil, "Bcc recipient, repeatable (appears only on your Sent copy)")
	cmd.Flags().StringArrayVar(&replyTo, "reply-to", nil, "Reply-To address, repeatable")
	cmd.Flags().StringVar(&subject, "subject", "", "subject")
	body.register(cmd)
	cmd.Flags().StringVar(&inReplyTo, "in-reply-to", "", "In-Reply-To message-id header")
	cmd.Flags().StringArrayVar(&references, "references", nil, "References message-id, repeatable")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "idempotency key (default: a fresh ULID; pass the same one to retry a partial send)")
	cmd.Flags().BoolVar(&save, "save", true, "write a Sent copy in the sender's mailbox")
	cmd.Flags().BoolVar(&bounce, "bounce", true, "deliver a DSN on terminal relay failure (--bounce=false to opt out)")
	return cmd
}

// composeBody resolves a body from its inline flag or its file (- = stdin).
// It returns nil when neither was given, so an unset body is omitted rather
// than sent as an empty string.
func composeBody(inline, file string, inlineSet bool) (*string, error) {
	if file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, err
		}
		s := string(data)
		return &s, nil
	}
	if inlineSet || inline != "" {
		s := inline
		return &s, nil
	}
	return nil, nil
}

// uploadAttachment stages one file and returns the attachment reference. The
// filename and content type are derived here so the recipient sees the file as
// it was named on disk.
func uploadAttachment(cmd *cobra.Command, a *app, client *coreapi.Client, mailboxID, path string) (*coreapi.SendAttachment, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a file", path)
	}
	name := filepath.Base(path)
	ctype := attachmentType(name)
	getBody := func() (io.ReadCloser, error) { return os.Open(path) }
	up, err := client.UploadBlob(cmd.Context(), mailboxID, getBody, st.Size(), ctype)
	if err != nil {
		return nil, fmt.Errorf("uploading %s: %w", name, err)
	}
	a.out.Msgf("staged %s (%s, %s)", name, fmtBytes(up.Size), up.BlobID)
	return &coreapi.SendAttachment{BlobID: up.BlobID, Filename: name}, nil
}

// attachmentType guesses a file's media type as a BARE type with no
// parameters. The parameters matter: mime.TypeByExtension answers
// "text/plain; charset=utf-8", the upload records whatever it is sent, and
// core's MIME builder then refuses that stored type (invalid_content_type) —
// it appends its own charset and rejects a caller-supplied one.
func attachmentType(name string) string {
	full := mime.TypeByExtension(filepath.Ext(name))
	if full == "" {
		return "application/octet-stream"
	}
	if base, _, err := mime.ParseMediaType(full); err == nil && base != "" {
		return base
	}
	if base, _, ok := strings.Cut(full, ";"); ok {
		return strings.TrimSpace(base)
	}
	return full
}

// parseSendAddresses parses a repeated address flag, also splitting on commas
// so `--to "a@x, b@y"` works the way a mail client's To field does.
func parseSendAddresses(vals []string) ([]coreapi.SendAddress, error) {
	var out []coreapi.SendAddress
	for _, v := range vals {
		for _, part := range splitAddressList(v) {
			addr, err := parseSendAddress(part)
			if err != nil {
				return nil, err
			}
			out = append(out, *addr)
		}
	}
	return out, nil
}

// splitAddressList splits on commas that are not inside a display name.
func splitAddressList(s string) []string {
	var parts []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '"':
			depth ^= 1
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				parts = append(parts, cur.String())
				cur.Reset()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseSendAddress accepts `addr` or `Name <addr>` (quoted display names too).
func parseSendAddress(s string) (*coreapi.SendAddress, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("address cannot contain newlines")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty address")
	}
	if i := strings.LastIndex(s, "<"); i >= 0 && strings.HasSuffix(s, ">") {
		addr := strings.TrimSpace(s[i+1 : len(s)-1])
		name := strings.TrimSpace(s[:i])
		name = strings.Trim(name, `"`)
		if addr == "" {
			return nil, fmt.Errorf("no address in %q", s)
		}
		if name == "" {
			return &coreapi.SendAddress{Address: addr}, nil
		}
		return &coreapi.SendAddress{Address: addr, Name: &name}, nil
	}
	if strings.ContainsAny(s, " <>") {
		return nil, fmt.Errorf("unparseable address %q (use `addr` or `Name <addr>`)", s)
	}
	return &coreapi.SendAddress{Address: s}, nil
}

// renderSendResult prints the per-recipient outcomes — the reason this route
// exists, so every recipient gets its own line rather than one summary verb.
func renderSendResult(p *Printer, res *coreapi.SendResult) {
	ok, failed := 0, 0
	for _, r := range res.Recipients {
		switch r.Status {
		case "failed":
			failed++
		default:
			ok++
		}
	}
	if failed == 0 {
		p.Successf("sent to %d recipient(s) — delivery %s", ok, res.DeliveryID)
	} else {
		p.Warnf("sent with %d failure(s) of %d recipient(s) — delivery %s", failed, len(res.Recipients), res.DeliveryID)
	}
	for _, r := range res.Recipients {
		line := "  " + r.Address + ": " + r.Status
		if r.Error != "" {
			line += " (" + r.Error + ")"
		}
		p.Msgf("%s", line)
	}
	if res.SentCopy != "" {
		note := res.SentCopy
		if res.SentCopy == "over_quota" {
			note = "over_quota — sending proceeded, but no Sent copy was stored"
		}
		p.Msgf("  sent copy: %s", note)
	}
}
