package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
)

// validFlags is the message-flag vocabulary core accepts (STORE and APPEND).
var validFlags = map[string]bool{
	"seen": true, "answered": true, "flagged": true, "draft": true, "deleted": true,
}

// normalizeFlags rejects any flag name outside the five-name set and, on success,
// lowercases each entry IN PLACE so the wire value matches core's case-sensitive
// enum. Without the lowercasing, `--set Seen` would pass this client-side check
// (which is case-insensitive) yet be rejected server-side with 400 invalid_flag.
func normalizeFlags(flags []string) error {
	for i, f := range flags {
		lower := strings.ToLower(f)
		if !validFlags[lower] {
			return fmt.Errorf("invalid flag %q: expected one of seen, answered, flagged, draft, deleted", f)
		}
		flags[i] = lower
	}
	return nil
}

func flagsDisplay(f []string) string {
	if len(f) == 0 {
		return "—"
	}
	return strings.Join(f, ",")
}

func labelNamesDisplay(m coreapi.MessageMeta) string {
	if len(m.Labels) == 0 {
		return "—"
	}
	names := make([]string, len(m.Labels))
	for i, l := range m.Labels {
		names[i] = l.Name
	}
	return strings.Join(names, ",")
}

func msgSubjectDisplay(m coreapi.MessageMeta) string {
	if m.Subject == nil || *m.Subject == "" {
		return "(no subject)"
	}
	return *m.Subject
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// messageListRows builds the standard message table rows.
func messageListRows(msgs []coreapi.MessageMeta) [][]string {
	rows := make([][]string, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, []string{
			m.ID,
			truncate(m.EnvelopeFrom, 28),
			truncate(msgSubjectDisplay(m), 40),
			fmtEpoch(m.ReceivedAt),
			flagsDisplay(m.Flags),
			truncate(labelNamesDisplay(m), 20),
		})
	}
	return rows
}

var messageListHeaders = []string{"ID", "FROM", "SUBJECT", "DATE", "FLAGS", "LABELS"}

// printMessageMeta renders a single message's metadata as a field table.
func printMessageMeta(w io.Writer, p *Printer, m *coreapi.MessageMeta) {
	rows := [][]string{
		{"ID", m.ID},
		{"From", m.EnvelopeFrom},
		{"To", m.EnvelopeTo},
		{"Subject", msgSubjectDisplay(*m)},
		{"Received", fmtEpoch(m.ReceivedAt)},
	}
	if m.SentAt != nil {
		rows = append(rows, []string{"Sent", fmtEpoch(*m.SentAt)})
	}
	rows = append(rows,
		[]string{"Size", fmtBytes(m.Size)},
		[]string{"Flags", flagsDisplay(m.Flags)},
		[]string{"Labels", labelMembershipDisplay(m)},
	)
	if m.ThreadID != nil && *m.ThreadID != "" {
		rows = append(rows, []string{"Thread", *m.ThreadID})
	}
	if m.MessageIDHeader != nil && *m.MessageIDHeader != "" {
		rows = append(rows, []string{"Message-ID", *m.MessageIDHeader})
	}
	if m.InReplyTo != nil && *m.InReplyTo != "" {
		rows = append(rows, []string{"In-Reply-To", *m.InReplyTo})
	}
	if m.Snippet != nil && *m.Snippet != "" {
		rows = append(rows, []string{"Snippet", truncate(*m.Snippet, 72)})
	}
	rows = append(rows, []string{"Blob", m.BlobHash + " (" + m.BlobGen + ")"})
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)
}

// fmtContentAddrs renders a header address-list as "Name <email>, …".
func fmtContentAddrs(addrs []coreapi.ContentAddress) string {
	if len(addrs) == 0 {
		return "—"
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		if a.Name != nil && *a.Name != "" {
			parts[i] = fmt.Sprintf("%s <%s>", *a.Name, a.Email)
		} else {
			parts[i] = a.Email
		}
	}
	return strings.Join(parts, ", ")
}

// bodySummary describes a body slot for the header table.
func bodySummary(b *coreapi.ContentBody) string {
	if b == nil {
		return "—"
	}
	if b.Truncated || b.Content == nil {
		return fmt.Sprintf("section %s, %s (truncated — fetch the part)", b.Section, fmtBytes(b.Size))
	}
	return fmt.Sprintf("section %s, %s", b.Section, fmtBytes(b.Size))
}

// printContent renders the structured content view (headers + bodies +
// attachments) in the FIELD/VALUE + table house style.
func printContent(w io.Writer, p *Printer, mbx, id string, c *coreapi.ContentResult) {
	h := c.Headers
	rows := [][]string{
		{"From", fmtContentAddrs(h.From)},
		{"To", fmtContentAddrs(h.To)},
	}
	if len(h.Cc) > 0 {
		rows = append(rows, []string{"Cc", fmtContentAddrs(h.Cc)})
	}
	if len(h.ReplyTo) > 0 {
		rows = append(rows, []string{"Reply-To", fmtContentAddrs(h.ReplyTo)})
	}
	rows = append(rows, []string{"Subject", derefOr(h.Subject, "(none)")})
	if h.Date != nil {
		rows = append(rows, []string{"Date", fmtEpoch(*h.Date)})
	}
	if h.MessageIDHeader != nil && *h.MessageIDHeader != "" {
		rows = append(rows, []string{"Message-ID", *h.MessageIDHeader})
	}
	if h.InReplyTo != nil && *h.InReplyTo != "" {
		rows = append(rows, []string{"In-Reply-To", *h.InReplyTo})
	}
	rows = append(rows,
		[]string{"Text", bodySummary(c.Text)},
		[]string{"HTML", bodySummary(c.HTML)},
	)
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)

	// Print the plain-text body in full when it is present and inlined.
	if c.Text != nil && !c.Text.Truncated && c.Text.Content != nil {
		fmt.Fprintf(w, "\n── Text (section %s) ──\n%s\n", c.Text.Section, strings.TrimRight(*c.Text.Content, "\r\n"))
	}

	if len(c.Attachments) > 0 {
		fmt.Fprintln(w)
		arows := make([][]string, 0, len(c.Attachments))
		for _, at := range c.Attachments {
			kind := "attachment"
			if at.Inline {
				kind = "inline"
			}
			if at.Degraded {
				kind += " (degraded)"
			}
			arows = append(arows, []string{at.Section, truncate(at.Filename, 32), at.ContentType, fmtBytes(at.Size), kind})
		}
		printTable(w, p, []string{"SECTION", "FILENAME", "TYPE", "SIZE", "KIND"}, arows)
		p.Msgf("  fetch a part:  openemail message part %s <section> -o <file>", id)
	}
}

// derefOr returns *s or a fallback when s is nil/empty.
func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// labelMembershipDisplay renders labels with their per-label UIDs.
func labelMembershipDisplay(m *coreapi.MessageMeta) string {
	if len(m.Labels) == 0 {
		return "—"
	}
	parts := make([]string, len(m.Labels))
	for i, l := range m.Labels {
		parts[i] = fmt.Sprintf("%s (uid %d)", l.Name, l.UID)
	}
	return strings.Join(parts, ", ")
}
