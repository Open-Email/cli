package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Printer renders command output. stdout carries data (the pipeable thing);
// stderr carries messages, prompts, warnings. --json switches data to machine
// JSON. Color is emitted only on a TTY with NO_COLOR unset and --no-color off.
type Printer struct {
	json  bool
	color bool
	out   io.Writer // stdout
	err   io.Writer // stderr
}

func newPrinter(jsonMode, noColor bool) *Printer {
	color := !noColor && os.Getenv("NO_COLOR") == "" &&
		os.Getenv("TERM") != "dumb" && term.IsTerminal(int(os.Stdout.Fd()))
	return &Printer{json: jsonMode, color: color, out: os.Stdout, err: os.Stderr}
}

// JSON reports whether machine output is selected.
func (p *Printer) JSON() bool { return p.json }

// Emit writes v as JSON when --json is set, otherwise invokes human to render a
// human view to stdout.
func (p *Printer) Emit(v any, human func(w io.Writer)) {
	if p.json {
		p.writeJSON(v)
		return
	}
	human(p.out)
}

func (p *Printer) writeJSON(v any) {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// EmitRaw emits core's exact response JSON (pretty-printed) in --json mode, else
// invokes human. Used where a typed struct would lose union/null fidelity (the
// delivery trichotomy: duplicate:false, uid:null, etc.).
func (p *Printer) EmitRaw(raw []byte, human func(w io.Writer)) {
	if p.json {
		var buf bytes.Buffer
		if json.Indent(&buf, raw, "", "  ") == nil {
			buf.WriteByte('\n')
			_, _ = p.out.Write(buf.Bytes())
		} else {
			_, _ = p.out.Write(raw)
		}
		return
	}
	human(p.out)
}

// Msgf writes an informational line to stderr.
func (p *Printer) Msgf(format string, a ...any) {
	fmt.Fprintf(p.err, format+"\n", a...)
}

// Successf writes a success line (✓) to stderr.
func (p *Printer) Successf(format string, a ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.paint(colGreen, "✓"), fmt.Sprintf(format, a...))
}

// Warnf writes a warning line to stderr.
func (p *Printer) Warnf(format string, a ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.paint(colYellow, "warning:"), fmt.Sprintf(format, a...))
}

// ANSI colors, applied only when p.color.
const (
	colReset  = "\x1b[0m"
	colBold   = "\x1b[1m"
	colDim    = "\x1b[2m"
	colRed    = "\x1b[31m"
	colGreen  = "\x1b[32m"
	colYellow = "\x1b[33m"
	colCyan   = "\x1b[36m"
)

func (p *Printer) paint(code, s string) string {
	if !p.color {
		return s
	}
	return code + s + colReset
}

// Bold / Dim helpers for human renderers.
func (p *Printer) Bold(s string) string   { return p.paint(colBold, s) }
func (p *Printer) Dim(s string) string    { return p.paint(colDim, s) }
func (p *Printer) Cyan(s string) string   { return p.paint(colCyan, s) }
func (p *Printer) Green(s string) string  { return p.paint(colGreen, s) }
func (p *Printer) Yellow(s string) string { return p.paint(colYellow, s) }
func (p *Printer) Red(s string) string    { return p.paint(colRed, s) }

// promptTTY reports whether we can interactively prompt (stdin + stderr TTYs).
func promptTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// dashIfNil renders a *string as its value or "—".
func strOr(p *string, dash string) string {
	if p == nil || *p == "" {
		return dash
	}
	return *p
}

func int64Or(p *int64, dash string) string {
	if p == nil {
		return dash
	}
	return fmt.Sprintf("%d", *p)
}

// indentLines prefixes every non-empty line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
