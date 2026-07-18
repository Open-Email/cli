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

// visibleWidth is the on-screen rune count of s, ignoring color escapes.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiSeq.ReplaceAllString(s, ""))
}

// printTable renders headers + rows aligned. Headers are dimmed when color is on.
func printTable(w io.Writer, p *Printer, headers []string, rows [][]string) {
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
