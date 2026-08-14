package tui

import (
	"context"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// doNotSendDesc lists the ACCOUNT's own do-not-send list (core 0033) — the
// tenant-owned tier beside the deployment-global suppressions screen, which
// stays system-only. Policy, not evidence: the tenant wrote every row, so
// unlike the global list both add and remove belong right here.
//
// accountID comes from the session (Options.AccountID); the screen is offered
// to account principals only, since a system key has no account of its own.
func doNotSendDesc(accountID string) resourceDesc {
	return resourceDesc{
		key:     "do-not-send",
		name:    "Do-Not-Send",
		caption: "addresses this account never mails · enforced on outbound at the last hop",
		columns: []column{
			{title: "ADDRESS", flex: true},
			{title: "REASON", width: 12},
			{title: "ADDED", width: 16},
			{title: "NOTE", width: 30},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListAccountSuppressions(ctx, accountID, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, s := range pg.Items {
				rows[i] = rowData{
					cells: []string{
						s.Address, s.Reason, fmtEpoch(s.CreatedAt), truncate(strOr(s.Note, "—"), 30),
					},
					item: s,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			s := item.(coreapi.AccountSuppression)
			return []kv{
				{k: "address", v: s.Address},
				{k: "reason", v: s.Reason},
				{k: "added", v: fmtEpoch(s.CreatedAt)},
				{},
				{v: "Note"},
				{v: strOr(s.Note, "(none)")},
				{},
				{v: "Binds mail this account submits, from any of its domains. The"},
				{v: "platform's own bounce/complaint suppressions are separate and"},
				{v: "surface per message in the delivery log as relay_suppressed."},
			}
		},
		actions: []action{
			{key: "a", label: "a add", run: func(ctx context.Context, ui *Options, _ any) pane {
				return doNotSendAddPane(ctx, ui, accountID)
			}},
			{key: "u", label: "u allow again", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return doNotSendRemoveConfirm(ctx, ui, accountID, item.(coreapi.AccountSuppression))
			}},
		},
	}
}

func doNotSendAddPane(ctx context.Context, ui *Options, accountID string) pane {
	var (
		address string
		note    string
	)
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Address").Placeholder("them@example.net").
				Value(&address).Validate(required("address")),
			huh.NewInput().Title("Note").
				Description("why — stored on the row so the list stays explainable").
				Value(&note),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		s, err := c.AddAccountSuppression(sctx, accountID, coreapi.AccountSuppressionCreate{
			Address: strings.TrimSpace(address), Note: strings.TrimSpace(note),
		})
		if err != nil {
			return "", nil, err
		}
		return s.Address + " added — this account will not mail it", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "Stop mailing an address", build: build, submit: submit})
}

// doNotSendRemoveConfirm mirrors the console's "Allow again": it clears the
// account's own row AND, since this screen only exists on an account key,
// attempts the global hard-bounce lift core grants that key — so a bounce
// block discovered through a refused send is cleared in the same gesture. A
// global complaint row is untouchable here by construction (core answers the
// account key 404), which the body says out loud.
func doNotSendRemoveConfirm(ctx context.Context, ui *Options, accountID string, s coreapi.AccountSuppression) pane {
	body := "Removes " + s.Address + " from this account's do-not-send list, and lifts the " +
		"platform's bounce block on it if one exists.\n\n" +
		"If the address was suppressed platform-wide over a spam COMPLAINT, mail to it " +
		"stays refused — only an operator can lift that."
	if s.Note != nil && *s.Note != "" {
		body += "\n\nNote on the row: " + *s.Note
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Allow mail to " + s.Address,
		body:  body,
		verb:  "allow again",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if _, err := c.RemoveAccountSuppression(sctx, accountID, s.Address); err != nil && !coreapi.IsNotFound(err) {
				return "", err
			}
			if _, err := c.LiftSuppression(sctx, s.Address); err != nil && !coreapi.IsNotFound(err) {
				return "", err
			}
			return s.Address + " can be mailed again", nil
		},
	})
}
