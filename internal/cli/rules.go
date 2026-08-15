package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newRulesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rules",
		Aliases: []string{"rule"},
		Short:   "Manage a mailbox's filter rules (the simple alternative to writing Sieve)",
		Long: "Manage a mailbox's filter rules: a flat, ordered list evaluated top to bottom,\n" +
			"compiled to Sieve and run by the same engine hand-written scripts use.\n\n" +
			"Rules and hand-written Sieve are two authoring interfaces over ONE active\n" +
			"filter: activating rules deactivates an active hand-written script, and\n" +
			"`openemail sieve activate <name>` deactivates the rules. Last action wins.",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newRulesListCmd(a),
		newRulesGetCmd(a),
		newRulesPutCmd(a),
		newRulesDeleteCmd(a),
		newRulesAddCmd(a),
		newRulesRemoveCmd(a),
		newRulesToggleCmd(a, true),
		newRulesToggleCmd(a, false),
		newRulesMoveCmd(a),
		newRulesScriptCmd(a),
	)
	return cmd
}

// rulesState fetches the current document, mapping a 404 (no rules yet) to an
// empty in-memory document so the edit helpers can create the first rule.
func rulesState(cmd *cobra.Command, client *coreapi.Client, mbx string) (*coreapi.FilterRulesState, error) {
	st, err := client.GetRules(cmd.Context(), mbx)
	if err == nil {
		return st, nil
	}
	if coreapi.IsNotFound(err) {
		return &coreapi.FilterRulesState{Rules: []coreapi.FilterRule{}, Status: "inactive"}, nil
	}
	return nil, err
}

func newRulesListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the mailbox's filter rules in evaluation order",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				if coreapi.IsNotFound(err) {
					a.out.Emit(map[string]any{"rules": []coreapi.FilterRule{}, "status": "none"}, func(w io.Writer) {
						a.out.Msgf("no filter rules — create one with `openemail rules add`")
					})
					return nil
				}
				return err
			}
			a.out.Emit(st, func(w io.Writer) {
				rows := make([][]string, 0, len(st.Rules))
				for i, r := range st.Rules {
					rows = append(rows, []string{
						strconv.Itoa(i + 1), ruleNameDisplay(r, i), ruleOnOff(r),
						ruleMatchDisplay(r), summarizeConditions(r.Conditions),
						summarizeActions(r.Actions), boolYN(r.Stop),
					})
				}
				printTable(w, a.out, []string{"#", "NAME", "STATE", "MATCH", "IF", "THEN", "STOP"}, rows)
				printRulesStatus(a, st)
			})
			return nil
		},
	}
}

// printRulesStatus explains, in one line, whether these rules actually filter
// mail — the question a rules listing must never leave ambiguous.
func printRulesStatus(a *app, st *coreapi.FilterRulesState) {
	switch {
	case st.Status == "active":
		a.out.Msgf("these rules are the active filter")
	case st.ActiveScript != nil:
		a.out.Warnf("NOT active — the hand-written script %q is the active filter; `openemail rules put`/edits re-activate these", *st.ActiveScript)
	default:
		a.out.Warnf("NOT active — this mailbox has no active filter (delivery is unfiltered)")
	}
}

func newRulesGetCmd(a *app) *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Print the rules document as JSON (round-trips through `rules put`)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			doc, err := json.MarshalIndent(coreapi.RulesDocument{Rules: st.Rules}, "", "  ")
			if err != nil {
				return err
			}
			doc = append(doc, '\n')
			if outPath != "" {
				if err := os.WriteFile(outPath, doc, 0o600); err != nil {
					return err
				}
				a.out.Successf("Wrote %s", outPath)
				return nil
			}
			os.Stdout.Write(doc)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write the document to a file instead of stdout")
	return cmd
}

func newRulesPutCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "put",
		Short: "Replace the whole rules document from JSON and make it the active filter",
		Long: "Replace the whole rules document (from --file or stdin) and activate it.\n" +
			"The body is {\"rules\": [...]}, as `openemail rules get` prints it.\n" +
			"An empty list is legal: rules mode with no rules yet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			raw, err := readRawInput(file)
			if err != nil {
				return err
			}
			doc, err := parseRulesDocument(raw)
			if err != nil {
				return usageError(err)
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			res, err := client.PutRules(cmd.Context(), mbx, *doc)
			if err != nil {
				return err
			}
			emitRulesPut(a, res, fmt.Sprintf("%d rule(s) saved and activated", len(res.Rules)))
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the JSON document from a file (default: stdin)")
	return cmd
}

// parseRulesDocument accepts either the full {"rules": [...]} envelope or a
// bare array of rules — a plain array is what a user editing the list by hand
// most often ends up with.
func parseRulesDocument(raw []byte) (*coreapi.RulesDocument, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty input: expected a rules document")
	}
	if strings.HasPrefix(trimmed, "[") {
		var rules []coreapi.FilterRule
		if err := json.Unmarshal([]byte(trimmed), &rules); err != nil {
			return nil, fmt.Errorf("invalid rules JSON: %w", err)
		}
		return &coreapi.RulesDocument{Rules: rules}, nil
	}
	var doc coreapi.RulesDocument
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("invalid rules JSON: %w", err)
	}
	if doc.Rules == nil {
		return nil, errors.New(`document has no "rules" array`)
	}
	return &doc, nil
}

func emitRulesPut(a *app, res *coreapi.FilterRulesPutResult, msg string) {
	a.out.Emit(res, func(w io.Writer) {
		a.out.Successf("%s", msg)
		a.out.Msgf("these rules are now the active filter (any hand-written script was deactivated)")
	})
}

func newRulesDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete",
		Aliases: []string{"rm"},
		Short:   "Delete the rules document (leaves the mailbox with no active filter)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm("Delete all filter rules? If they are the active filter, delivery becomes unfiltered.") {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			res, err := client.DeleteRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Filter rules deleted")
				a.out.Msgf("hand-written Sieve scripts are untouched — `openemail sieve scripts list`")
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newRulesAddCmd(a *app) *cobra.Command {
	var (
		name, match    string
		ifs, thens     []string
		stop, disabled bool
		position       int
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Append a rule built from --if/--then conditions (read-modify-write)",
		Long: "Append a rule to the document and re-activate it.\n\n" +
			"Conditions (--if, repeatable):\n" +
			"  from:contains:@acme.com      subject:is:Invoice      listId:exists\n" +
			"  header:X-Spam:contains:yes   body:contains:urgent    size:over:5000000\n" +
			"  Fields: from to cc toOrCc subject listId header body size\n" +
			"  Ops:    contains is matches regex exists (body: contains; size: over|under)\n" +
			"  Prefix a condition with ! to invert it: '!from:contains:@acme.com'\n\n" +
			"Actions (--then, repeatable):\n" +
			"  label:Work        flag:seen|answered|flagged\n" +
			"  redirect:a@b.com  redirect-copy:a@b.com        discard",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rule, err := buildRule(name, match, ifs, thens, stop, disabled)
			if err != nil {
				return usageError(err)
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := rulesState(cmd, client, mbx)
			if err != nil {
				return err
			}
			rules := st.Rules
			idx := len(rules)
			if cmd.Flags().Changed("position") {
				if position < 1 || position > len(rules)+1 {
					return usageError(fmt.Errorf("--position %d out of range (1–%d)", position, len(rules)+1))
				}
				idx = position - 1
			}
			rules = append(rules, coreapi.FilterRule{})
			copy(rules[idx+1:], rules[idx:])
			rules[idx] = *rule
			res, err := client.PutRules(cmd.Context(), mbx, coreapi.RulesDocument{Rules: rules})
			if err != nil {
				return err
			}
			emitRulesPut(a, res, fmt.Sprintf("Rule added at position %d (%d total)", idx+1, len(res.Rules)))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display label for the rule")
	cmd.Flags().StringVar(&match, "match", "all", "all (every condition must match) | any")
	cmd.Flags().StringArrayVar(&ifs, "if", nil, "condition, repeatable — e.g. from:contains:@acme.com")
	cmd.Flags().StringArrayVar(&thens, "then", nil, "action, repeatable — e.g. label:Work")
	cmd.Flags().BoolVar(&stop, "stop", false, "stop evaluating later rules when this one matches")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "add the rule but leave it switched off")
	cmd.Flags().IntVar(&position, "position", 0, "insert at this 1-based position (default: append)")
	return cmd
}

func newRulesRemoveCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <position>",
		Short: "Remove the rule at a 1-based position (see `rules list`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			idx, err := rulePosition(args[0], len(st.Rules))
			if err != nil {
				return usageError(err)
			}
			victim := st.Rules[idx]
			if !yes && !confirm(fmt.Sprintf("Remove rule %d (%s)?", idx+1, ruleNameDisplay(victim, idx))) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			rules := append(append([]coreapi.FilterRule{}, st.Rules[:idx]...), st.Rules[idx+1:]...)
			res, err := client.PutRules(cmd.Context(), mbx, coreapi.RulesDocument{Rules: rules})
			if err != nil {
				return err
			}
			emitRulesPut(a, res, fmt.Sprintf("Rule %d removed (%d left)", idx+1, len(res.Rules)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newRulesToggleCmd(a *app, enable bool) *cobra.Command {
	use, verb := "disable <position>", "disabled"
	short := "Switch a rule off without deleting it"
	if enable {
		use, verb = "enable <position>", "enabled"
		short = "Switch a rule back on"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			idx, err := rulePosition(args[0], len(st.Rules))
			if err != nil {
				return usageError(err)
			}
			rules := append([]coreapi.FilterRule{}, st.Rules...)
			on := enable
			rules[idx].Enabled = &on
			res, err := client.PutRules(cmd.Context(), mbx, coreapi.RulesDocument{Rules: rules})
			if err != nil {
				return err
			}
			emitRulesPut(a, res, fmt.Sprintf("Rule %d %s", idx+1, verb))
			return nil
		},
	}
}

func newRulesMoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "move <from> <to>",
		Short: "Reorder: move the rule at <from> to 1-based position <to>",
		Long: "Reorder the list. Order is meaning here — rules run top to bottom, so moving\n" +
			"a rule past a `stop` rule can change which rules ever run.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			from, err := rulePosition(args[0], len(st.Rules))
			if err != nil {
				return usageError(err)
			}
			to, err := rulePosition(args[1], len(st.Rules))
			if err != nil {
				return usageError(err)
			}
			rules := moveRule(st.Rules, from, to)
			res, err := client.PutRules(cmd.Context(), mbx, coreapi.RulesDocument{Rules: rules})
			if err != nil {
				return err
			}
			emitRulesPut(a, res, fmt.Sprintf("Rule moved from %d to %d", from+1, to+1))
			return nil
		},
	}
}

func newRulesScriptCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "script",
		Short: "Print the Sieve source the rules compile to (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			st, err := client.GetRules(cmd.Context(), mbx)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"script": st.Script, "status": st.Status}, func(w io.Writer) {
				fmt.Fprint(os.Stdout, st.Script)
				if !strings.HasSuffix(st.Script, "\n") {
					fmt.Fprintln(os.Stdout)
				}
			})
			return nil
		},
	}
}

// ── rule construction & rendering ────────────────────────────────────────────

// moveRule returns the list with the element at from relocated to to.
func moveRule(in []coreapi.FilterRule, from, to int) []coreapi.FilterRule {
	rules := append([]coreapi.FilterRule{}, in...)
	r := rules[from]
	rules = append(rules[:from], rules[from+1:]...)
	rest := append([]coreapi.FilterRule{}, rules[to:]...)
	return append(append(rules[:to], r), rest...)
}

// rulePosition parses and bounds-checks a 1-based position argument.
func rulePosition(arg string, n int) (int, error) {
	pos, err := strconv.Atoi(arg)
	if err != nil {
		return 0, fmt.Errorf("invalid position %q: expected a number (see `openemail rules list`)", arg)
	}
	if n == 0 {
		return 0, errors.New("this mailbox has no rules")
	}
	if pos < 1 || pos > n {
		return 0, fmt.Errorf("position %d out of range (1–%d)", pos, n)
	}
	return pos - 1, nil
}

// ruleTextOps are the operators a text-valued condition accepts.
var ruleTextOps = map[string]bool{"contains": true, "is": true, "matches": true, "regex": true, "exists": true}

// ruleTextFields are the fields whose value is a string.
var ruleTextFields = map[string]bool{
	"from": true, "to": true, "cc": true, "toOrCc": true, "subject": true, "listId": true,
}

// buildRule assembles one rule from the --if/--then flag forms.
func buildRule(name, match string, ifs, thens []string, stop, disabled bool) (*coreapi.FilterRule, error) {
	if match != "all" && match != "any" {
		return nil, fmt.Errorf("invalid --match %q: expected all or any", match)
	}
	if len(ifs) == 0 {
		return nil, errors.New("a rule needs at least one --if condition")
	}
	if len(thens) == 0 {
		return nil, errors.New("a rule needs at least one --then action")
	}
	rule := &coreapi.FilterRule{Name: name, Match: match, Stop: stop}
	for _, spec := range ifs {
		cond, err := parseCondition(spec)
		if err != nil {
			return nil, err
		}
		rule.Conditions = append(rule.Conditions, *cond)
	}
	for _, spec := range thens {
		act, err := parseAction(spec)
		if err != nil {
			return nil, err
		}
		rule.Actions = append(rule.Actions, *act)
	}
	if disabled {
		off := false
		rule.Enabled = &off
	}
	return rule, nil
}

// parseCondition parses `[!]field:op[:value]`, plus the header form
// `header:<Name>:op[:value]`. The value may itself contain colons, so each
// form splits only as far as its fixed prefix.
func parseCondition(spec string) (*coreapi.RuleCondition, error) {
	c := coreapi.RuleCondition{}
	if strings.HasPrefix(spec, "!") {
		c.Not = true
		spec = spec[1:]
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid --if %q: expected field:op[:value]", spec)
	}
	c.Field = parts[0]
	rest := parts[1]

	if c.Field == "header" {
		hp := strings.SplitN(rest, ":", 3)
		if len(hp) < 2 {
			return nil, fmt.Errorf("invalid --if %q: expected header:<Name>:op[:value]", spec)
		}
		c.Header, c.Op = hp[0], hp[1]
		if len(hp) == 3 {
			c.Value = hp[2]
		}
	} else {
		op := strings.SplitN(rest, ":", 2)
		c.Op = op[0]
		if len(op) == 2 {
			c.Value = op[1]
		}
	}

	switch c.Field {
	case "size":
		if c.Op != "over" && c.Op != "under" {
			return nil, fmt.Errorf("invalid --if %q: size takes over or under", spec)
		}
		s, ok := c.Value.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("invalid --if %q: size needs a byte count", spec)
		}
		n, err := parseByteSize(s)
		if err != nil {
			return nil, fmt.Errorf("invalid --if %q: %w", spec, err)
		}
		c.Value = n
	case "body":
		if c.Op != "contains" {
			return nil, fmt.Errorf("invalid --if %q: body takes contains only", spec)
		}
		if s, _ := c.Value.(string); s == "" {
			return nil, fmt.Errorf("invalid --if %q: body:contains needs a value", spec)
		}
	case "header":
		if !ruleTextOps[c.Op] {
			return nil, fmt.Errorf("invalid --if %q: unknown op %q", spec, c.Op)
		}
	default:
		if !ruleTextFields[c.Field] {
			return nil, fmt.Errorf("invalid --if %q: unknown field %q (from to cc toOrCc subject listId header body size)", spec, c.Field)
		}
		if !ruleTextOps[c.Op] {
			return nil, fmt.Errorf("invalid --if %q: unknown op %q (contains is matches regex exists)", spec, c.Op)
		}
	}
	if c.Op == "exists" {
		c.Value = nil
	} else if c.Field != "size" {
		if s, _ := c.Value.(string); s == "" {
			return nil, fmt.Errorf("invalid --if %q: op %q needs a value", spec, c.Op)
		}
	}
	return &c, nil
}

// parseByteSize accepts a plain byte count or a k/m/g suffix.
func parseByteSize(s string) (int64, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, errors.New("size must not be negative")
		}
		return n, nil
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult = 1 << 10
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "M"):
		mult = 1 << 20
	case strings.HasSuffix(s, "g"), strings.HasSuffix(s, "G"):
		mult = 1 << 30
	default:
		return 0, fmt.Errorf("unparseable size %q (bytes, or a k/m/g suffix)", s)
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("unparseable size %q", s)
	}
	return n * mult, nil
}

// parseAction parses `label:<Name>`, `flag:<flag>`, `redirect[-copy]:<addr>`,
// or `discard`.
func parseAction(spec string) (*coreapi.RuleAction, error) {
	kind, rest, _ := strings.Cut(spec, ":")
	switch kind {
	case "label", "fileinto", "fileInto":
		if rest == "" {
			return nil, fmt.Errorf("invalid --then %q: label needs a name", spec)
		}
		return &coreapi.RuleAction{Type: "fileInto", Label: rest}, nil
	case "flag", "addflag", "addFlag":
		switch rest {
		case "seen", "answered", "flagged":
			return &coreapi.RuleAction{Type: "addFlag", Flag: rest}, nil
		}
		return nil, fmt.Errorf("invalid --then %q: flag must be seen, answered, or flagged", spec)
	case "redirect", "redirect-copy":
		if rest == "" {
			return nil, fmt.Errorf("invalid --then %q: redirect needs an address", spec)
		}
		return &coreapi.RuleAction{Type: "redirect", To: rest, KeepCopy: kind == "redirect-copy"}, nil
	case "discard":
		return &coreapi.RuleAction{Type: "discard"}, nil
	}
	return nil, fmt.Errorf("invalid --then %q: expected label:<name>, flag:<flag>, redirect[-copy]:<addr>, or discard", spec)
}

func ruleNameDisplay(r coreapi.FilterRule, idx int) string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return fmt.Sprintf("(rule %d)", idx+1)
}

func ruleOnOff(r coreapi.FilterRule) string {
	if r.IsEnabled() {
		return "on"
	}
	return "off"
}

func ruleMatchDisplay(r coreapi.FilterRule) string {
	if r.Match == "" {
		return "all"
	}
	return r.Match
}

// summarizeConditions renders a rule's tests as one compact cell.
func summarizeConditions(cs []coreapi.RuleCondition) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		field := c.Field
		if c.Field == "header" && c.Header != "" {
			field = c.Header
		}
		s := field + " " + c.Op
		if c.Op != "exists" && c.Value != nil {
			s += " " + formatRuleValue(c.Value)
		}
		if c.Not {
			s = "NOT " + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// summarizeActions renders a rule's actions as one compact cell.
func summarizeActions(as []coreapi.RuleAction) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		switch a.Type {
		case "fileInto":
			parts = append(parts, "file into "+a.Label)
		case "addFlag":
			parts = append(parts, "flag "+a.Flag)
		case "redirect":
			s := "redirect to " + a.To
			if a.KeepCopy {
				s += " (keep copy)"
			}
			parts = append(parts, s)
		case "discard":
			parts = append(parts, "discard")
		default:
			parts = append(parts, a.Type)
		}
	}
	return strings.Join(parts, ", ")
}

// formatRuleValue renders a condition value, which is a string for every field
// but size (where JSON decoding yields a float64).
func formatRuleValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmtBytes(int64(t))
	case int64:
		return fmtBytes(t)
	}
	return fmt.Sprintf("%v", v)
}
