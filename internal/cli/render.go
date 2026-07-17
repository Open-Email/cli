package cli

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// printTable renders headers + rows aligned. Headers are dimmed when color is on.
func printTable(w io.Writer, p *Printer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	hdr := make([]string, len(headers))
	for i, h := range headers {
		hdr[i] = p.Dim(h)
	}
	fmt.Fprintln(tw, joinTab(hdr))
	for _, r := range rows {
		fmt.Fprintln(tw, joinTab(r))
	}
	tw.Flush()
}

func joinTab(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += "\t"
		}
		out += c
	}
	return out
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
