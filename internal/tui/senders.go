package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// The names of the two lists behind this screen. They are a CONTRACT with the
// console's "Blocked & allowed senders" screen (openemail-console
// src/sending.ts), which creates and reads lists under exactly these names: a
// different string here would show a tenant an empty screen beside the rules
// they wrote there, and write new ones where the console never looks.
const (
	blockedSendersListName = "Blocked senders"
	allowedSendersListName = "Allowed senders"
)

// senderRule is one row: an entry, the verdict of the list it is on, and that
// list's id, so a remove or flip acts on the list the row was READ from. One
// address can sit on both lists (core resolves that to allow), so the pattern
// alone does not say which rule a row is.
type senderRule struct {
	Verdict string // block | allow
	ListID  string
	Entry   coreapi.AddressListEntry
}

// sendersCursor is the screen's page cursor: which list the next page reads,
// and where in it. ListID is empty on the step into the allow list, which is
// resolved when that page is read rather than when the walk started, so an
// allow list created meanwhile is still found.
type sendersCursor struct {
	Verdict string `json:"v"`
	ListID  string `json:"l,omitempty"`
	Inner   string `json:"c,omitempty"`
}

func sendersListName(verdict string) string {
	if verdict == "allow" {
		return allowedSendersListName
	}
	return blockedSendersListName
}

// sendersDesc shows the account's INBOUND sender policy: who is refused and
// who skips the filters, as one table over two account-scoped lists (block and
// allow) with the verdict on each row — the console's "Blocked & allowed
// senders" screen. Do-Not-Send is the outbound half and stays its own screen:
// a refused stranger and a bounced colleague are different consequences for
// different people, and one table with a direction column is the confusion
// both screens exist to end. Other lists — per-domain, per-mailbox, or more
// named lists at account scope — are `openemail lists`'s job.
//
// accountID comes from the session; the screen is offered to account
// principals only.
func sendersDesc(accountID string) resourceDesc {
	fam := coreapi.AccountLists(accountID)
	return resourceDesc{
		key:     "senders",
		name:    "Blocked & Allowed Senders",
		caption: "inbound, account-wide · block refuses the message · allow beats it and skips the spam filter",
		// The sender leads because the first cell titles the detail pane; the
		// added date lives there too, leaving the sender room to be read.
		columns: []column{
			{title: "SENDER", flex: true},
			{title: "VERDICT", width: 7},
			{title: "EXPIRES", width: 16},
			{title: "HITS", width: 6},
			{title: "NOTE", width: 24},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			var cur sendersCursor
			// On a first page both lists come from one lookup; a later step into
			// the allow list resolves it then.
			allowID, allowKnown := "", false
			if cursor == "" {
				block, allow, err := findSenderLists(ctx, c, accountID)
				if err != nil {
					return nil, "", err
				}
				cur = sendersCursor{Verdict: "block", ListID: listIDOf(block)}
				allowID, allowKnown = listIDOf(allow), true
			} else if err := json.Unmarshal([]byte(cursor), &cur); err != nil {
				return nil, "", fmt.Errorf("senders cursor: %w", err)
			}
			var rows []rowData
			for {
				if cur.Verdict == "allow" && cur.ListID == "" {
					if !allowKnown {
						_, allow, err := findSenderLists(ctx, c, accountID)
						if err != nil {
							return nil, "", err
						}
						allowID, allowKnown = listIDOf(allow), true
					}
					cur.ListID = allowID
				}
				if cur.ListID != "" {
					pg, err := c.ListAddressListEntries(ctx, fam, cur.ListID, pageLimit, cur.Inner)
					if err != nil {
						if !coreapi.IsNotFound(err) {
							return nil, "", err
						}
						// A load-more page appends to the old list's rows; the
						// replacement's first page would mix with them.
						if cur.Inner != "" {
							return nil, "", fmt.Errorf("the %s list changed while this screen was open: %w",
								sendersListName(cur.Verdict), errReloadRequired)
						}
						// Gone between resolving it and reading it: nothing on it.
						pg = coreapi.Page[coreapi.AddressListEntry]{}
					}
					for _, e := range pg.Items {
						r := senderRule{Verdict: cur.Verdict, ListID: cur.ListID, Entry: e}
						rows = append(rows, rowData{
							id: r.Verdict + ":" + e.Pattern,
							cells: []string{e.Pattern, r.Verdict, fmtEpochPtr(e.ExpiresAt),
								fmtInt(e.HitCount), truncate(strOr(e.Note, "—"), 24)},
							item: r,
						})
					}
					if pg.NextCursor != "" {
						cur.Inner = pg.NextCursor
						next, _ := json.Marshal(cur)
						return rows, string(next), nil
					}
				}
				if cur.Verdict != "block" {
					return rows, "", nil
				}
				// The block list is done: fill the rest of this page from the allow
				// list rather than end short and hide it behind a load-more.
				cur = sendersCursor{Verdict: "allow", ListID: allowID}
			}
		},
		detail: func(item any) []kv {
			r := item.(senderRule)
			e := r.Entry
			out := []kv{
				{k: "verdict", v: r.Verdict},
				{k: "sender", v: e.Pattern},
				{k: "added", v: fmtEpoch(e.CreatedAt)},
				{k: "expires", v: fmtEpochPtr(e.ExpiresAt)},
				// A floor: core folds hits within ten seconds of the last one.
				{k: "hits", v: fmtInt(e.HitCount)},
				{k: "last hit", v: fmtEpochPtr(e.LastHitAt)},
				{},
				{v: "Note"},
				{v: strOr(e.Note, "(none)")},
				{},
			}
			if r.Verdict == "allow" {
				out = append(out,
					kv{v: "Mail from this sender skips the spam filter and this account's"},
					kv{v: "blocks (a block-all included), but only on an identity something"},
					kv{v: "vouched for: the From header by a DMARC pass, the envelope sender"},
					kv{v: "by an SPF pass."},
				)
			} else {
				out = append(out,
					kv{v: "Mail from this sender (envelope sender or From) is refused in"},
					kv{v: "the SMTP session, so the sending server bounces it. Mail already"},
					kv{v: "accepted (a pickup, a group copy) is filed to Junk instead."},
				)
			}
			return append(out,
				kv{},
				kv{v: "Applies to every mailbox in the account. Lists scoped to one"},
				kv{v: "domain or mailbox (webmail's, `openemail lists`) outrank these;"},
				kv{v: "an allow and a block for the same sender here resolve to allow."},
			)
		},
		actions: []action{
			{key: "a", label: "a add", run: func(ctx context.Context, ui *Options, _ any) pane {
				return senderAddPane(ctx, ui, accountID)
			}},
			{key: "f", label: "f flip", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return senderFlipConfirm(ctx, ui, accountID, item.(senderRule))
			}},
			{key: "d", label: "d remove", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return senderRemoveConfirm(ctx, ui, accountID, item.(senderRule))
			}},
		},
	}
}

func listIDOf(l *coreapi.AddressList) string {
	if l == nil {
		return ""
	}
	return l.ID
}

// findSenderLists locates the account's two lists in one lookup, either being
// nil when it does not exist. Matched on (scope, direction, verdict, name), so
// a list a tenant happened to give one of these names for another purpose is
// never mistaken for it.
func findSenderLists(ctx context.Context, c *coreapi.Client, accountID string) (block, allow *coreapi.AddressList, err error) {
	fam := coreapi.AccountLists(accountID)
	items, err := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
		return c.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
			ScopeKind: "account", ScopeID: accountID, Direction: "inbound",
		}, 0, cur)
	})
	if err != nil {
		return nil, nil, err
	}
	for i := range items {
		switch {
		case items[i].Verdict == "block" && items[i].Name == blockedSendersListName:
			block = &items[i]
		case items[i].Verdict == "allow" && items[i].Name == allowedSendersListName:
			allow = &items[i]
		}
	}
	return block, allow, nil
}

func findSenderList(ctx context.Context, c *coreapi.Client, accountID, verdict string) (*coreapi.AddressList, error) {
	block, allow, err := findSenderLists(ctx, c, accountID)
	if verdict == "allow" {
		return allow, err
	}
	return block, err
}

// ensureSenderList is findSenderList for a WRITE: it creates the list when the
// account has none. Only the write paths call it — reading the screen must
// never mint directory state.
func ensureSenderList(ctx context.Context, c *coreapi.Client, accountID, verdict string) (*coreapi.AddressList, error) {
	list, err := findSenderList(ctx, c, accountID, verdict)
	if err != nil || list != nil {
		return list, err
	}
	name := sendersListName(verdict)
	fam := coreapi.AccountLists(accountID)
	created, err := c.CreateAddressList(ctx, fam, coreapi.AddressListCreate{
		Name: name, ScopeKind: "account", ScopeID: accountID,
		Direction: "inbound", Verdict: verdict,
	})
	if err == nil {
		return created, nil
	}
	// A 409 is a lost create race (adopt the winner) or the name taken at this
	// scope by a list of another kind (refuse rather than write into it). One
	// read of every list at the scope tells them apart, as Do-Not-Send does.
	if coreapi.Status(err) != 409 {
		return nil, err
	}
	all, e2 := coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
		return c.ListAddressLists(ctx, fam, coreapi.ListAddressListsFilter{
			ScopeKind: "account", ScopeID: accountID,
		}, 0, cur)
	})
	if e2 != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name != name {
			continue
		}
		if all[i].Direction == "inbound" && all[i].Verdict == verdict {
			return &all[i], nil
		}
		return nil, fmt.Errorf(
			"a list named %q already exists for a different purpose (%s/%s); rename it with `openemail lists rename`",
			name, all[i].Direction, all[i].Verdict)
	}
	return nil, err
}

// addSenderRule puts one pattern on the verdict's list, creating the list on
// first use. A 404 on the add means the list was deleted after it was found;
// one re-resolve covers it.
func addSenderRule(ctx context.Context, c *coreapi.Client, accountID, verdict, pattern, note string) (*coreapi.AddressListEntry, error) {
	in := coreapi.AddressListEntryInput{Pattern: strings.TrimSpace(pattern), Note: strings.TrimSpace(note)}
	fam := coreapi.AccountLists(accountID)
	list, err := ensureSenderList(ctx, c, accountID, verdict)
	if err != nil {
		return nil, err
	}
	e, err := c.AddAddressListEntry(ctx, fam, list.ID, in)
	if err != nil && coreapi.IsNotFound(err) {
		if list, err = ensureSenderList(ctx, c, accountID, verdict); err != nil {
			return nil, err
		}
		e, err = c.AddAddressListEntry(ctx, fam, list.ID, in)
	}
	return e, err
}

// removeSenderRule deletes the rule from the list its row was read from. A 404
// there is either the entry already gone (done) or the list replaced since the
// read, in which case the current list of that verdict is tried once.
func removeSenderRule(ctx context.Context, c *coreapi.Client, accountID string, r senderRule) error {
	fam := coreapi.AccountLists(accountID)
	_, err := c.RemoveAddressListEntry(ctx, fam, r.ListID, r.Entry.Pattern)
	if err == nil || !coreapi.IsNotFound(err) {
		return err
	}
	list, err := findSenderList(ctx, c, accountID, r.Verdict)
	if err != nil || list == nil || list.ID == r.ListID {
		return err
	}
	if _, err = c.RemoveAddressListEntry(ctx, fam, list.ID, r.Entry.Pattern); err != nil && !coreapi.IsNotFound(err) {
		return err
	}
	return nil
}

// flipSenderRule moves a rule to the other verdict, note and all. There is no
// PATCH, so it is two writes, and the order is the console's: ADD the new
// verdict first, then REMOVE the old. Between the two the sender is on both
// lists, which core resolves to allow; if the add fails the old rule stands,
// where the reverse order would leave no rule at all.
func flipSenderRule(ctx context.Context, c *coreapi.Client, accountID string, r senderRule) error {
	note := ""
	if r.Entry.Note != nil {
		note = *r.Entry.Note
	}
	if _, err := addSenderRule(ctx, c, accountID, flippedVerdict(r.Verdict), r.Entry.Pattern, note); err != nil {
		return err
	}
	return removeSenderRule(ctx, c, accountID, r)
}

func flippedVerdict(v string) string {
	if v == "allow" {
		return "block"
	}
	return "allow"
}

func senderAddPane(ctx context.Context, ui *Options, accountID string) pane {
	var (
		pattern string
		verdict = "block"
		note    string
	)
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Sender").Placeholder("them@example.net").
				Description("or a whole domain: @example.net, or @.example.net for its subdomains too").
				Value(&pattern).Validate(required("sender")),
			huh.NewSelect[string]().Title("Verdict").
				Options(
					huh.NewOption("Block — refuse mail from this sender", "block"),
					huh.NewOption("Allow — beat this account's blocks, skip the spam filter", "allow"),
				).
				Value(&verdict),
			huh.NewInput().Title("Note").
				Description("why — stored on the row so the list stays explainable").
				Value(&note),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		e, err := addSenderRule(sctx, c, accountID, verdict, pattern, note)
		if err != nil {
			return "", nil, err
		}
		// The STORED spelling: core normalizes `@example.net` to `*@example.net`,
		// and the row the user is about to see says the latter.
		if verdict == "allow" {
			return e.Pattern + " allowed — mail from it skips the spam filter", nil, nil
		}
		return e.Pattern + " blocked — mail from it is refused", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "Block or allow a sender", build: build, submit: submit})
}

func senderFlipConfirm(ctx context.Context, ui *Options, accountID string, r senderRule) pane {
	to := flippedVerdict(r.Verdict)
	body := "Moves " + r.Entry.Pattern + " from " + sendersListName(r.Verdict) + " to " +
		sendersListName(to) + ", keeping its note."
	if to == "allow" {
		body += "\n\nMail from it will skip this account's blocks and the spam filter whenever SPF or DMARC vouches for the sender."
	} else {
		body += "\n\nMail from it will be refused."
	}
	if r.Entry.ExpiresAt != nil {
		body += "\n\nThe new rule does not expire."
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Flip " + r.Entry.Pattern + " to " + to,
		body:  body,
		verb:  "flip to " + to,
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := flipSenderRule(sctx, c, accountID, r); err != nil {
				return "", err
			}
			return r.Entry.Pattern + " is now " + map[string]string{"allow": "allowed", "block": "blocked"}[to], nil
		},
	})
}

func senderRemoveConfirm(ctx context.Context, ui *Options, accountID string, r senderRule) pane {
	body := "Removes the " + r.Verdict + " rule for " + r.Entry.Pattern + " from " + sendersListName(r.Verdict) + "."
	if r.Verdict == "allow" {
		body += "\n\nMail from it goes back through the spam filter and this account's blocks."
	} else {
		body += "\n\nMail from it is accepted again, unless another list still blocks it."
	}
	if r.Entry.Note != nil && *r.Entry.Note != "" {
		body += "\n\nNote on the row: " + *r.Entry.Note
	}
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Remove " + r.Verdict + " rule for " + r.Entry.Pattern,
		body:  body,
		verb:  "remove",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := removeSenderRule(sctx, c, accountID, r); err != nil {
				return "", err
			}
			return r.Entry.Pattern + " is no longer " + map[string]string{"allow": "allowed", "block": "blocked"}[r.Verdict], nil
		},
	})
}
