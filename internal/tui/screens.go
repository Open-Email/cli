package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Open-Email/cli/internal/coreapi"
)

// allDescriptors declares every listing screen. Sidebar gating (Accounts is
// system-only) happens in newRoot; the map itself is principal-agnostic.
func allDescriptors() map[string]resourceDesc {
	return map[string]resourceDesc{
		"domains":   domainsDesc(),
		"traffic":   trafficPickerDesc(),
		"mailboxes": mailboxesDesc(),
		"routes":    routesDesc(),
		"patterns":  patternsDesc(),
		"keys":      keysDesc(),
		"accounts":  accountsDesc(),
	}
}

func domainsDesc() resourceDesc {
	return resourceDesc{
		key:  "domains",
		name: "Domains",
		columns: []column{
			{title: "DOMAIN", flex: true},
			{title: "ON", width: 3},
			// The lifecycle view (enabled AND the capability), not the raw
			// columns: a disabled domain with can_send=1 is not sending.
			{title: "SEND", width: 4},
			{title: "RECV", width: 4},
			{title: "ALIAS OF", width: 16},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListDomains(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, d := range pg.Items {
				rows[i] = rowData{
					cells: []string{d.Domain, yn(d.Enabled), yn(d.Sending), yn(d.Receiving), strOr(d.AliasOf, "—"), fmtEpoch(d.CreatedAt)},
					item:  d,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			d := item.(coreapi.Domain)
			kvs := []kv{
				{k: "domain", v: d.Domain},
				// verified is true for every row that exists — control of the
				// domain is the precondition for core creating it at all.
				{k: "verified", v: yn(d.Verified)},
				{k: "receiving", v: yn(d.Receiving)},
				{k: "sending", v: yn(d.Sending)},
				{k: "enabled", v: yn(d.Enabled)},
				{k: "can send", v: yn(d.CanSend)},
				{k: "can receive", v: yn(d.CanReceive)},
				{k: "alias of", v: strOr(d.AliasOf, "—")},
				{k: "fbl", v: yn(d.FBL)},
				{k: "dmarc ingestion", v: yn(d.DMARC)},
				{k: "account", v: strOr(d.AccountID, "platform (no account)")},
				{k: "created", v: fmtEpoch(d.CreatedAt)},
				{},
				{v: "DNS"},
			}
			if d.DNSStatus == nil {
				kvs = append(kvs, kv{k: "status", v: "never checked"})
			} else {
				kvs = append(kvs,
					kv{k: "mx", v: dnsMark(d.DNSStatus.MX)},
					kv{k: "spf", v: dnsMark(d.DNSStatus.SPF)},
					kv{k: "dkim", v: dnsMark(d.DNSStatus.DKIM)},
					kv{k: "dmarc", v: dnsMark(d.DNSStatus.DMARC)},
				)
				if d.DNSStatus.JMAP != nil {
					kvs = append(kvs, kv{k: "jmap", v: dnsMark(d.DNSStatus.JMAP)})
				}
				kvs = append(kvs, kv{k: "checked", v: fmtEpochPtr(d.DNSCheckedAt)})
			}
			return kvs
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return domainFormPane(ctx, ui, nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				d := item.(coreapi.Domain)
				return domainFormPane(ctx, ui, &d)
			}},
			{key: "D", label: "D dmarc", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				d := item.(coreapi.Domain)
				return newScreenPane(ctx, ui, dmarcDesc(d.Domain))
			}},
		},
		extra: func(ctx context.Context, c *coreapi.Client, item any) ([]kv, error) {
			d := item.(coreapi.Domain)
			// Two independent reads for one pane — issue them together rather
			// than making the detail wait on a round trip it does not need.
			var (
				wg      sync.WaitGroup
				t       *coreapi.DomainTraffic
				tErr    error
				dm      *coreapi.DomainDmarc
				dmErr   error
				kvs     []kv
				window  = coreapi.DmarcWindowDefault
				outcome []string
			)
			wg.Add(2)
			go func() { defer wg.Done(); t, tErr = c.GetDomainTraffic(ctx, d.Domain, "24h") }()
			go func() { defer wg.Done(); dm, dmErr = c.GetDomainDmarc(ctx, d.Domain, window) }()
			wg.Wait()
			if tErr != nil {
				return nil, tErr
			}
			kvs = []kv{
				{},
				{v: "Traffic (24h, sampled)"},
				{k: "events", v: fmt.Sprintf("%d", t.Totals.Events)},
				{k: "bytes", v: fmtBytes(t.Totals.Bytes)},
			}
			outcome = make([]string, 0, len(t.ByOutcome))
			for o := range t.ByOutcome {
				outcome = append(outcome, o)
			}
			sort.Strings(outcome)
			for _, o := range outcome {
				kvs = append(kvs, kv{k: o, v: fmt.Sprintf("%d", t.ByOutcome[o])})
			}
			// The readiness line is a courtesy, not the pane's subject: a
			// domain nobody reports on must still show its traffic, so a DMARC
			// failure is swallowed rather than failing the whole extra.
			if dmErr == nil && dm.Totals.Reports > 0 {
				kvs = append(kvs,
					kv{},
					kv{v: fmt.Sprintf("DMARC readiness (%dd)", dm.WindowDays)},
					kv{k: "verdict", v: dmarcVerdictCell(dm.Readiness.Verdict)},
					kv{k: "aligned", v: fmtRate(dm.Readiness.AlignedRate)},
					kv{k: "policy", v: dmarcPolicyCell(dm.Readiness)},
					kv{k: "blockers", v: fmt.Sprintf("%d", len(dm.Readiness.Blockers))},
					kv{k: "detail", v: "press D on the listing for the full DMARC screen"},
				)
			}
			return kvs, nil
		},
	}
}

func dnsMark(b *bool) string {
	switch {
	case b == nil:
		return "—"
	case *b:
		return "pass"
	default:
		return "FAIL"
	}
}

func mailboxesDesc() resourceDesc {
	return resourceDesc{
		key:  "mailboxes",
		name: "Mailboxes",
		columns: []column{
			{title: "ID", width: 26},
			{title: "PRIMARY ADDRESS", flex: true},
			{title: "QUOTA", width: 9},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListMailboxes(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, m := range pg.Items {
				quota := "∞"
				if m.QuotaBytes != nil {
					quota = fmtBytes(*m.QuotaBytes)
				}
				rows[i] = rowData{
					cells: []string{m.ID, strOr(m.PrimaryAddress, "—"), quota, fmtEpoch(m.CreatedAt)},
					item:  m,
				}
			}
			return rows, pg.NextCursor, nil
		},
		open: func(ctx context.Context, ui *Options, item any) pane {
			return newMessagesPane(ctx, ui, item.(coreapi.Mailbox))
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return mailboxFormPane(ctx, ui, nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				m := item.(coreapi.Mailbox)
				return mailboxFormPane(ctx, ui, &m)
			}},
			{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return mailboxDeleteConfirm(ctx, ui, item.(coreapi.Mailbox))
			}},
			{key: "D", label: "D deleted", run: func(ctx context.Context, ui *Options, _ any) pane {
				return newScreenPane(ctx, ui, deletedMailboxesDesc())
			}},
			{key: "C", label: "C credentials", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return newScreenPane(ctx, ui, credentialsDesc(item.(coreapi.Mailbox)))
			}},
		},
	}
}

func routesDesc() resourceDesc {
	return resourceDesc{
		key:  "routes",
		name: "Routes",
		columns: []column{
			{title: "ADDRESS", flex: true},
			{title: "TYPE", width: 7},
			{title: "TARGET", flex: true},
			{title: "PACED", width: 5},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListRoutes(ctx, "", "", pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, r := range pg.Items {
				rows[i] = rowData{
					cells: []string{r.Address, r.DestinationType, routeTarget(r.DestinationType, r.MailboxID, r.WebhookURL, r.RemoteAddress, nil), yn(r.Paced), fmtEpoch(r.CreatedAt)},
					item:  r,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			r := item.(coreapi.Route)
			return []kv{
				{k: "address", v: r.Address},
				{k: "domain", v: r.Domain},
				{k: "type", v: r.DestinationType},
				{k: "mailbox", v: strOr(r.MailboxID, "—")},
				{k: "webhook", v: strOr(r.WebhookURL, "—")},
				{k: "remote", v: strOr(r.RemoteAddress, "—")},
				{k: "paced", v: yn(r.Paced)},
				{k: "created", v: fmtEpoch(r.CreatedAt)},
			}
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return routeFormPane(ctx, ui, nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				r := item.(coreapi.Route)
				return routeFormPane(ctx, ui, &r)
			}},
			{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return routeDeleteConfirm(ctx, ui, item.(coreapi.Route))
			}},
			{key: "M", label: "M members", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				r := item.(coreapi.Route)
				if r.DestinationType != "group" {
					return nil
				}
				return newScreenPane(ctx, ui, membersDesc(r))
			}},
		},
		extra: func(ctx context.Context, c *coreapi.Client, item any) ([]kv, error) {
			r := item.(coreapi.Route)
			if r.DestinationType != "group" {
				return nil, nil
			}
			pg, err := c.ListMembers(ctx, r.Address, 100, "")
			if err != nil {
				return nil, err
			}
			kvs := []kv{{}, {v: fmt.Sprintf("Members (%d)", len(pg.Items))}}
			for i, m := range pg.Items {
				kvs = append(kvs, kv{k: fmt.Sprintf("%d", i+1), v: m.MemberAddress})
			}
			if pg.NextCursor != "" {
				kvs = append(kvs, kv{k: "…", v: "more — use `openemail routes members list`"})
			}
			return kvs, nil
		},
	}
}

func patternsDesc() resourceDesc {
	return resourceDesc{
		key:  "patterns",
		name: "Patterns",
		columns: []column{
			{title: "DOMAIN", width: 18},
			{title: "PATTERN", flex: true},
			{title: "PRIO", width: 4},
			{title: "TYPE", width: 7},
			{title: "TARGET", flex: true},
			{title: "PACED", width: 5},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListPatterns(ctx, "", pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, p := range pg.Items {
				rows[i] = rowData{
					cells: []string{p.Domain, p.Pattern, fmt.Sprintf("%d", p.Priority), p.DestinationType, routeTarget(p.DestinationType, p.MailboxID, p.WebhookURL, p.RemoteAddress, p.AliasAddress), yn(p.Paced)},
					item:  p,
				}
			}
			return rows, pg.NextCursor, nil
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return patternFormPane(ctx, ui, nil)
			}},
			{key: "e", label: "e edit", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				p := item.(coreapi.Pattern)
				return patternFormPane(ctx, ui, &p)
			}},
			{key: "d", label: "d delete", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return patternDeleteConfirm(ctx, ui, item.(coreapi.Pattern))
			}},
		},
		detail: func(item any) []kv {
			p := item.(coreapi.Pattern)
			return []kv{
				{k: "id", v: fmt.Sprintf("%d", p.ID)},
				{k: "domain", v: p.Domain},
				{k: "pattern", v: p.Pattern},
				{k: "priority", v: fmt.Sprintf("%d", p.Priority)},
				{k: "type", v: p.DestinationType},
				{k: "mailbox", v: strOr(p.MailboxID, "—")},
				{k: "webhook", v: strOr(p.WebhookURL, "—")},
				{k: "remote", v: strOr(p.RemoteAddress, "—")},
				{k: "alias", v: strOr(p.AliasAddress, "—")},
				{k: "paced", v: yn(p.Paced)},
				{k: "created", v: fmtEpoch(p.CreatedAt)},
			}
		},
	}
}

// routeTarget renders the destination a route/pattern points at.
func routeTarget(typ string, mailboxID, webhookURL, remote, alias *string) string {
	switch typ {
	case "mailbox":
		return strOr(mailboxID, "—")
	case "webhook":
		return strOr(webhookURL, "—")
	case "remote":
		return strOr(remote, "—")
	case "alias":
		return strOr(alias, "—")
	case "group":
		return "(members)"
	}
	return "—"
}

func keysDesc() resourceDesc {
	return resourceDesc{
		key:  "keys",
		name: "API Keys",
		columns: []column{
			{title: "NAME", flex: true},
			{title: "ROLE", width: 7},
			{title: "ACCOUNT", width: 26},
			{title: "CREATED", width: 16},
			{title: "LAST USED", width: 16},
			{title: "REVOKED", width: 7},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListAPIKeys(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, k := range pg.Items {
				rows[i] = rowData{
					cells: []string{k.Name, k.Role, strOr(k.AccountName, strOr(k.AccountID, "—")), fmtEpoch(k.CreatedAt), fmtEpochPtr(k.LastUsedAt), yn(k.RevokedAt != nil)},
					item:  k,
				}
			}
			return rows, pg.NextCursor, nil
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return keyCreatePane(ctx, ui)
			}},
			{key: "d", label: "d revoke", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				k := item.(coreapi.APIKey)
				if k.RevokedAt != nil {
					return nil // already revoked
				}
				return keyRevokeConfirm(ctx, ui, k)
			}},
		},
		detail: func(item any) []kv {
			k := item.(coreapi.APIKey)
			return []kv{
				{k: "id", v: k.ID},
				{k: "name", v: k.Name},
				{k: "role", v: k.Role},
				{k: "account", v: strOr(k.AccountName, strOr(k.AccountID, "—"))},
				{k: "account id", v: strOr(k.AccountID, "—")},
				{k: "created", v: fmtEpoch(k.CreatedAt)},
				{k: "last used", v: fmtEpochPtr(k.LastUsedAt)},
				{k: "revoked", v: fmtEpochPtr(k.RevokedAt)},
			}
		},
	}
}

func accountsDesc() resourceDesc {
	return resourceDesc{
		key:  "accounts",
		name: "Accounts",
		columns: []column{
			{title: "ID", width: 26},
			{title: "NAME", flex: true},
			{title: "MAX MBX", width: 7},
			{title: "CREATED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListAccounts(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, a := range pg.Items {
				rows[i] = rowData{
					cells: []string{a.ID, a.Name, int64Or(a.MaxMailboxes, "∞"), fmtEpoch(a.CreatedAt)},
					item:  a,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			a := item.(coreapi.Account)
			return []kv{
				{k: "id", v: a.ID},
				{k: "name", v: a.Name},
				{k: "max mailboxes", v: int64Or(a.MaxMailboxes, "∞")},
				{k: "created", v: fmtEpoch(a.CreatedAt)},
			}
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return accountFormPane(ctx, ui)
			}},
		},
	}
}

// membersDesc is the dynamic listing for one group route's members — built at
// drill-down time so the whole screenPane machinery (filter, paging, actions,
// flash) works over a nested resource.
func membersDesc(r coreapi.Route) resourceDesc {
	addr := r.Address
	return resourceDesc{
		key:  "members:" + addr,
		name: "Members — " + addr,
		columns: []column{
			{title: "MEMBER", flex: true},
			{title: "ADDED", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListMembers(ctx, addr, 100, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, m := range pg.Items {
				rows[i] = rowData{cells: []string{m.MemberAddress, fmtEpoch(m.CreatedAt)}, item: m}
			}
			return rows, pg.NextCursor, nil
		},
		actions: []action{
			{key: "a", label: "a add", run: func(ctx context.Context, ui *Options, _ any) pane {
				return memberAddFormPane(ctx, ui, addr)
			}},
			{key: "d", label: "d remove", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return memberRemoveConfirm(ctx, ui, addr, item.(coreapi.RouteMember))
			}},
		},
	}
}

// trashDesc is the dynamic trash listing for one mailbox: expunged messages
// with their purge deadline, restorable in place.
func trashDesc(mbx coreapi.Mailbox) resourceDesc {
	label := strOr(mbx.PrimaryAddress, mbx.ID)
	return resourceDesc{
		key:  "trash:" + mbx.ID,
		name: "Trash — " + label,
		columns: []column{
			{title: "EXPUNGED", width: 16},
			{title: "FROM", width: 24},
			{title: "SUBJECT", flex: true},
			{title: "PURGES", width: 16},
			{title: "SIZE", width: 9},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListTrash(ctx, mbx.ID, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, m := range pg.Items {
				rows[i] = rowData{
					cells: []string{
						fmtEpoch(m.ExpungedAt), m.EnvelopeFrom, strOr(m.Subject, "(no subject)"),
						fmtEpoch(m.PurgeAfter), fmtBytes(m.Size),
					},
					item: m,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			m := item.(coreapi.ExpungedMessageMeta)
			return []kv{
				{k: "subject", v: strOr(m.Subject, "(no subject)")},
				{k: "from", v: m.EnvelopeFrom},
				{k: "to", v: m.EnvelopeTo},
				{k: "size", v: fmtBytes(m.Size)},
				{k: "received", v: fmtEpoch(m.ReceivedAt)},
				{k: "expunged", v: fmtEpoch(m.ExpungedAt)},
				{k: "purges", v: fmtEpoch(m.PurgeAfter)},
				{k: "had labels", v: strings.Join(m.ExpungedLabels, ", ")},
				{k: "id", v: m.ID},
			}
		},
		actions: []action{
			{key: "u", label: "u restore", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				m := item.(coreapi.ExpungedMessageMeta)
				res, err := c.RestoreMessage(ctx, mbx.ID, m.ID)
				if err != nil {
					return "", err
				}
				names := make([]string, 0, len(res.Message.Labels))
				for _, l := range res.Message.Labels {
					names = append(names, l.Name)
				}
				return "restored to " + strings.Join(names, ", "), nil
			}},
		},
	}
}

// deletedMailboxesDesc lists restorable mailbox tombstones (the 7-day undo
// window). Restore is in place; a purge-deleted or expired tombstone is
// refused locally before any network call.
func deletedMailboxesDesc() resourceDesc {
	return resourceDesc{
		key:  "mailboxes:deleted",
		name: "Deleted mailboxes",
		columns: []column{
			{title: "ID", width: 26},
			{title: "PRIMARY ADDRESS", flex: true},
			{title: "DELETED", width: 16},
			{title: "RESTORABLE UNTIL", width: 16},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListDeletedMailboxes(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, m := range pg.Items {
				until := "not restorable"
				if m.Restorable {
					until = fmtEpochPtr(m.RestorableUntil)
				}
				rows[i] = rowData{
					cells: []string{m.ID, strOr(m.PrimaryAddress, "—"), fmtEpoch(m.DeletedAt), until},
					item:  m,
				}
			}
			return rows, pg.NextCursor, nil
		},
		detail: func(item any) []kv {
			m := item.(coreapi.DeletedMailbox)
			quota := "∞"
			if m.QuotaBytes != nil {
				quota = fmtBytes(*m.QuotaBytes)
			}
			return []kv{
				{k: "id", v: m.ID},
				{k: "primary address", v: strOr(m.PrimaryAddress, "—")},
				{k: "quota", v: quota},
				{k: "account", v: strOr(m.AccountID, "—")},
				{k: "deleted", v: fmtEpoch(m.DeletedAt)},
				{k: "restorable", v: yn(m.Restorable)},
				{k: "restorable until", v: fmtEpochPtr(m.RestorableUntil)},
			}
		},
		actions: []action{
			{key: "u", label: "u restore", needsRow: true, do: func(ctx context.Context, c *coreapi.Client, item any) (string, error) {
				m := item.(coreapi.DeletedMailbox)
				if !m.Restorable {
					return "", fmt.Errorf("%s is not restorable (purge delete, or the undo window elapsed)", strOr(m.PrimaryAddress, m.ID))
				}
				res, err := c.RestoreMailbox(ctx, m.ID)
				if err != nil {
					return "", err
				}
				flash := "mailbox " + strOr(res.PrimaryAddress, res.ID) + " restored"
				if res.AddressChanged {
					flash += " — primary address changed (the old one was re-routed meanwhile)"
				}
				return flash, nil
			}},
			{key: "p", label: "p purge", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				return mailboxPurgeConfirm(ctx, ui, item.(coreapi.DeletedMailbox))
			}},
		},
	}
}

// credKindLabel renders a credential's wire kind for display (app_password →
// app-password); passwords pass through unchanged.
func credKindLabel(kind string) string {
	if kind == "app_password" {
		return "app-password"
	}
	return kind
}

// credentialsDesc is the dynamic listing of one mailbox's IMAP/POP3/SMTP login
// credentials (the secrets a mail client authenticates with — distinct from the
// API keys this console uses). Management is admin-gated in core (account owner
// or system); a mailbox principal can't reach it. Built at drill-down time so
// the whole screenPane machinery works over the nested resource.
func credentialsDesc(mbx coreapi.Mailbox) resourceDesc {
	label := mailboxLabel(&mbx)
	return resourceDesc{
		key:  "credentials:" + mbx.ID,
		name: "Credentials — " + label,
		columns: []column{
			{title: "KIND", width: 13},
			{title: "USERNAME", flex: true},
			{title: "LABEL", width: 18},
			{title: "CREATED", width: 16},
			{title: "LAST USED", width: 16},
			{title: "REVOKED", width: 7},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			creds, err := c.ListCredentials(ctx, mbx.ID)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(creds))
			for i, cr := range creds {
				rows[i] = rowData{
					cells: []string{
						credKindLabel(cr.Kind), cr.Username, strOr(cr.Name, "—"),
						fmtEpoch(cr.CreatedAt), fmtEpochPtr(cr.LastUsedAt), yn(cr.RevokedAt != nil),
					},
					item: cr,
				}
			}
			return rows, "", nil // credentials are unpaginated
		},
		detail: func(item any) []kv {
			cr := item.(coreapi.Credential)
			return []kv{
				{k: "id", v: cr.ID},
				{k: "kind", v: credKindLabel(cr.Kind)},
				{k: "username", v: cr.Username},
				{k: "label", v: strOr(cr.Name, "—")},
				{k: "created", v: fmtEpoch(cr.CreatedAt)},
				{k: "last used", v: fmtEpochPtr(cr.LastUsedAt)},
				{k: "revoked", v: fmtEpochPtr(cr.RevokedAt)},
			}
		},
		actions: []action{
			{key: "n", label: "n new", run: func(ctx context.Context, ui *Options, _ any) pane {
				return credentialFormPane(ctx, ui, mbx)
			}},
			{key: "d", label: "d revoke", needsRow: true, run: func(ctx context.Context, ui *Options, item any) pane {
				cr := item.(coreapi.Credential)
				if cr.RevokedAt != nil {
					return nil // already revoked
				}
				return credentialRevokeConfirm(ctx, ui, mbx, cr)
			}},
		},
	}
}

// trafficRanges is the window vocabulary the core endpoint accepts, in cycle
// order (GET /domains/:domain/traffic ?range).
var trafficRanges = []string{"1h", "6h", "24h", "7d", "30d"}

// trafficState carries the active range for a per-domain traffic screen. It is
// mutated in place by the range-cycle action and read by fetch, so the same
// screenPane re-queries a new window without rebuilding the pane. The index is
// atomic because the action runs in a goroutine while fetch reads concurrently.
type trafficState struct{ i atomic.Int64 }

func newTrafficState() *trafficState {
	st := &trafficState{}
	for i, r := range trafficRanges {
		if r == "24h" { // product default matches the core endpoint default
			st.i.Store(int64(i))
			break
		}
	}
	return st
}

func (t *trafficState) rng() string   { return trafficRanges[int(t.i.Load())%len(trafficRanges)] }
func (t *trafficState) cycle() string { t.i.Add(1); return t.rng() }

// trafficPickerDesc is the Traffic sidebar screen: a domain list whose enter
// drills into that domain's per-event log. Traffic is indexed by the canonical
// local-party domain (core src/lib/traffic.ts), so an alias domain drills into
// an empty window — its ALIAS OF column says why.
func trafficPickerDesc() resourceDesc {
	return resourceDesc{
		key:  "traffic",
		name: "Traffic",
		columns: []column{
			{title: "DOMAIN", flex: true},
			{title: "ALIAS OF", width: 20},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pg, err := c.ListDomains(ctx, pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(pg.Items))
			for i, d := range pg.Items {
				rows[i] = rowData{
					cells: []string{d.Domain, strOr(d.AliasOf, "—")},
					item:  d,
				}
			}
			return rows, pg.NextCursor, nil
		},
		open: func(ctx context.Context, ui *Options, item any) pane {
			d := item.(coreapi.Domain)
			return newScreenPane(ctx, ui, trafficEventsDesc(d.Domain))
		},
	}
}

// eventsFreshness is the persistent caveat on the per-event log: it is the
// durable audit record queried from the Iceberg store (near-real-time, minutes
// of lag), NOT the live message stream (that's the mailbox events WebSocket).
const eventsFreshness = "durable audit log · updates within a few minutes · not the live message stream"

// trafficEventsDesc is one domain's per-event traffic log: individual delivery
// attempts, newest first, with REAL server-side keyset pagination (unlike the
// single-shot aggregate). R cycles the range in place; enter opens the full
// event detail; s opens the sampled aggregate summary (trafficDesc). `/`
// client-filters the rows already loaded.
func trafficEventsDesc(domain string) resourceDesc {
	st := newTrafficState()
	return resourceDesc{
		key:     "events:" + domain,
		name:    "Traffic — " + domain,
		caption: eventsFreshness,
		columns: []column{
			{title: "TIME", width: 19},
			{title: "SOURCE", width: 10},
			{title: "OUTCOME", width: 14},
			{title: "FROM", flex: true},
			{title: "TO", flex: true},
			{title: "SUBJECT", flex: true},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			de, err := c.GetDomainEvents(ctx, domain, st.rng(), "", "", pageLimit, cursor)
			if err != nil {
				return nil, "", err
			}
			rows := make([]rowData, len(de.Events))
			for i, e := range de.Events {
				rows[i] = rowData{
					cells: []string{
						fmtEpoch(e.EventTime), e.Source, e.Outcome,
						strOr(e.EnvelopeFrom, "—"), strOr(e.EnvelopeTo, "—"), strOr(e.Subject, "—"),
					},
					item: e,
				}
			}
			return rows, strOr(de.Cursor, ""), nil
		},
		detail: func(item any) []kv {
			e := item.(coreapi.TrafficEvent)
			return []kv{
				{k: "time", v: fmtEpoch(e.EventTime)},
				{k: "source", v: e.Source},
				{k: "outcome", v: e.Outcome},
				{k: "detail", v: strOr(e.Detail, "—")},
				{},
				{k: "from", v: strOr(e.EnvelopeFrom, "—")},
				{k: "to", v: strOr(e.EnvelopeTo, "—")},
				{k: "original", v: strOr(e.OriginalAddress, "—")},
				{k: "subject", v: strOr(e.Subject, "—")},
				{},
				{k: "matched by", v: strOr(e.MatchedBy, "—")},
				{k: "route kind", v: strOr(e.RouteKind, "—")},
				{k: "route target", v: strOr(e.RouteTarget, "—")},
				{k: "mailbox", v: strOr(e.MailboxID, "—")},
				{},
				{k: "delivery id", v: strOr(e.DeliveryID, "—")},
				{k: "message id", v: strOr(e.MessageID, "—")},
				{k: "message-id hdr", v: strOr(e.MessageIDHeader, "—")},
				{k: "size", v: int64Or(e.SizeBytes, "—")},
				{k: "blob hash", v: strOr(e.BlobHash, "—")},
				{k: "attempt", v: int32Ptr(e.Attempt)},
				{k: "response", v: strOr(e.Response, "—")},
				{},
				{v: eventsFreshness},
			}
		},
		actions: []action{
			{key: "R", label: "R range", do: func(ctx context.Context, c *coreapi.Client, _ any) (string, error) {
				return "range: " + st.cycle(), nil
			}},
			{key: "s", label: "s summary", run: func(ctx context.Context, ui *Options, _ any) pane {
				return newScreenPane(ctx, ui, trafficDesc(domain))
			}},
		},
	}
}

// int32Ptr renders a nullable queue-attempt counter (null → dash on endpoint rows).
func int32Ptr(p *int32) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *p)
}

// trafficDesc is one domain's traffic window: outcome × route-kind rows sorted
// by volume, with a trailing TOTALS row that also carries the active range so
// the current window stays visible without extra chrome. R cycles the range in
// place (mutate state → refetch). Figures are sampled Analytics-Engine
// estimates (~90-day retention); see core README §"Traffic log & analytics".
func trafficDesc(domain string) resourceDesc {
	st := newTrafficState()
	return resourceDesc{
		key:  "traffic:" + domain,
		name: "Traffic — " + domain,
		columns: []column{
			{title: "OUTCOME", flex: true},
			{title: "ROUTE KIND", width: 16},
			{title: "EVENTS", width: 12},
			{title: "BYTES", width: 12},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			rng := st.rng() // snapshot so the queried window and the TOTALS label agree
			t, err := c.GetDomainTraffic(ctx, domain, rng)
			if err != nil {
				return nil, "", err
			}
			sorted := append([]coreapi.TrafficRow(nil), t.Rows...)
			sort.Slice(sorted, func(i, j int) bool {
				if sorted[i].Events != sorted[j].Events {
					return sorted[i].Events > sorted[j].Events
				}
				return sorted[i].Outcome < sorted[j].Outcome
			})
			rows := make([]rowData, 0, len(sorted)+1)
			for _, r := range sorted {
				rows = append(rows, rowData{
					cells: []string{r.Outcome, orDash(r.RouteKind), fmtInt(r.Events), fmtBytes(r.Bytes)},
					item:  r,
				})
			}
			rows = append(rows, rowData{
				cells: []string{fmt.Sprintf("TOTALS (%s)", rng), "—", fmtInt(t.Totals.Events), fmtBytes(t.Totals.Bytes)},
				item:  coreapi.TrafficRow{Outcome: "TOTALS", Events: t.Totals.Events, Bytes: t.Totals.Bytes},
			})
			return rows, "", nil // aggregate is single-shot; no pagination
		},
		detail: func(item any) []kv {
			r := item.(coreapi.TrafficRow)
			return []kv{
				{k: "outcome", v: r.Outcome},
				{k: "route kind", v: orDash(r.RouteKind)},
				{k: "events", v: fmtInt(r.Events)},
				{k: "bytes", v: fmtBytes(r.Bytes)},
				{},
				{v: "sampled Analytics-Engine estimates · ~90-day retention"},
			}
		},
		actions: []action{
			{key: "R", label: "R range", do: func(ctx context.Context, c *coreapi.Client, _ any) (string, error) {
				return "range: " + st.cycle(), nil
			}},
		},
	}
}
