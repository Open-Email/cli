package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/openemail/openemail-cli/internal/coreapi"
)

// validFlags is the message-flag vocabulary core accepts (STORE and APPEND).
var validFlags = map[string]bool{
	"seen": true, "answered": true, "flagged": true, "draft": true, "deleted": true,
}

// validateFlags rejects any flag name outside the five-name set (a client-side
// nicety; core also answers 400 invalid_flag).
func validateFlags(flags []string) error {
	for _, f := range flags {
		if !validFlags[strings.ToLower(f)] {
			return fmt.Errorf("invalid flag %q: expected one of seen, answered, flagged, draft, deleted", f)
		}
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
