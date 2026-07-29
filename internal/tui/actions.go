package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Open-Email/cli/internal/compose"
	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// The v2 mutations: every action opens a form (huh) or a confirm gate. The
// wire semantics mirror the flag commands exactly — create-and-bind for
// mailboxes, atomic destination replace for routes/patterns (type + matching
// field together), webhook secret write-only (empty on edit = preserve),
// booleans always sent (the form was prefilled, so it is a faithful replace).

func required(name string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(name + " is required")
		}
		return nil
	}
}

func validAddress(s string) error {
	if s != "" && !strings.Contains(s, "@") {
		return errors.New("must be a full address (local@domain)")
	}
	return nil
}

func validBytesOrEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	_, err := parseBytes(s)
	return err
}

func validIntOrEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := strconv.Atoi(strings.TrimSpace(s)); err != nil {
		return errors.New("must be a whole number")
	}
	return nil
}

// boolField is a yes/no choice rendered as a two-option select list. NOT a
// huh Confirm: a Confirm's button pair only toggles on ←/→ while enter accepts
// the current value — users press enter believing they flipped it and the form
// "succeeds" as a no-op (a real field report: enabling a domain's Can send).
// A select shows the cursor ON the current value; ↑/↓ changes, enter advances.
func boolField(title, desc string, v *bool) huh.Field {
	f := huh.NewSelect[bool]().Title(title).
		Options(huh.NewOption("yes", true), huh.NewOption("no", false)).
		Value(v)
	if desc != "" {
		f = f.Description(desc)
	}
	return f
}

// --- domains ---

func domainFormPane(ctx context.Context, ui *Options, existing *coreapi.Domain) pane {
	var (
		domain  string
		owner   string
		enabled = true
		canSend = false
		canRecv = true
		fbl     = false
		dmarc   = false
		aliasOf string
	)
	title := "New domain"
	if existing != nil {
		domain = existing.Domain
		enabled, canSend, canRecv, fbl = existing.Enabled, existing.CanSend, existing.CanReceive, existing.FBL
		dmarc = existing.DMARC
		aliasOf = strOr(existing.AliasOf, "")
		title = "Edit domain " + existing.Domain
	}
	build := func() *huh.Form {
		var fields []huh.Field
		if existing == nil {
			fields = append(fields,
				huh.NewInput().
					Title("Domain").Placeholder("example.com").
					Value(&domain).Validate(required("domain")),
				// Ownership is create-only (core has no accountId patch). The
				// explicit empty-for-platform choice mirrors core's contract:
				// system keys must name an owner or deliberately mint a
				// platform domain; account keys always own their domains.
				huh.NewInput().
					Title("Owner account").
					Description("owning account id — empty mints a PLATFORM domain owned by no tenant (system keys; ignored for account keys, which always own their domains)").
					Value(&owner),
			)
		}
		fields = append(fields,
			boolField("Enabled", "", &enabled),
			boolField("Can send", "allow outbound submission from this domain", &canSend),
			boolField("Can receive", "", &canRecv),
			boolField("FBL ingestion", "parse DSN/ARF feedback reports delivered to this domain", &fbl),
			boolField("DMARC ingestion", "parse aggregate (RUA) reports delivered to this domain — not the _dmarc DNS record", &dmarc),
			huh.NewInput().Title("Alias of").
				Description("deliver this domain's mail as that domain's — empty for none (clears on edit)").
				Value(&aliasOf),
		)
		return huh.NewForm(huh.NewGroup(fields...))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		alias := strings.TrimSpace(aliasOf)
		if existing == nil {
			in := coreapi.DomainCreateInput{
				Domain: strings.TrimSpace(domain), Enabled: &enabled,
				CanSend: &canSend, CanReceive: &canRecv, FBL: &fbl, DMARC: &dmarc,
			}
			if o := strings.TrimSpace(owner); o != "" {
				in.AccountID = &o
			} else {
				in.Platform = true
			}
			if alias != "" {
				in.AliasOf = &alias
			}
			d, _, err := c.CreateDomain(sctx, in)
			if err != nil {
				return "", nil, err
			}
			return "domain " + d.Domain + " created", nil, nil
		}
		patch := map[string]any{"enabled": enabled, "canReceive": canRecv, "fbl": fbl, "dmarc": dmarc}
		// canSend is EARNED from DNS: core refuses canSend:true from an account
		// key (403 sending_not_writable), so sending it unchanged would make
		// every unrelated edit of an already-sending domain fail. Send it only
		// when the operator actually moved the toggle — turning sending OFF is
		// always allowed, and turning it ON is what legitimately needs a system
		// key or a re-run of domain create.
		if canSend != existing.CanSend {
			patch["canSend"] = canSend
		}
		if alias == "" {
			patch["aliasOf"] = nil
		} else {
			patch["aliasOf"] = alias
		}
		d, err := c.UpdateDomain(sctx, existing.Domain, patch)
		if err != nil {
			return "", nil, err
		}
		return "domain " + d.Domain + " updated", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: title, build: build, submit: submit})
}

// --- mailboxes ---

func mailboxLabel(m *coreapi.Mailbox) string {
	if a := strOr(m.PrimaryAddress, ""); a != "" {
		return a
	}
	return m.ID
}

func mailboxFormPane(ctx context.Context, ui *Options, existing *coreapi.Mailbox) pane {
	var address, quota string
	origAddress, origQuota := "", ""
	title := "New mailbox"
	addrDesc := "optional — claims the address: route + primary label are created atomically (409 address_taken if routed elsewhere)"
	if existing != nil {
		address = strOr(existing.PrimaryAddress, "")
		if existing.QuotaBytes != nil {
			quota = strconv.FormatInt(*existing.QuotaBytes, 10)
		}
		origAddress, origQuota = address, quota
		title = "Edit mailbox " + mailboxLabel(existing)
		addrDesc = "must already be routed to this mailbox (bind with a route first — address_not_routed otherwise)"
	}
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Primary address").Placeholder("alice@example.com").
				Description(addrDesc).Value(&address).Validate(validAddress),
			huh.NewInput().Title("Quota").Placeholder("1G").
				Description("e.g. 500M, 1.5G — empty for unlimited").
				Value(&quota).Validate(validBytesOrEmpty),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		addr := strings.TrimSpace(address)
		q := strings.TrimSpace(quota)
		if existing == nil {
			in := coreapi.MailboxCreateInput{}
			if addr != "" {
				in.PrimaryAddress = &addr
			}
			if q != "" {
				n, err := parseBytes(q)
				if err != nil {
					return "", nil, err
				}
				in.QuotaBytes = &n
			}
			m, err := c.CreateMailbox(sctx, in)
			if err != nil {
				return "", nil, err
			}
			return "mailbox " + mailboxLabel(m) + " created", nil, nil
		}
		patch := map[string]any{}
		if addr != origAddress && addr != "" {
			patch["primaryAddress"] = addr
		}
		if q != origQuota {
			if q == "" {
				patch["quotaBytes"] = nil // explicit null = unlimited
			} else {
				n, err := parseBytes(q)
				if err != nil {
					return "", nil, err
				}
				patch["quotaBytes"] = n
			}
		}
		if len(patch) == 0 {
			return "no changes", nil, nil
		}
		m, err := c.UpdateMailbox(sctx, existing.ID, patch)
		if err != nil {
			return "", nil, err
		}
		return "mailbox " + mailboxLabel(m) + " updated", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: title, build: build, submit: submit})
}

func mailboxDeleteConfirm(ctx context.Context, ui *Options, m coreapi.Mailbox) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete mailbox " + mailboxLabel(&m),
		body: "Soft-delete this mailbox. Delivery admission closes immediately; the mailbox " +
			"stays restorable for 7 days (openemail mailboxes restore " + m.ID + "), then is wiped. " +
			"A purge delete (no undo) is CLI-only: openemail mailboxes delete --purge.",
		verb: "delete mailbox",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			res, err := c.DeleteMailbox(sctx, m.ID, false)
			if err != nil {
				return "", err
			}
			return "mailbox deleted — restorable until " + fmtEpochPtr(res.RestorableUntil), nil
		},
	})
}

// mailboxPurgeConfirm expedites an already-soft-deleted mailbox from the trash:
// it waives the rest of the undo window (irreversible). Offered on the deleted
// list; a window-elapsed tombstone (restorable:false) is still purgeable to
// reclaim its store now instead of waiting for the deadman.
func mailboxPurgeConfirm(ctx context.Context, ui *Options, m coreapi.DeletedMailbox) pane {
	label := strOr(m.PrimaryAddress, m.ID)
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Purge deleted mailbox " + label,
		body: "Waive the rest of the undo window and wipe this mailbox now (at the ~25 min " +
			"straggler floor). It becomes non-restorable immediately — restore will fail. " +
			"This is irreversible.",
		verb: "purge mailbox",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if _, err := c.PurgeMailbox(sctx, m.ID); err != nil {
				return "", err
			}
			return "mailbox " + label + " purged — no longer restorable", nil
		},
	})
}

// buildCredentialInput assembles a create request from the form fields,
// enforcing that a password credential carries a password. Username/label are
// trimmed; an empty username is omitted so core defaults it to the primary
// address; a password is only sent for kind=password.
func buildCredentialInput(kind, username, password, name string) (coreapi.CredentialCreateInput, error) {
	in := coreapi.CredentialCreateInput{Kind: kind, Username: strings.TrimSpace(username)}
	if l := strings.TrimSpace(name); l != "" {
		in.Name = &l
	}
	if kind == "password" {
		if strings.TrimSpace(password) == "" {
			return coreapi.CredentialCreateInput{}, errors.New("a password credential needs a password")
		}
		in.Password = password
	}
	return in, nil
}

// credentialFormPane creates an IMAP/POP3/SMTP login credential on a mailbox.
// app-password mints a token revealed exactly once; password sets a plaintext
// the caller supplies. Username defaults to the mailbox primary address.
func credentialFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox) pane {
	kind := "app_password"
	username := strOr(mbx.PrimaryAddress, "")
	var password, name string
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Kind").
				Description("app passwords are generated high-entropy tokens (also usable as a mailbox API token); passwords are ones you choose, for IMAP/SMTP login").
				Options(
					huh.NewOption("Generate an app password (shown once)", "app_password"),
					huh.NewOption("Set a password (IMAP/SMTP login)", "password"),
				).
				Value(&kind),
			huh.NewInput().Title("Username").
				Description("login username (defaults to the mailbox primary address)").
				Placeholder("alice@example.com").Value(&username),
			huh.NewInput().Title("Password").
				DescriptionFunc(func() string {
					if kind == "password" {
						return "required for a password credential"
					}
					return "ignored for app-password — a token is generated"
				}, &kind).
				EchoMode(huh.EchoModePassword).Value(&password),
			huh.NewInput().Title("Label").Placeholder("optional — e.g. Thunderbird").Value(&name),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		in, err := buildCredentialInput(kind, username, password, name)
		if err != nil {
			return "", nil, err
		}
		cred, err := c.CreateCredential(sctx, mbx.ID, in)
		if err != nil {
			return "", nil, err
		}
		if cred.Token != "" {
			reveal := newNotePane("Credential created", []string{
				credKindLabel(cred.Kind) + " · username " + cred.Username,
				"",
				"App-password token — shown ONCE, copy it now:",
				"",
				cred.Token,
			})
			return "credential created for " + cred.Username, reveal, nil
		}
		return "password credential created for " + cred.Username, nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "New credential — " + mailboxLabel(&mbx), build: build, submit: submit})
}

func credentialRevokeConfirm(ctx context.Context, ui *Options, mbx coreapi.Mailbox, cred coreapi.Credential) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Revoke credential " + strOr(cred.Name, cred.Username),
		body: "This credential stops authenticating immediately. Any IMAP/POP3/SMTP client using " +
			"it will fail to log in until reconfigured with a new one. Not undoable — mint a new credential to restore access.",
		verb: "revoke credential",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.RevokeCredential(sctx, mbx.ID, cred.ID); err != nil {
				return "", err
			}
			return "credential revoked", nil
		},
	})
}

// --- routes / patterns (shared destination plumbing) ---

func destDescription(typ *string) func() string {
	return func() string {
		switch *typ {
		case "mailbox":
			return "mailbox id, or an address already routed to the mailbox (resolved on save)"
		case "webhook":
			return "https:// endpoint that will receive delivery POSTs"
		case "remote":
			return "external address to forward to"
		case "alias":
			return "address template, e.g. {1}@other.example ({n} = pattern wildcard captures)"
		case "group":
			return "groups have no single target — manage members in the route detail / CLI"
		}
		return ""
	}
}

// resolveDest turns the form's (type, target) into the wire destination field.
// A mailbox target given as an address is resolved through its route.
func resolveDest(ctx context.Context, c *coreapi.Client, typ, target string) (field string, value string, err error) {
	target = strings.TrimSpace(target)
	switch typ {
	case "mailbox":
		if target == "" {
			return "", "", errors.New("a mailbox destination needs a target")
		}
		if strings.Contains(target, "@") {
			r, rerr := c.GetRoute(ctx, target)
			if rerr != nil {
				return "", "", fmt.Errorf("resolving %s: %w", target, rerr)
			}
			if r.MailboxID == nil {
				return "", "", fmt.Errorf("%s is not a mailbox route (%s)", target, r.DestinationType)
			}
			target = *r.MailboxID
		}
		return "mailboxId", target, nil
	case "webhook":
		if target == "" {
			return "", "", errors.New("a webhook destination needs a URL")
		}
		return "webhookUrl", target, nil
	case "remote":
		if target == "" {
			return "", "", errors.New("a remote destination needs an address")
		}
		return "remoteAddress", target, nil
	case "alias":
		if target == "" {
			return "", "", errors.New("an alias destination needs an address template")
		}
		return "aliasAddress", target, nil
	case "group":
		return "", "", nil
	}
	return "", "", fmt.Errorf("unknown destination type %q", typ)
}

func currentTarget(typ string, mailboxID, webhookURL, remote, alias *string) string {
	switch typ {
	case "mailbox":
		return strOr(mailboxID, "")
	case "webhook":
		return strOr(webhookURL, "")
	case "remote":
		return strOr(remote, "")
	case "alias":
		return strOr(alias, "")
	}
	return ""
}

func routeFormPane(ctx context.Context, ui *Options, existing *coreapi.Route) pane {
	var (
		address, target, secret string
		typ                     = "mailbox"
		paced                   bool
	)
	title := "New route"
	secretDesc := "webhook only — signing secret for delivery POSTs (optional)"
	if existing != nil {
		address = existing.Address
		typ = existing.DestinationType
		target = currentTarget(typ, existing.MailboxID, existing.WebhookURL, existing.RemoteAddress, nil)
		paced = existing.Paced
		title = "Edit route " + existing.Address
		secretDesc = "webhook only — empty PRESERVES the existing secret; core never returns it"
	}
	build := func() *huh.Form {
		var fields []huh.Field
		if existing == nil {
			fields = append(fields, huh.NewInput().Title("Address").Placeholder("alice@example.com").
				Value(&address).Validate(func(s string) error {
				if err := required("address")(s); err != nil {
					return err
				}
				return validAddress(s)
			}))
		}
		fields = append(fields,
			huh.NewSelect[string]().Title("Destination type").
				Options(huh.NewOptions("mailbox", "webhook", "remote", "group")...).
				Value(&typ),
			huh.NewInput().Title("Target").DescriptionFunc(destDescription(&typ), &typ).Value(&target),
			huh.NewInput().Title("Webhook secret").Description(secretDesc).EchoMode(huh.EchoModePassword).Value(&secret),
			boolField("Paced", "stage + queue-drain instead of synchronous delivery (funnel protection for hot shared inboxes)", &paced),
		)
		return huh.NewForm(huh.NewGroup(fields...))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		field, value, err := resolveDest(sctx, c, typ, target)
		if err != nil {
			return "", nil, err
		}
		if existing == nil {
			in := coreapi.RouteCreateInput{
				Address: strings.TrimSpace(address), DestinationType: typ, Paced: paced,
				WebhookSecret: strings.TrimSpace(secret),
			}
			switch field {
			case "mailboxId":
				in.MailboxID = value
			case "webhookUrl":
				in.WebhookURL = value
			case "remoteAddress":
				in.RemoteAddress = value
			}
			r, err := c.CreateRoute(sctx, in)
			if err != nil {
				return "", nil, err
			}
			return "route " + r.Address + " created", nil, nil
		}
		// Destination replace is atomic: type + matching field together. An
		// empty secret is OMITTED so the stored one is preserved.
		patch := map[string]any{"paced": paced, "destinationType": typ}
		if field != "" {
			patch[field] = value
		}
		if typ == "webhook" && strings.TrimSpace(secret) != "" {
			patch["webhookSecret"] = strings.TrimSpace(secret)
		}
		r, err := c.UpdateRoute(sctx, existing.Address, patch)
		if err != nil {
			return "", nil, err
		}
		return "route " + r.Address + " updated", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: title, build: build, submit: submit})
}

func routeDeleteConfirm(ctx context.Context, ui *Options, r coreapi.Route) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Delete route " + r.Address,
		body: "Mail to " + r.Address + " stops resolving through this route and falls through " +
			"the routing ladder (pattern / tag-strip / catch-all, else 550).",
		verb: "delete route",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.DeleteRoute(sctx, r.Address); err != nil {
				return "", err
			}
			return "route " + r.Address + " deleted", nil
		},
	})
}

func patternFormPane(ctx context.Context, ui *Options, existing *coreapi.Pattern) pane {
	var (
		domain, pattern, priority, target, secret string
		typ                                       = "mailbox"
		paced                                     bool
	)
	title := "New pattern"
	if existing != nil {
		domain, pattern = existing.Domain, existing.Pattern
		priority = strconv.Itoa(existing.Priority)
		typ = existing.DestinationType
		target = currentTarget(typ, existing.MailboxID, existing.WebhookURL, existing.RemoteAddress, existing.AliasAddress)
		paced = existing.Paced
		title = fmt.Sprintf("Edit pattern %s@%s", existing.Pattern, existing.Domain)
	}
	build := func() *huh.Form {
		var fields []huh.Field
		if existing == nil {
			fields = append(fields,
				huh.NewInput().Title("Domain").Placeholder("example.com").
					Value(&domain).Validate(required("domain")),
				huh.NewInput().Title("Pattern").Placeholder("support-*").
					Description("local-part glob; * and ? wildcards").
					Value(&pattern).Validate(required("pattern")),
			)
		}
		fields = append(fields,
			huh.NewInput().Title("Priority").Placeholder("100").
				Description("lower wins; empty keeps the default").
				Value(&priority).Validate(validIntOrEmpty),
			huh.NewSelect[string]().Title("Destination type").
				Options(huh.NewOptions("mailbox", "webhook", "remote", "alias")...).
				Value(&typ),
			huh.NewInput().Title("Target").DescriptionFunc(destDescription(&typ), &typ).Value(&target),
			huh.NewInput().Title("Webhook secret").
				Description("webhook only — empty preserves any existing secret").
				EchoMode(huh.EchoModePassword).Value(&secret),
			boolField("Paced", "", &paced),
		)
		return huh.NewForm(huh.NewGroup(fields...))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		field, value, err := resolveDest(sctx, c, typ, target)
		if err != nil {
			return "", nil, err
		}
		prio := strings.TrimSpace(priority)
		if existing == nil {
			in := coreapi.PatternCreateInput{
				Domain: strings.TrimSpace(domain), Pattern: strings.TrimSpace(pattern),
				DestinationType: typ, Paced: paced, WebhookSecret: strings.TrimSpace(secret),
			}
			if prio != "" {
				n, _ := strconv.Atoi(prio)
				in.Priority = &n
			}
			switch field {
			case "mailboxId":
				in.MailboxID = value
			case "webhookUrl":
				in.WebhookURL = value
			case "remoteAddress":
				in.RemoteAddress = value
			case "aliasAddress":
				in.AliasAddress = value
			}
			p, err := c.CreatePattern(sctx, in)
			if err != nil {
				return "", nil, err
			}
			return fmt.Sprintf("pattern %s@%s created", p.Pattern, p.Domain), nil, nil
		}
		patch := map[string]any{"paced": paced, "destinationType": typ}
		if field != "" {
			patch[field] = value
		}
		if prio != "" {
			n, _ := strconv.Atoi(prio)
			patch["priority"] = n
		}
		if typ == "webhook" && strings.TrimSpace(secret) != "" {
			patch["webhookSecret"] = strings.TrimSpace(secret)
		}
		p, err := c.UpdatePattern(sctx, existing.ID, patch)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("pattern %s@%s updated", p.Pattern, p.Domain), nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: title, build: build, submit: submit})
}

func patternDeleteConfirm(ctx context.Context, ui *Options, p coreapi.Pattern) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: fmt.Sprintf("Delete pattern %s@%s", p.Pattern, p.Domain),
		body: "Addresses matching this pattern stop resolving through it and fall through " +
			"the routing ladder.",
		verb: "delete pattern",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.DeletePattern(sctx, p.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("pattern %s@%s deleted", p.Pattern, p.Domain), nil
		},
	})
}

// --- API keys ---

// keyCreatePane opens the new-key form. For system principals it first loads
// the accounts list so the tenant is PICKED, not typed (ULIDs are hostile).
func keyCreatePane(ctx context.Context, ui *Options) pane {
	if ui.Role != coreapi.PrincipalSystem {
		return keyFormPane(ctx, ui, nil)
	}
	return newLoaderPane(ctx, ui, "New API key", func(lctx context.Context, c *coreapi.Client) (pane, error) {
		accounts, err := coreapi.Depaginate(lctx, func(dctx context.Context, cursor string) (coreapi.Page[coreapi.Account], error) {
			return c.ListAccounts(dctx, 200, cursor)
		})
		if err != nil {
			return nil, err
		}
		return keyFormPane(ctx, ui, accounts), nil
	})
}

func keyFormPane(ctx context.Context, ui *Options, accounts []coreapi.Account) pane {
	var (
		name      string
		role      = coreapi.PrincipalAccount
		accountID string
	)
	build := func() *huh.Form {
		fields := []huh.Field{
			huh.NewInput().Title("Name").Placeholder("ci @ builder").
				Value(&name).Validate(required("name")),
		}
		if ui.Role == coreapi.PrincipalSystem {
			acctOpts := make([]huh.Option[string], 0, len(accounts)+1)
			acctOpts = append(acctOpts, huh.NewOption("platform (no account)", ""))
			for _, a := range accounts {
				acctOpts = append(acctOpts, huh.NewOption(a.Name+" — "+a.ID, a.ID))
			}
			fields = append(fields,
				huh.NewSelect[string]().Title("Role").
					Options(huh.NewOptions(coreapi.PrincipalAccount, coreapi.PrincipalSystem)...).
					Value(&role),
				huh.NewSelect[string]().Title("Account").
					Description("who the key acts as (account-role keys only; ignored for system)").
					Options(acctOpts...).
					Value(&accountID),
			)
		}
		return huh.NewForm(huh.NewGroup(fields...))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		acct := strings.TrimSpace(accountID)
		if role == coreapi.PrincipalSystem {
			acct = "" // system keys are platform-scoped by definition
		}
		ck, err := c.CreateAPIKey(sctx, strings.TrimSpace(name), role, acct)
		if err != nil {
			return "", nil, err
		}
		scope := "platform"
		if ck.AccountID != nil && *ck.AccountID != "" {
			scope = "account " + *ck.AccountID
		}
		reveal := newNotePane("API key created", []string{
			"Name: " + ck.Name + " · role " + ck.Role + " · " + scope,
			"",
			"Token — shown ONCE, copy it now:",
			"",
			ck.Token,
		})
		return "key " + ck.Name + " created", reveal, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "New API key", build: build, submit: submit})
}

func keyRevokeConfirm(ctx context.Context, ui *Options, k coreapi.APIKey) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Revoke API key " + k.Name,
		body: "The key stops authenticating immediately. If it is the key THIS console is " +
			"using, you will be locked out until you log in again.",
		verb: "revoke key",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.RevokeAPIKey(sctx, k.ID); err != nil {
				return "", err
			}
			return "key " + k.Name + " revoked", nil
		},
	})
}

// --- accounts (system) ---

func accountFormPane(ctx context.Context, ui *Options) pane {
	var name, maxMbx string
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Name").Placeholder("Acme Corp").
				Value(&name).Validate(required("name")),
			huh.NewInput().Title("Max mailboxes").Placeholder("50").
				Description("cap on live mailboxes — empty for unlimited").
				Value(&maxMbx).Validate(validIntOrEmpty),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		var capPtr *int64
		if m := strings.TrimSpace(maxMbx); m != "" {
			n, _ := strconv.ParseInt(m, 10, 64)
			capPtr = &n
		}
		a, err := c.CreateAccount(sctx, strings.TrimSpace(name), capPtr)
		if err != nil {
			return "", nil, err
		}
		// A keyless account is unusable — bootstrap one and reveal it once.
		// The account exists either way; a failed mint is reported, not fatal.
		lines := []string{"Account: " + a.Name, "Id: " + a.ID, ""}
		ck, kerr := c.CreateAPIKey(sctx, "bootstrap", coreapi.PrincipalAccount, a.ID)
		if kerr != nil {
			lines = append(lines,
				"API key mint FAILED: "+kerr.Error(),
				"Mint one from the API Keys screen (n), or: openemail keys create bootstrap --account "+a.ID)
		} else {
			lines = append(lines,
				"API key \""+ck.Name+"\" (role "+ck.Role+") — token shown ONCE, copy it now:",
				"",
				ck.Token)
		}
		return "account " + a.Name + " created (" + a.ID + ")", newNotePane("Account created", lines), nil
	}
	return newFormPane(ctx, ui, formSpec{title: "New account", build: build, submit: submit})
}

// --- group members ---

func memberAddFormPane(ctx context.Context, ui *Options, groupAddress string) pane {
	var member string
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Member address").Placeholder("bob@example.com").
				Description("added to " + groupAddress + " — expansion dedupes by identity, nesting ≤ 3").
				Value(&member).Validate(func(s string) error {
				if err := required("member address")(s); err != nil {
					return err
				}
				return validAddress(s)
			}),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		m := strings.TrimSpace(member)
		if err := c.AddMember(sctx, groupAddress, m); err != nil {
			return "", nil, err
		}
		return m + " added", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{title: "Add member to " + groupAddress, build: build, submit: submit})
}

func memberRemoveConfirm(ctx context.Context, ui *Options, groupAddress string, m coreapi.RouteMember) pane {
	return newConfirmPane(ctx, ui, confirmSpec{
		title: "Remove " + m.MemberAddress,
		body:  "Mail to " + groupAddress + " stops fanning out to " + m.MemberAddress + ". Re-adding is a one-keystroke undo.",
		verb:  "remove member",
		submit: func(sctx context.Context, c *coreapi.Client) (string, error) {
			if err := c.RemoveMember(sctx, groupAddress, m.MemberAddress); err != nil {
				return "", err
			}
			return m.MemberAddress + " removed", nil
		},
	})
}

// --- message labels ---

// diffLabels computes the PATCH that turns current memberships into selected.
// Additions apply before removals server-side, so a move never passes through
// a zero-label state.
func diffLabels(current, selected []string) coreapi.PatchInput {
	has := func(list []string, s string) bool {
		for _, l := range list {
			if l == s {
				return true
			}
		}
		return false
	}
	var in coreapi.PatchInput
	for _, s := range selected {
		if !has(current, s) {
			in.LabelsAdd = append(in.LabelsAdd, s)
		}
	}
	for _, c := range current {
		if !has(selected, c) {
			in.LabelsRemove = append(in.LabelsRemove, c)
		}
	}
	return in
}

func labelEditFormPane(ctx context.Context, ui *Options, mailboxID string, m coreapi.MessageMeta, all []coreapi.LabelInfo) pane {
	current := make([]string, 0, len(m.Labels))
	for _, l := range m.Labels {
		current = append(current, l.Name)
	}
	selected := append([]string{}, current...)
	names := make([]string, 0, len(all))
	for _, l := range all {
		names = append(names, l.Name)
	}
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewMultiSelect[string]().Title("Labels").
				Description("space toggles · enter saves — removing the last label is refused (use d for trash)").
				Options(huh.NewOptions(names...)...).
				Value(&selected),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		if len(selected) == 0 {
			return "", nil, errors.New("keep at least one label — use d to move the message to trash")
		}
		in := diffLabels(current, selected)
		if len(in.LabelsAdd) == 0 && len(in.LabelsRemove) == 0 {
			return "no changes", nil, nil
		}
		if _, err := c.PatchMessage(sctx, mailboxID, m.ID, in); err != nil {
			return "", nil, err
		}
		return "labels updated", nil, nil
	}
	return newFormPane(ctx, ui, formSpec{
		title: "Labels — " + strOr(m.Subject, "(no subject)"), build: build, submit: submit,
	})
}

// --- compose (send from the mailbox context) ---

// composeFormPane sends a plain-text message from the mailbox the user is
// browsing, mirroring `openemail send` compose mode exactly: same MIME
// builder, same /deliver/outbound submission, envelope = From/To. The delivery
// id is minted once per form instance, so a submit that errors and is retried
// after editing converges idempotently instead of double-sending.
func composeFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox) pane {
	var (
		from       = strOr(mbx.PrimaryAddress, "")
		to         string
		subject    string
		body       string
		save       = true
		deliveryID = compose.NewDeliveryID()
	)
	build := func() *huh.Form {
		return huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("From").
				Description("must resolve to this mailbox on a sendable domain (DMARC-aligned signing)").
				Value(&from).Validate(func(s string) error {
				if err := required("from")(s); err != nil {
					return err
				}
				return validAddress(s)
			}),
			huh.NewInput().Title("To").Placeholder("someone@example.com").
				Value(&to).Validate(func(s string) error {
				if err := required("to")(s); err != nil {
					return err
				}
				return validAddress(s)
			}),
			huh.NewInput().Title("Subject").Value(&subject),
			huh.NewText().Title("Body").Lines(8).
				Description("plain text · tab moves to the next field").
				Value(&body),
			// The last field completes the form, so make it the explicit send
			// step — both options SEND; they differ only in whether core keeps
			// a copy under the Sent label (?save=true). Esc still cancels.
			huh.NewSelect[bool]().Title("Send").
				Description("enter sends · esc cancels").
				Options(
					huh.NewOption("send and save a copy to Sent", true),
					huh.NewOption("send without a Sent copy", false),
				).
				Value(&save),
		))
	}
	submit := func(sctx context.Context, c *coreapi.Client) (string, pane, error) {
		f, t := strings.TrimSpace(from), strings.TrimSpace(to)
		msg := compose.TextMessage(f, t, strings.TrimSpace(subject), body)
		opts := coreapi.OutboundOptions{
			EnvelopeFrom: f, EnvelopeTo: t, DeliveryID: deliveryID, Save: &save,
		}
		getBody := func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(msg)), nil
		}
		res, _, err := c.SubmitOutbound(sctx, opts, getBody, int64(len(msg)))
		if err != nil {
			return "", nil, err
		}
		flash := "sent — " + res.Status
		switch res.Status {
		case "delivered":
			if res.Duplicate {
				flash = "sent — delivered (duplicate, already stored)"
			}
		case "queued":
			flash = "sent — queued (webhook/remote/group/relay)"
		case "filtered":
			flash = "sent — filtered by the recipient's Sieve script"
		}
		if res.SentCopy != "" {
			flash += " · Sent copy stored"
		}
		return flash, nil, nil
	}
	return newFormPane(ctx, ui, formSpec{
		title: "Compose — from " + strOr(mbx.PrimaryAddress, mbx.ID), build: build, submit: submit,
	})
}
