package cli

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// tableGap is the number of spaces between columns.
const tableGap = 2

// ansiSeq matches SGR color escapes. Column widths are measured on the VISIBLE
// text (escapes stripped) so colored cells — e.g. the dimmed header — align with
// plain ones. text/tabwriter can't do this: it counts the invisible color bytes
// as width, so every column drifted by the length of the header's dim codes.
var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// ansiEscape matches ANSI/VT escape sequences — CSI (\x1b[ … final), OSC
// (\x1b] … BEL/ST), and the two-byte ESC-Fe forms — for stripping control
// sequences out of untrusted cell data (a decoded Subject from inbound mail
// could otherwise clear the screen, spoof the terminal title, or move the
// cursor, and would desync every following column). CSI is listed first so
// "\x1b[" prefers it over the bare ESC-Fe alternative.
var ansiEscape = regexp.MustCompile("\x1b(?:\\[[0-9;:<=>?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[@-_])")

// visibleWidth is the on-screen rune count of s, ignoring color escapes.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiSeq.ReplaceAllString(s, ""))
}

// isCtl reports whether r is a C0/DEL/C1 control code point.
func isCtl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) }

// sanitizeCell neutralizes untrusted control data before it reaches a terminal —
// a table cell holding a decoded Subject/From from inbound mail, or any other
// string this CLI did not write (the login console's key name, its one-time
// code) — so it cannot inject escape sequences or desync column widths. It strips ANSI/VT escape sequences and every
// C0/DEL/C1 control rune (including tab/newline, which would break the layout).
// Table cells never legitimately carry ANSI — styling is applied by the renderer
// — so this is loss-free for well-formed data.
func sanitizeCell(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 && strings.IndexFunc(s, isCtl) < 0 {
		return s // fast path: nothing to strip
	}
	s = ansiEscape.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if isCtl(r) {
			return -1
		}
		return r
	}, s)
}

// sanitizeBody strips ANSI escape sequences and unsafe control codes from untrusted
// multiline message bodies while preserving safe whitespace formatting (\n and \t).
func sanitizeBody(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 && strings.IndexFunc(s, func(r rune) bool {
		return r != '\n' && r != '\t' && isCtl(r)
	}) < 0 {
		return s
	}
	s = ansiEscape.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if isCtl(r) {
			return -1
		}
		return r
	}, s)
}

// sanitizeCells returns a copy of cells with sanitizeCell applied to each.
func sanitizeCells(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = sanitizeCell(c)
	}
	return out
}

// printTable renders headers + rows aligned. Headers are dimmed when color is on.
// Every cell is sanitized first (see sanitizeCell) so untrusted values can't
// inject terminal escapes or throw off column alignment.
func printTable(w io.Writer, p *Printer, headers []string, rows [][]string) {
	headers = sanitizeCells(headers)
	if len(rows) > 0 {
		clean := make([][]string, len(rows))
		for i, r := range rows {
			clean[i] = sanitizeCells(r)
		}
		rows = clean
	}
	cols := len(headers)
	widths := make([]int, cols)
	measure := func(cells []string) {
		for i := 0; i < cols && i < len(cells); i++ {
			if n := visibleWidth(cells[i]); n > widths[i] {
				widths[i] = n
			}
		}
	}
	measure(headers)
	for _, r := range rows {
		measure(r)
	}

	writeRow := func(cells []string, header bool) {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if header {
				b.WriteString(p.Dim(cell))
			} else {
				b.WriteString(cell)
			}
			if i < cols-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)+tableGap))
			}
		}
		fmt.Fprintln(w, b.String())
	}
	writeRow(headers, true)
	for _, r := range rows {
		writeRow(r, false)
	}
}

// fmtEpoch renders epoch seconds as a local timestamp.
func fmtEpoch(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04")
}

// fmtEpochPtr renders a nullable epoch.
func fmtEpochPtr(sec *int64) string {
	if sec == nil {
		return "—"
	}
	return fmtEpoch(*sec)
}

// fmtQuota renders a nullable byte quota (null = unlimited).
func fmtQuota(b *int64) string {
	if b == nil {
		return "unlimited"
	}
	return fmtBytes(*b)
}

// fmtSendCap renders a send-allowance cap, which has THREE states where a
// quota has two — and conflating any pair of them misreports the bound:
//
//	nil → no override on this mailbox, so the platform default applies. NOT
//	      the same as unlimited, and reading it that way is the dangerous
//	      direction (it reports a bounded mailbox as unbounded).
//	0   → explicitly unlimited, deliberately asked for.
//	n   → that many per rolling 24h.
//
// The platform default is not visible from a mailbox row — `send-usage`
// reports the number actually in force.
func fmtSendCap(n *int64) string {
	if n == nil {
		return "platform default"
	}
	if *n == 0 {
		return "unlimited"
	}
	return strconv.FormatInt(*n, 10)
}

// fmtSendState renders the send freeze or hold. Spelled as a state rather than
// a yes/no because "Sending: no" reads like a capability the mailbox never had,
// while both of these are things an operator DID and can undo.
//
// The two modes are named apart because their effect on QUEUED mail differs: a
// freeze bounces it, a hold keeps it. DISABLED (core's word for the freeze) is
// reported first, since core resolves a row carrying both toward the permanent
// answer.
func fmtSendState(disabled, paused bool) string {
	switch {
	case disabled:
		return "DISABLED (submissions refused permanently; queued mail bounced at the relay)"
	case paused:
		return "PAUSED (submissions deferred; queued mail held, not bounced)"
	}
	return "enabled"
}

// fmtSendLimit renders a limit already resolved to what is IN FORCE (the
// send-usage read), where null means the axis is not enforced at all.
func fmtSendLimit(n *int64) string {
	if n == nil {
		return "not enforced"
	}
	return strconv.FormatInt(*n, 10)
}

// boolYN renders a bool as yes/no.
func boolYN(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// requiredDestFlag returns the flag a destination type must set, or "" for a
// type (group) that carries no destination fields.
func requiredDestFlag(typ string) string {
	switch typ {
	case "mailbox":
		return "mailbox"
	case "webhook":
		return "webhook-url"
	case "remote":
		return "remote"
	case "alias":
		return "alias"
	}
	return ""
}

// boolPtrIfChanged returns &v when the named flag was set, else nil — for
// building partial patch bodies where "unset" must differ from "false".
func boolPtrIfChanged(cmd *cobra.Command, name string, v bool) *bool {
	if cmd.Flags().Changed(name) {
		return &v
	}
	return nil
}

// fmtBytes renders a byte count in IEC units.
func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
