package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
)

// rulesDesc is the dynamic filter-rules listing for one mailbox: the flat
// ordered document, editable in place. Every mutation is a whole-document PUT
// (the document caps at 100 rules), which also RE-ACTIVATES the rules — the
// one-active-filter invariant core enforces, surfaced in the summary line.
//
// Rule authoring (conditions/actions) stays CLI-side: a form that builds an
// arbitrary condition list is a screen of its own, and the flag syntax is
// better suited to it. The console owns the operations a listing is good at —
// reorder, toggle, delete.
func rulesDesc(mbx coreapi.Mailbox) resourceDesc {
	label := mailboxLabel(&mbx)
	// state is captured by the descriptor so summary() can read what fetch
	// learned: whether these rules actually filter mail.
	st := &rulesScreenState{}
	return resourceDesc{
		key:  "rules:" + mbx.ID,
		name: "Filter rules — " + label,
		summary: func(w int) []string {
			return st.summaryLines(w)
		},
		columns: []column{
			{title: "#", width: 3},
			{title: "NAME", flex: true},
			{title: "STATE", width: 5},
			{title: "MATCH", width: 5},
			{title: "IF", flex: true},
			{title: "THEN", flex: true},
			{title: "STOP", width: 4},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			state, err := c.GetRules(ctx, mbx.ID)
			if err != nil {
				if coreapi.IsNotFound(err) {
					// No rules yet is an ordinary empty screen, not an error:
					// this is exactly where a user starts.
					st.set(nil)
					return nil, "", nil
				}
				return nil, "", err
			}
			st.set(state)
			rows := make([]rowData, len(state.Rules))
			for i, r := range state.Rules {
				rows[i] = rowData{
					cells: []string{
						fmt.Sprintf("%d", i+1), ruleName(r, i), ruleState(r),
						ruleMatch(r), ruleConditions(r.Conditions), ruleActions(r.Actions), yn(r.Stop),
					},
					item: ruleRow{rule: r, index: i},
				}
			}
			return rows, "", nil // the document is unpaginated (max 100)
		},
		detail: func(item any) []kv {
			rr := item.(ruleRow)
			r := rr.rule
			kvs := []kv{
				{k: "position", v: fmt.Sprintf("%d", rr.index+1)},
				{k: "name", v: ruleName(r, rr.index)},
				{k: "enabled", v: yn(r.IsEnabled())},
				{k: "match", v: ruleMatch(r) + " conditions must match"},
				{k: "stop", v: yn(r.Stop)},
				{},
				{v: "Conditions"},
			}
			for i, c := range r.Conditions {
				kvs = append(kvs, kv{k: fmt.Sprintf("%d", i+1), v: conditionLine(c)})
			}
			kvs = append(kvs, kv{}, kv{v: "Actions"})
			for i, a := range r.Actions {
				kvs = append(kvs, kv{k: fmt.Sprintf("%d", i+1), v: actionLine(a)})
			}
			return kvs
		},
		actions: []action{
			{key: "t", label: "t on/off", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				rr := item.(ruleRow)
				rules, err := currentRules(ctx, c, mbx.ID)
				if err != nil {
					return "", err
				}
				if rr.index >= len(rules) {
					return "", fmt.Errorf("rule %d no longer exists — refresh", rr.index+1)
				}
				on := !rules[rr.index].IsEnabled()
				rules[rr.index].Enabled = &on
				if _, err := c.PutRules(ctx, mbx.ID, coreapi.RulesDocument{Rules: rules}); err != nil {
					return "", err
				}
				verb := "disabled"
				if on {
					verb = "enabled"
				}
				return fmt.Sprintf("rule %d %s", rr.index+1, verb), nil
			}},
			{key: "K", label: "K up", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				return reorderRule(ctx, c, mbx.ID, item.(ruleRow), -1)
			}},
			{key: "J", label: "J down", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				return reorderRule(ctx, c, mbx.ID, item.(ruleRow), +1)
			}},
			{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return ruleDeleteConfirm(ctx, ui, mbx, item.(ruleRow))
			}},
			{key: "X", label: "X delete all", run: func(ctx context.Context, ui *Options, _ any) pane {
				return rulesDeleteAllConfirm(ctx, ui, mbx)
			}},
			// Sieve scripts live behind this screen rather than beside it: they
			// are the same concern (what filters mail) and core allows only one
			// active filter across both surfaces, so seeing them together is the
			// only way the "NOT ACTIVE" line above makes sense.
			{key: "s", label: "s scripts", run: func(ctx context.Context, ui *Options, _ any) pane {
				return newScreenPane(ctx, ui, sieveDesc(mbx))
			}},
		},
	}
}

// ruleRow pairs a rule with its position — the document is addressed by index,
// so a row must carry where it sits.
type ruleRow struct {
	rule  coreapi.FilterRule
	index int
}

// rulesScreenState holds what the last fetch learned, for the summary header.
type rulesScreenState struct {
	loaded bool
	state  *coreapi.FilterRulesState
}

func (s *rulesScreenState) set(st *coreapi.FilterRulesState) {
	s.loaded, s.state = true, st
}

// summaryLines answers the one question a rules screen must never leave
// ambiguous: do these rules actually filter delivered mail?
func (s *rulesScreenState) summaryLines(w int) []string {
	if !s.loaded {
		return nil
	}
	if s.state == nil {
		return []string{stDim.Render(truncate("no filter rules yet — author them with `openemail rules add`", w))}
	}
	switch {
	case s.state.Status == "active":
		return []string{stLive.Render(truncate("ACTIVE — these rules filter delivered mail", w))}
	case s.state.ActiveScript != nil:
		return []string{stErr.Render(truncate(
			fmt.Sprintf("NOT ACTIVE — the hand-written script %q is the active filter; any edit here re-activates these rules", *s.state.ActiveScript), w))}
	default:
		return []string{stErr.Render(truncate("NOT ACTIVE — this mailbox has no active filter (delivery is unfiltered)", w))}
	}
}

// currentRules re-reads the document before a mutation: the screen edits by
// position, so it must not write back a list it read minutes ago.
func currentRules(ctx context.Context, c *coreapi.Client, mailboxID string) ([]coreapi.FilterRule, error) {
	st, err := c.GetRules(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	return append([]coreapi.FilterRule{}, st.Rules...), nil
}

// reorderRule moves a rule one position up (-1) or down (+1). Order is
// meaning: a rule's position decides whether an earlier `stop` preempts it.
func reorderRule(ctx context.Context, c *coreapi.Client, mailboxID string, rr ruleRow, delta int) (string, error) {
	rules, err := currentRules(ctx, c, mailboxID)
	if err != nil {
		return "", err
	}
	to := rr.index + delta
	if rr.index >= len(rules) {
		return "", fmt.Errorf("rule %d no longer exists — refresh", rr.index+1)
	}
	if to < 0 || to >= len(rules) {
		return "", nil // already at the edge; a no-op, not an error
	}
	rules[rr.index], rules[to] = rules[to], rules[rr.index]
	if _, err := c.PutRules(ctx, mailboxID, coreapi.RulesDocument{Rules: rules}); err != nil {
		return "", err
	}
	return fmt.Sprintf("rule moved to position %d", to+1), nil
}

func ruleDeleteConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, rr ruleRow) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete rule " + ruleName(rr.rule, rr.index),
		body: "Remove this rule from the document. The remaining rules stay active and keep " +
			"their order. To keep the rule but stop it running, press t to switch it off instead.",
		verb: "delete rule",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			rules, err := currentRules(sctx, c, mbx.ID)
			if err != nil {
				return "", err
			}
			if rr.index >= len(rules) {
				return "", fmt.Errorf("rule %d no longer exists — refresh", rr.index+1)
			}
			rules = append(rules[:rr.index], rules[rr.index+1:]...)
			if _, err := c.PutRules(sctx, mbx.ID, coreapi.RulesDocument{Rules: rules}); err != nil {
				return "", err
			}
			return fmt.Sprintf("rule deleted (%d left)", len(rules)), nil
		},
	})
}

func rulesDeleteAllConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete ALL filter rules — " + mailboxLabel(&mbx),
		body: "Removes the whole rules document. If it is the active filter, this mailbox is " +
			"left with NO active filter and delivery becomes unfiltered. Hand-written Sieve " +
			"scripts are untouched.",
		verb: "delete all rules",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if _, err := c.DeleteRules(sctx, mbx.ID); err != nil {
				return "", err
			}
			return "all filter rules deleted", nil
		},
	})
}

// ── rendering ────────────────────────────────────────────────────────────────

func ruleName(r coreapi.FilterRule, idx int) string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return fmt.Sprintf("(rule %d)", idx+1)
}

func ruleState(r coreapi.FilterRule) string {
	if r.IsEnabled() {
		return "on"
	}
	return "off"
}

func ruleMatch(r coreapi.FilterRule) string {
	if r.Match == "" {
		return "all"
	}
	return r.Match
}

func ruleConditions(cs []coreapi.RuleCondition) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, conditionLine(c))
	}
	return strings.Join(parts, ", ")
}

func conditionLine(c coreapi.RuleCondition) string {
	field := c.Field
	if c.Field == "header" && c.Header != "" {
		field = c.Header
	}
	s := field + " " + c.Op
	if c.Op != "exists" && c.Value != nil {
		s += " " + ruleValue(c.Value)
	}
	if c.Not {
		s = "NOT " + s
	}
	return s
}

func ruleActions(as []coreapi.RuleAction) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		parts = append(parts, actionLine(a))
	}
	return strings.Join(parts, ", ")
}

func actionLine(a coreapi.RuleAction) string {
	switch a.Type {
	case "fileInto":
		return "file into " + a.Label
	case "addFlag":
		return "flag " + a.Flag
	case "redirect":
		if a.KeepCopy {
			return "redirect to " + a.To + " (keep copy)"
		}
		return "redirect to " + a.To
	case "discard":
		return "discard"
	}
	return a.Type
}

// ruleValue renders a condition value: a string for every field but size,
// which arrives as a JSON number.
func ruleValue(v any) string {
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
