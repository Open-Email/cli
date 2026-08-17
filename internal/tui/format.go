package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ansiEscape matches ANSI/VT escape sequences for stripping control sequences.
var ansiEscape = regexp.MustCompile("\x1b(?:\\[[0-9;:<=>?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[@-_])")

// isCtl reports whether r is a C0/DEL/C1 control code point.
func isCtl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) }

// sanitizePlain strips ANSI/VT escape sequences and unsafe control characters while preserving \n and \t.
func sanitizePlain(s string) string {
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

// Formatting mirrors the flag commands' human output (internal/cli/render.go)
// so the console and the CLI describe the platform in the same vocabulary.

func fmtEpoch(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).Local().Format("2006-01-02 15:04")
}

func fmtEpochPtr(sec *int64) string {
	if sec == nil {
		return "—"
	}
	return fmtEpoch(*sec)
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func strOr(p *string, dash string) string {
	if p == nil || *p == "" {
		return dash
	}
	return *p
}

// orDash renders a plain (non-pointer) string, substituting a dash when empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func int64Or(p *int64, dash string) string {
	if p == nil {
		return dash
	}
	return strconv.FormatInt(*p, 10)
}

// sendCapOr renders an allowance cap's THREE states. A nil pointer is
// "platform default", never "∞": on a quota those would mean the same thing,
// but here the platform number may be the tightest bound in play, so showing it
// as unlimited would tell an operator the opposite of the truth.
func sendCapOr(p *int64) string {
	if p == nil {
		return "platform default"
	}
	if *p == 0 {
		return "unlimited"
	}
	return strconv.FormatInt(*p, 10)
}

// storagePoolOr is sendCapOr for the account storage pool: same three states,
// but a byte count read as a raw integer is unreadable, so the configured case
// goes through fmtBytes. Kept apart rather than teaching sendCapOr a unit,
// because every other caller of that is a message or recipient COUNT.
func storagePoolOr(p *int64) string {
	if p == nil {
		return "platform default"
	}
	if *p == 0 {
		return "unlimited"
	}
	return fmtBytes(*p)
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "—"
}

// fmtInt renders a count with thousands separators (1204 → "1,204"). Used for
// traffic event counts, which read as tallies rather than sizes.
func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return neg + b.String()
}

// fmtMsgFlags compacts a message's flag set for a table cell, in canonical
// order regardless of input order. Core's vocabulary is lowercase bare names
// (seen | answered | flagged | draft | deleted): N new (no seen), A answered,
// F flagged, D draft, T deleted.
func fmtMsgFlags(flags []string) string {
	has := map[string]bool{}
	for _, f := range flags {
		has[strings.ToLower(f)] = true
	}
	var out []byte
	if !has["seen"] {
		out = append(out, 'N')
	}
	for _, m := range [...]struct {
		flag string
		code byte
	}{{"answered", 'A'}, {"flagged", 'F'}, {"draft", 'D'}, {"deleted", 'T'}} {
		if has[m.flag] {
			out = append(out, m.code)
		}
	}
	return string(out)
}

// parseBytes reads a human byte size ("500M", "1.5 GB", "1073741824") into
// bytes (base 1024). The empty string is the caller's case (usually
// "unlimited"), not this function's.
func parseBytes(s string) (int64, error) {
	t := strings.TrimSpace(strings.ToUpper(s))
	if t == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	t = strings.TrimSuffix(t, "B")
	switch {
	case strings.HasSuffix(t, "K"):
		mult, t = 1<<10, strings.TrimSuffix(t, "K")
	case strings.HasSuffix(t, "M"):
		mult, t = 1<<20, strings.TrimSuffix(t, "M")
	case strings.HasSuffix(t, "G"):
		mult, t = 1<<30, strings.TrimSuffix(t, "G")
	case strings.HasSuffix(t, "T"):
		mult, t = 1<<40, strings.TrimSuffix(t, "T")
	}
	t = strings.TrimSpace(t)
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("bad size %q (use e.g. 500M, 1.5G, or plain bytes)", s)
	}
	return int64(f * float64(mult)), nil
}

// truncate shortens s to n display runes with an ellipsis, sanitizing control sequences.
func truncate(s string, n int) string {
	s = sanitizePlain(s)
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// wrapPlain wraps plain text to width, preserving existing newlines and sanitizing ANSI codes. Long
// unbreakable words are hard-split. Used for message bodies only — table
// cells and detail values rely on truncation instead.
func wrapPlain(s string, width int) string {
	s = sanitizePlain(s)
	if width < 8 {
		width = 8
	}
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(wrapLine(line, width))
	}
	return b.String()
}

func wrapLine(line string, width int) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	for _, w := range words {
		r := []rune(w)
		for len(r) > width { // hard-split an unbreakable run
			if col > 0 {
				b.WriteByte('\n')
				col = 0
			}
			b.WriteString(string(r[:width]))
			b.WriteByte('\n')
			r = r[width:]
		}
		wl := len(r)
		switch {
		case col == 0:
			b.WriteString(string(r))
			col = wl
		case col+1+wl <= width:
			b.WriteByte(' ')
			b.WriteString(string(r))
			col += 1 + wl
		default:
			b.WriteByte('\n')
			b.WriteString(string(r))
			col = wl
		}
	}
	return b.String()
}
