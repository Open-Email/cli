package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// doNotSendListName is the name core's 0040 migration gives the seeded list,
// and the name this screen creates one under. It must match the CLI's
// `DoNotSendListName`: a different string here would give a tenant whose rows
// core already migrated a SECOND, empty list.
const doNotSendListName = "Do not send"

// doNotSendDesc shows the ACCOUNT's own do-not-send list — one ADDRESS LIST
// (core migration 0040: account scope, outbound, block), which is what the
// per-account suppression table of 0033 became. Policy, not evidence: the
// tenant wrote every row, so unlike the deployment-global suppressions screen
// next door both add and remove belong right here.
//
// The screen deliberately shows ONE list rather than the whole address-list
// construct: inbound blocks, spam-filter exemptions and the narrower scopes are
// a different job with a different audience, and folding them into one table
// with a direction column is exactly the confusion this feature exists to end.
//
// accountID comes from the session (Options.AccountID); the screen is offered
// to account principals only, since a system key has no account of its own.
func doNotSendDesc(accountID string) resourceDesc {
	fam := coreapi.AccountLists(accountID)
	return resourceDesc{
		key:     "do-not-send",
		name:    "Do-Not-Send",
		caption: "addresses this account never mails · enforced on outbound at the last hop",
		columns: []column{
			{title: "ADDRESS", flex: true},
			{title: "ADDED", width: 16},
			{title: "NOTE", width: 40},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			list, err := findDoNotSendList(ctx, c, accountID)
			if err != nil {
				return nil, "", err
			}
			// No list means nobody has been suppressed — an empty table, not an
			// error, and emphatically not a list minted by opening a screen.
			if list == nil {
				return nil, "", nil
			}
			pg, err := c.ListAddressListEntries(ctx, fam, list.ID, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, e := range pg.Items {
				rows[i] = rowData{
					cells: []string{e.Pattern, fmtEpoch(e.CreatedAt), truncate(strOr(e.Note, "—"), 40)},
					item:  e,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			e := item.(coreapi.AddressListEntry)
			return []kv{
				{k: "pattern", v: e.Pattern},
				{k: "added", v: fmtEpoch(e.CreatedAt)},
				{},
				{v: "Note"},
				{v: strOr(e.Note, "(none)")},
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
				return doNotSendRemoveConfirm(ctx, ui, accountID, item.(coreapi.AddressListEntry))
			}},
		},
	}
}

// findDoNotSendList locates the account's seeded list, or nil when it has none.
// Matched on (scope, direction, verdict, name) rather than on name alone, so a
// list a tenant happened to name "Do not send" through the CLI's general
// `lists` surface cannot be written into by this screen.
func findDoNotSendList(ctx context.Context, c *coreapi.Client, accountID string) (*coreapi.AddressList, error) {
	fam := coreapi.AccountLists(accountID)
	items, err := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
		return c.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
			ScopeKind: "account", ScopeID: accountID, Direction: "outbound", Verdict: "block",
		}, 0, cur)
	})
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == doNotSendListName {
			return &items[i], nil
		}
	}
	return nil, nil
}

// ensureDoNotSendList is findDoNotSendList for a WRITE: it creates the list
// when the account has none. Only the add path calls it — reading a screen must
// never mint directory state.
func ensureDoNotSendList(ctx context.Context, c *coreapi.Client, accountID string) (*coreapi.AddressList, error) {
	list, err := findDoNotSendList(ctx, c, accountID)
	if err != nil || list != nil {
		return list, err
	}
	fam := coreapi.AccountLists(accountID)
	created, err := c.CreateAddressList(ctx, fam, coreapi.AddressListCreate{
		Name: doNotSendListName, ScopeKind: "account", ScopeID: accountID,
		Direction: "outbound", Verdict: "block",
	})
	if err != nil {
		// A 409 is either a lost create race (adopt the winner) or a same-named
		// list built for a different purpose (refuse clearly rather than write
		// outbound-block entries into an inbound/allow list). One re-read by name
		// at this scope tells them apart — the CLI facade does the same.
		if coreapi.Status(err) == 409 {
			all, e2 := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
				return c.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
					ScopeKind: "account", ScopeID: accountID,
				}, 0, cur)
			})
			if e2 != nil {
				return nil, err
			}
			for i := range all {
				if all[i].Name != doNotSendListName {
					continue
				}
				if all[i].Direction == "outbound" && all[i].Verdict == "block" {
					return &all[i], nil
				}
				return nil, fmt.Errorf(
					"a list named %q already exists for a different purpose (%s/%s); rename it or use the lists screen",
					doNotSendListName, all[i].Direction, all[i].Verdict)
			}
		}
		return nil, err
	}
	return created, nil
}

func doNotSendAddPane(ctx context.Context, ui *Options, accountID string) pane {
	var (
		address string
		note    string
	)
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Address").Placeholder("them@example.net").
				Description("or a whole domain: @example.net, or @.example.net for its subdomains too").
				Value(&address).Validate(required("address")),
			huh.NewInput().Title("Note").
				Description("why — stored on the row so the list stays explainable").
				Value(&note),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		list, err := ensureDoNotSendList(sctx, c, accountID)
		if err != nil {
			return "", nil, err
		}
		e, err := c.AddAddressListEntry(sctx, coreapi.AccountLists(accountID), list.ID, coreapi.AddressListEntryInput{
			Pattern: strings.TrimSpace(address), Note: strings.TrimSpace(note),
		})
		if err != nil {
			return "", nil, err
		}
		// The STORED spelling: core normalizes `@example.net` to `*@example.net`,
		// and the row the user is about to see in the table says the latter.
		return e.Pattern + " added — this account will not mail it", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "Stop mailing an address", build: build, submit: submit})
}

// doNotSendRemoveConfirm mirrors the console's "Allow again": it clears the
// account's own entry AND, since this screen only exists on an account key,
// attempts the global hard-bounce lift core grants that key — so a bounce
// block discovered through a refused send is cleared in the same gesture. A
// global complaint row is untouchable here by construction (core answers the
// account key 404), which the body says out loud.
func doNotSendRemoveConfirm(ctx context.Context, ui *Options, accountID string, e coreapi.AddressListEntry) pane {
	body := "Removes " + e.Pattern + " from this account's do-not-send list, and lifts the " +
		"platform's bounce block on it if one exists.\n\n" +
		"If the address was suppressed platform-wide over a spam COMPLAINT, mail to it " +
		"stays refused — only an operator can lift that."
	if e.Note != nil && *e.Note != "" {
		body += "\n\nNote on the row: " + *e.Note
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Allow mail to " + e.Pattern,
		body:  body,
		verb:  "allow again",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			list, err := findDoNotSendList(sctx, c, accountID)
			if err != nil {
				return "", err
			}
			if list != nil {
				if _, err := c.RemoveAddressListEntry(sctx, coreapi.AccountLists(accountID), list.ID, e.Pattern); err != nil && !coreapi.IsNotFound(err) {
					return "", err
				}
			}
			// The global lift takes a bare ADDRESS, so a domain-shaped pattern has
			// nothing to lift there — and asking would 404 harmlessly, which
			// IsNotFound already tolerates.
			if _, err := c.LiftSuppression(sctx, e.Pattern); err != nil && !coreapi.IsNotFound(err) {
				return "", err
			}
			return e.Pattern + " can be mailed again", nil
		},
	})
}
