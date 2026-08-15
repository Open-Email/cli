package coreapi

import (
	"context"
	"net/http"
)

// RuleCondition is one test in a filter rule. Field selects the union arm:
// address fields (from/to/cc/toOrCc), text fields (subject/listId), `header`
// (which also needs Header), `body` (op contains only), and `size` (op
// over/under, Value an integer of bytes). Op `exists` takes no value.
//
// Value is `any` because the size arm carries a number where every other arm
// carries a string — core validates the pairing.
type RuleCondition struct {
	Field  string `json:"field"`
	Op     string `json:"op"`
	Value  any    `json:"value,omitempty"`
	Header string `json:"header,omitempty"`
	Not    bool   `json:"not,omitempty"`
}

// RuleAction is one thing a matching rule does. Type selects the arm:
// fileInto (Label), addFlag (Flag: seen|answered|flagged), redirect (To, plus
// KeepCopy), discard (nothing else).
type RuleAction struct {
	Type     string `json:"type"`
	Label    string `json:"label,omitempty"`
	Flag     string `json:"flag,omitempty"`
	To       string `json:"to,omitempty"`
	KeepCopy bool   `json:"keepCopy,omitempty"`
}

// FilterRule is one rule of the flat, ordered document. Match is "all" or
// "any" over Conditions; Stop halts evaluation of later rules when this one
// matches.
type FilterRule struct {
	Name       string          `json:"name,omitempty"`
	Match      string          `json:"match,omitempty"`
	Conditions []RuleCondition `json:"conditions"`
	Actions    []RuleAction    `json:"actions"`
	Enabled    *bool           `json:"enabled,omitempty"`
	Stop       bool            `json:"stop,omitempty"`
}

// IsEnabled reports the rule's effective enabled state (the field defaults to
// true when absent).
func (r FilterRule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// RulesDocument is the PUT body: the whole ordered list (max 100 rules).
// Rules are replaced wholesale — read, edit, put.
type RulesDocument struct {
	Rules []FilterRule `json:"rules"`
}

// FilterRulesState is GET /rules: the document, whether it is the mailbox's
// active filter, which script IS active (nil = filtering off), and the Sieve
// the rules compile to.
type FilterRulesState struct {
	Rules []FilterRule `json:"rules"`
	State string       `json:"state"` // active | inactive
	// ActiveScript names whatever script is currently active; nil means no
	// active script at all (delivery is unfiltered).
	ActiveScript *string `json:"activeScript"`
	Script       string  `json:"script"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// FilterRulesPutResult is PUT /rules. State is always "active" — a successful
// put makes the rules the active filter, displacing any hand-written script.
type FilterRulesPutResult struct {
	Rules   []FilterRule `json:"rules"`
	State   string       `json:"state"`
	Created bool         `json:"created"`
	Script  string       `json:"script"`
}

// FilterRulesDeleted is DELETE /rules.
type FilterRulesDeleted struct {
	Deleted bool `json:"deleted"`
}

func (c *Client) rulesPath(mailboxID string) string {
	return "/mailboxes/" + escapeSegment(mailboxID) + "/rules"
}

// GetRules reads the mailbox's filter rules. 404 when the mailbox has none —
// including when a hand-written script occupies the reserved name.
func (c *Client) GetRules(ctx context.Context, mailboxID string) (*FilterRulesState, error) {
	var out FilterRulesState
	err := c.doJSON(ctx, request{
		method: http.MethodGet, path: c.rulesPath(mailboxID), idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PutRules replaces the rules and makes them the active filter — a previously
// active hand-written script is deactivated (last management action wins). An
// empty list is legal ("rules mode, no rules yet"). 409 name_conflict when a
// hand-written script already holds the reserved name `openemail.rules`.
func (c *Client) PutRules(ctx context.Context, mailboxID string, doc RulesDocument) (*FilterRulesPutResult, error) {
	if doc.Rules == nil {
		doc.Rules = []FilterRule{} // the field is required; nil would marshal to null
	}
	var out FilterRulesPutResult
	err := c.doJSON(ctx, request{
		method: http.MethodPut, path: c.rulesPath(mailboxID),
		body: mustJSON(doc), contentType: "application/json", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRules removes the generated script. If it was the active filter the
// mailbox is left with no active script — delivery becomes unfiltered.
// Hand-written scripts are untouched.
func (c *Client) DeleteRules(ctx context.Context, mailboxID string) (*FilterRulesDeleted, error) {
	var out FilterRulesDeleted
	err := c.doJSON(ctx, request{
		method: http.MethodDelete, path: c.rulesPath(mailboxID),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
