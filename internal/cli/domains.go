package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newDomainsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "Manage domains",
	}
	cmd.AddCommand(
		newDomainListCmd(a),
		newDomainCreateCmd(a),
		newDomainGetCmd(a),
		newDomainUpdateCmd(a),
		newDomainDNSCmd(a),
		newDomainHostnamesCmd(a),
		newDomainDeleteCmd(a),
		newDomainTrafficCmd(a),
		newDomainEventsCmd(a),
		newDomainDmarcCmd(a),
		newDomainDmarcSourcesCmd(a),
		newDomainDmarcReportsCmd(a),
		newEventWebhookCmd(a, eventWebhookDomain),
	)
	return cmd
}

func newDomainListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List domains",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Domain
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Domain], error) {
					return client.ListDomains(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Domain]
				page, err = client.ListDomains(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"domains": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, d := range items {
					rows = append(rows, []string{
						d.Domain, boolYN(d.Enabled), boolYN(d.Receiving), boolYN(d.Sending),
						boolYN(d.FBL), boolYN(d.DMARC), strOr(d.AliasOf, "—"),
					})
				}
				printTable(w, a.out, []string{"DOMAIN", "ENABLED", "RECEIVING", "SENDING", "FBL", "DMARC RUA", "ALIAS OF"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	return cmd
}

func newDomainCreateCmd(a *app) *cobra.Command {
	var (
		enabled, sendVerified, fbl, dmarc, platform bool
		jmap, dav, itip, sendOnly, receiveOnly      bool
		subaddressing                               bool
		aliasOf, account                            string
	)
	cmd := &cobra.Command{
		Use:   "create <domain>",
		Short: "Create a domain",
		Long: "Create a domain. Booleans default to core's values when omitted " +
			"(enabled true; send-only/fbl/dmarc false); pass e.g. --send-only or --enabled=false to override.\n\n" +
			"Sending is EARNED, not set: publish the bounce and DKIM records and re-run this command. " +
			"--send-verified is the operator override for a platform domain that never onboards (system keys only; " +
			"ignored for account keys).\n\n" +
			"--send-only creates a SEND-ONLY domain: its mailboxes stay with another provider and only sending " +
			"moves here. The platform then treats the domain as external for delivery (mail from your own " +
			"mailboxes to it is relayed to its MX like any other address, group members there are forwards), " +
			"the MX record is left out of the DNS checklist, and mail reaching our MX for it directly is refused. " +
			"Flip it later with `domains update --send-only=false`.\n\n" +
			"System keys must choose ownership explicitly: --account <id> for a tenant domain, or " +
			"--platform for a platform domain owned by no account (operator-only; invisible to every tenant). " +
			"Account keys need neither — they always own what they create.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if platform && cmd.Flags().Changed("account") {
				return usageError(errors.New("--account and --platform are mutually exclusive"))
			}
			in := coreapi.DomainCreateInput{
				Domain:        args[0],
				Enabled:       boolPtrIfChanged(cmd, "enabled", enabled),
				SendVerified:  boolPtrIfChanged(cmd, "send-verified", sendVerified),
				SendOnly:      boolPtrIfChanged(cmd, "send-only", sendOnly),
				ReceiveOnly:   boolPtrIfChanged(cmd, "receive-only", receiveOnly),
				FBL:           boolPtrIfChanged(cmd, "fbl", fbl),
				DMARC:         boolPtrIfChanged(cmd, "dmarc", dmarc),
				JMAP:          boolPtrIfChanged(cmd, "jmap", jmap),
				DAV:           boolPtrIfChanged(cmd, "dav", dav),
				ITIP:          boolPtrIfChanged(cmd, "itip", itip),
				Subaddressing: boolPtrIfChanged(cmd, "subaddressing", subaddressing),
				Platform:      platform,
			}
			if cmd.Flags().Changed("alias-of") {
				if aliasOf == "" {
					return usageError(errors.New("--alias-of must not be empty"))
				}
				in.AliasOf = &aliasOf
			}
			if cmd.Flags().Changed("account") {
				if account == "" {
					return usageError(errors.New("--account must not be empty"))
				}
				in.AccountID = &account
			}
			d, records, err := client.CreateDomain(cmd.Context(), in)
			if err != nil {
				// The refusal carries the record to publish. Render it instead of
				// a bare error code: a first-run customer's next action IS this
				// record, and deriving its name or the account token client-side
				// would duplicate platform config and drift.
				if rec, ok := coreapi.VerificationRecord(err); ok {
					a.out.Emit(map[string]any{"error": "domain_not_verified", "record": rec}, func(w io.Writer) {
						a.out.Warnf("%s is not verified yet.", args[0])
						fmt.Fprintln(w, "Publish this DNS record, then run the same command again:")
						fmt.Fprintln(w)
						printDNSRecords(w, a.out, []coreapi.DNSRecordCheck{{DNSRecord: rec}}, false)
					})
					return errSilent
				}
				return err
			}
			a.out.Emit(map[string]any{"domain": d, "records": records}, func(w io.Writer) {
				a.out.Successf("Domain %s is verified", d.Domain)
				printDomain(w, a.out, d)
				printOnboardingNext(w, a.out, d, records)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", true, "domain is enabled")
	cmd.Flags().BoolVar(&sendVerified, "send-verified", false, "(system keys) mark the send records as already proven — for a platform domain that never onboards; account keys earn it from DNS and this flag is ignored")
	cmd.Flags().BoolVar(&sendOnly, "send-only", false, "SEND-ONLY domain: mailboxes stay with another provider, only sending moves here — the platform relays mail for it to its own MX, asks for no MX record, and refuses mail reaching our MX for it")
	cmd.Flags().BoolVar(&receiveOnly, "receive-only", false, "RECEIVE-ONLY domain: mail arrives here, outbound goes through another provider — the bounce and DKIM records leave the DNS checklist and the verdict, and sending is never activated. Mutually exclusive with --send-only")
	cmd.Flags().BoolVar(&fbl, "fbl", false, "FBL ingestion domain (parses DSN/ARF reports; mutually exclusive with alias)")
	cmd.Flags().BoolVar(&dmarc, "dmarc", false, "DMARC report-ingestion domain: swallows every local part and parses arriving aggregate (RUA) reports — NOT the _dmarc DNS record")
	cmd.Flags().BoolVar(&jmap, "jmap", false, "JMAP autodiscovery: adds the _jmap._tcp SRV record to this domain's DNS checklist")
	cmd.Flags().BoolVar(&dav, "dav", false, "CalDAV/CardDAV autodiscovery: adds the _caldavs._tcp/_carddavs._tcp SRV + TXT records to the checklist")
	cmd.Flags().BoolVar(&itip, "itip", false, "inbound iTIP auto-apply: file arriving invitations into the recipient's calendar (off by default — it makes the calendar a write surface for arriving mail)")
	cmd.Flags().BoolVar(&subaddressing, "subaddressing", true, "plus-addressing (on by default): mail to alice+anything@ reaches alice@, and alice+*@ is a permitted sender family; --subaddressing=false makes + an ordinary character on this domain")
	cmd.Flags().StringVar(&aliasOf, "alias-of", "", "make this domain an alias of another")
	cmd.Flags().StringVar(&account, "account", "", "owning account id (system keys; account keys always own their domains)")
	cmd.Flags().BoolVar(&platform, "platform", false, "platform domain owned by no account (system keys only; invisible to tenants)")
	return cmd
}

// newDomainDNSCmd exposes the health check. It REPORTS only: it never creates a
// domain and never activates sending — `domains create` (create-or-advance) is
// the single path that moves a domain's state, so there is one writer of it.
func newDomainDNSCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "dns <domain>",
		Short: "Show the DNS records a domain needs and whether each is live",
		Long: "Show the DNS records this domain needs (MX for receiving; the oe-bounce CNAME and both " +
			"DKIM CNAMEs for sending; a recommended DMARC; and the JMAP/DAV SRV records where enabled) " +
			"and whether each resolves.\n\n" +
			"The \"spf\" kind is that oe-bounce CNAME, not a TXT record at your apex: outbound mail " +
			"carries a return path inside the subdomain, so SPF is evaluated — and aligned — there.\n\n" +
			"Liveness is a resolver view, so it can lag your DNS provider by a few minutes. A verdict is " +
			"cached briefly; --force re-queries now. When DNS cannot be queried at all the records are " +
			"still listed with liveness shown as \"?\" — unknown, not missing.\n\n" +
			"This command only reports. To activate sending, publish the records and re-run " +
			"`openemail domains create <domain>`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.CheckDomainDNS(cmd.Context(), args[0], force)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				if res.ResolverUnavailable {
					a.out.Warnf("DNS could not be queried — liveness is unknown, not failing.")
				}
				printDNSRecords(w, a.out, res.Records, true)
				if res.Cached {
					a.out.Msgf("%s", a.out.Dim("Cached verdict; pass --force to re-query."))
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "bypass the cached verdict and re-query DNS now")
	return cmd
}

func newDomainGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <domain>",
		Short: "Show a domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			d, err := client.GetDomain(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(d, func(w io.Writer) { printDomain(w, a.out, d) })
			return nil
		},
	}
}

func newDomainUpdateCmd(a *app) *cobra.Command {
	var (
		enabled, fbl, dmarc   bool
		jmap, dav, itip       bool
		subaddressing         bool
		sendOnly, receiveOnly bool
		aliasOf               string
		clearAlias            bool
	)
	cmd := &cobra.Command{
		Use:   "update <domain>",
		Short: "Update a domain",
		Long: "Update a domain's own settings. Sending is earned from DNS (re-run `domains create`), and " +
			"holding or stopping it is an operator action: openemail admin hold domain <domain> --pause|--stop, " +
			"openemail admin verify-sending <domain>.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if clearAlias && cmd.Flags().Changed("alias-of") {
				return usageError(errors.New("--clear-alias and --alias-of are mutually exclusive"))
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("enabled") {
				patch["enabled"] = enabled
			}
			// The owner's MODE: --send-only turns receiving off here (the domain's
			// own MX becomes the authority for its mail), --send-only=false turns
			// it back on.
			if cmd.Flags().Changed("send-only") {
				patch["sendOnly"] = sendOnly
			}
			// Its mirror: --receive-only stops asking for the send records (and
			// stops sending being activated), --receive-only=false asks again.
			if cmd.Flags().Changed("receive-only") {
				patch["receiveOnly"] = receiveOnly
			}
			if cmd.Flags().Changed("fbl") {
				patch["fbl"] = fbl
			}
			if cmd.Flags().Changed("dmarc") {
				patch["dmarc"] = dmarc
			}
			if cmd.Flags().Changed("jmap") {
				patch["jmap"] = jmap
			}
			if cmd.Flags().Changed("dav") {
				patch["dav"] = dav
			}
			if cmd.Flags().Changed("itip") {
				patch["itip"] = itip
			}
			if cmd.Flags().Changed("subaddressing") {
				patch["subaddressing"] = subaddressing
			}
			if clearAlias {
				patch["aliasOf"] = nil
			} else if cmd.Flags().Changed("alias-of") {
				if aliasOf == "" {
					return usageError(errors.New("--alias-of must not be empty; use --clear-alias to remove an alias"))
				}
				patch["aliasOf"] = aliasOf
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update"))
			}
			d, err := client.UpdateDomain(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(d, func(w io.Writer) {
				a.out.Successf("Updated domain %s", d.Domain)
				printDomain(w, a.out, d)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", false, "set enabled")
	cmd.Flags().BoolVar(&sendOnly, "send-only", false, "make the domain SEND-ONLY: mailboxes stay with another provider, only sending is here — the platform relays mail for it to its own MX and drops the MX record from the DNS checklist. --send-only=false turns receiving here back on")
	cmd.Flags().BoolVar(&receiveOnly, "receive-only", false, "make the domain RECEIVE-ONLY: mail arrives here, outbound goes through another provider — the bounce and DKIM records leave the DNS checklist and the verdict. --receive-only=false asks for them again")
	cmd.Flags().BoolVar(&fbl, "fbl", false, "set the FBL ingestion flag")
	cmd.Flags().BoolVar(&dmarc, "dmarc", false, "set the DMARC report-ingestion flag (aggregate/RUA parsing — NOT the _dmarc DNS record)")
	cmd.Flags().BoolVar(&jmap, "jmap", false, "set JMAP autodiscovery (adds the _jmap._tcp SRV record to the DNS checklist)")
	cmd.Flags().BoolVar(&dav, "dav", false, "set CalDAV/CardDAV autodiscovery (adds the _caldavs._tcp/_carddavs._tcp SRV + TXT records)")
	cmd.Flags().BoolVar(&itip, "itip", false, "set inbound iTIP auto-apply: file arriving invitations into the recipient's calendar (off by default — it makes the calendar a write surface for arriving mail)")
	cmd.Flags().BoolVar(&subaddressing, "subaddressing", true, "set plus-addressing: whether mail to alice+anything@ reaches alice@ and alice+*@ may send (on by default); --subaddressing=false makes + an ordinary character on this domain")
	cmd.Flags().StringVar(&aliasOf, "alias-of", "", "make this domain an alias of another")
	cmd.Flags().BoolVar(&clearAlias, "clear-alias", false, "clear the alias (make it a normal domain)")
	return cmd
}

func newDomainDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <domain>",
		Aliases: []string{"rm"},
		Short:   "Delete a domain (refused while routes/patterns/aliases reference it)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete domain %s?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			if err := client.DeleteDomain(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "domain": args[0]}, func(w io.Writer) {
				a.out.Successf("Deleted domain %s", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newDomainTrafficCmd(a *app) *cobra.Command {
	var rng string
	cmd := &cobra.Command{
		Use:   "traffic <domain>",
		Short: "Show traffic aggregates for a domain (sampled estimates)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			tr, err := client.GetDomainTraffic(cmd.Context(), args[0], rng)
			if err != nil {
				return err
			}
			a.out.Emit(tr, func(w io.Writer) {
				a.out.Msgf("%s — %s (estimated, ~%dd retention)", a.out.Bold(tr.Domain), tr.Range, tr.RetentionDays)
				a.out.Msgf("  total: %d events, %s", tr.Totals.Events, fmtBytes(tr.Totals.Bytes))
				rows := make([][]string, 0, len(tr.Rows))
				for _, r := range tr.Rows {
					rows = append(rows, []string{r.Outcome, r.RouteKind, fmt.Sprintf("%d", r.Events), fmtBytes(r.Bytes)})
				}
				printTable(w, a.out, []string{"OUTCOME", "ROUTE KIND", "EVENTS", "BYTES"}, rows)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&rng, "range", "24h", "time range: 1h|6h|24h|7d|30d")
	return cmd
}

func newDomainEventsCmd(a *app) *cobra.Command {
	var (
		rng, outcome, source, cursor string
		limit                        int
		all                          bool
	)
	cmd := &cobra.Command{
		Use:   "events <domain>",
		Short: "Browse the per-event traffic log for a domain (authoritative, near-real-time)",
		Long: "List individual traffic events (from/to/time/outcome/subject/detail), newest first, " +
			"from the durable Iceberg log — distinct from the sampled `traffic` aggregate and from the " +
			"live mailbox event stream. Updated within a few minutes of delivery.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			domain := args[0]
			var events []coreapi.TrafficEvent
			next := ""
			if all {
				events, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.TrafficEvent], error) {
					de, e := client.GetDomainEvents(ctx, domain, rng, outcome, source, limit, cur)
					if e != nil {
						return coreapi.Page[coreapi.TrafficEvent]{}, e
					}
					return coreapi.Page[coreapi.TrafficEvent]{Items: de.Events, NextCursor: strOr(de.NextCursor, "")}, nil
				})
			} else {
				var de *coreapi.DomainEvents
				de, err = client.GetDomainEvents(ctx, domain, rng, outcome, source, limit, cursor)
				if err == nil {
					events, next = de.Events, strOr(de.NextCursor, "")
				}
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"events": events, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(events))
				for _, e := range events {
					rows = append(rows, []string{
						fmtEpoch(e.EventTime), e.Source, e.Outcome,
						truncate(strOr(e.EnvelopeFrom, "—"), 24),
						truncate(strOr(e.EnvelopeTo, "—"), 24),
						truncate(strOr(e.Subject, "—"), 32),
						truncate(strOr(e.Detail, "—"), 20),
					})
				}
				printTable(w, a.out, []string{"TIME", "SOURCE", "OUTCOME", "FROM", "TO", "SUBJECT", "DETAIL"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&rng, "range", "24h", "time range: 1h|6h|24h|7d|30d")
	cmd.Flags().StringVar(&outcome, "outcome", "", "filter by outcome (e.g. delivered, bounced, relayed)")
	cmd.Flags().StringVar(&source, "source", "", "filter by source (e.g. inbound, outbound, relay, webhook)")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	return cmd
}

func printDomain(w io.Writer, p *Printer, d *coreapi.Domain) {
	rows := [][]string{
		{"Domain", d.Domain},
		// One row per direction, each naming WHY when the answer is no: a domain
		// held for suspension, one that was never set up to send, and one whose
		// owner switched it off are three different next steps.
		{"Receiving", receiveMode(d)},
		{"Sending", sendingMode(d)},
		{"Enabled", boolYN(d.Enabled)},
		{"FBL", boolYN(d.FBL)},
		{"DMARC ingestion", boolYN(d.DMARC)},
		{"JMAP autodiscovery", boolYN(d.JMAP)},
		{"DAV autodiscovery", boolYN(d.DAV)},
		{"Inbound iTIP", boolYN(d.ITIP)},
		{"Plus-addressing", boolYN(d.Subaddressing)},
		{"Alias of", strOr(d.AliasOf, "—")},
		{"Account", strOr(d.AccountID, "platform (no account)")},
		{"Created", fmtEpoch(d.CreatedAt)},
	}
	if d.DNSStatus != nil {
		rows = append(rows, []string{"DNS", fmtDNS(d.DNSStatus)})
	}
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)
}

func fmtDNS(s *coreapi.DNSStatus) string {
	part := func(name string, v *bool) string {
		if v == nil {
			return name + "=?"
		}
		return name + "=" + boolYN(*v)
	}
	out := fmt.Sprintf("%s %s %s %s", part("mx", s.MX), part("spf", s.SPF), part("dkim", s.DKIM), part("dmarc", s.DMARC))
	if s.JMAP != nil {
		out += " " + part("jmap", s.JMAP)
	}
	if s.DAV != nil {
		out += " " + part("dav", s.DAV)
	}
	return out
}

// printDNSRecords renders records as copy-pasteable rows. withLiveness adds the
// resolver verdict column; "?" there means UNKNOWN (the resolver could not be
// reached), which is deliberately distinct from a "no" the customer can fix.
func printDNSRecords(w io.Writer, p *Printer, records []coreapi.DNSRecordCheck, withLiveness bool) {
	head := []string{"KIND", "TYPE", "NAME", "VALUE", "REQUIRED"}
	if withLiveness {
		head = append(head, "LIVE")
	}
	rows := make([][]string, 0, len(records))
	for _, r := range records {
		value := r.Value
		// MX and SRV carry their numeric fields outside `value`, exactly as a DNS
		// UI asks for them; show them so a row is usable as-is. An SRV needs all
		// FOUR — priority, weight, port, target — and dropping the weight made the
		// _jmap._tcp and _caldavs._tcp rows silently incomplete on a surface whose
		// whole promise is "publish exactly this".
		if r.Priority != nil {
			value = fmt.Sprintf("%d %s", *r.Priority, value)
		}
		if r.Weight != nil {
			value = fmt.Sprintf("%s weight %d", value, *r.Weight)
		}
		if r.Port != nil {
			value = fmt.Sprintf("%s (port %d)", value, *r.Port)
		}
		// Core scores this record against value ∪ accept, so an alternate is not
		// trivia: a domain that claimed a vanity DAV hostname but still publishes
		// the platform target is LIVE while the VALUE column names a host it has
		// never published. Printing only the preferred target would have the
		// customer "fix" a working record.
		if len(r.Accept) > 0 {
			value = fmt.Sprintf("%s [or %s]", value, strings.Join(r.Accept, ", "))
		}
		row := []string{r.Kind, r.Type, r.Name, value, boolYN(r.Required)}
		if withLiveness {
			row = append(row, boolYNUnknown(r.OK))
		}
		rows = append(rows, row)
	}
	printTable(w, p, head, rows)
}

// printOnboardingNext tells the customer what is left. Sending is EARNED by
// publishing the oe-bounce CNAME + both DKIM CNAMEs and re-running create — it
// is not a flag they can set, so pointing at the records is the only honest
// next step.
func printOnboardingNext(w io.Writer, p *Printer, d *coreapi.Domain, records []coreapi.DNSRecordCheck) {
	if d.Sending || len(records) == 0 {
		return
	}
	var pending []coreapi.DNSRecordCheck
	for _, r := range records {
		if r.OK == nil || !*r.OK {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Still to publish (re-run this command afterwards to activate sending):")
	printDNSRecords(w, p, pending, true)
}

// receiveMode names what a "no" MEANS rather than printing a bare "no": a
// send-only domain sends from here while its inbound MX stays with another
// provider, and the platform relays mail for it there. A reader of `get` should
// not have to know that `no` is a mode and not a fault.
func receiveMode(d *coreapi.Domain) string {
	switch {
	case d.Receiving:
		return "yes"
	case !d.Enabled:
		return "no — domain disabled"
	}
	return "no — send-only (inbound MX elsewhere; mail for it is relayed there)"
}

// sendingMode is the same courtesy for the other direction, in the order a
// reader should hear them: the owner's off switch, then the operator's hold or
// stop, then the one state that is nobody's action — the send records were
// never proven, and the fix is DNS.
func sendingMode(d *coreapi.Domain) string {
	switch {
	case d.Sending:
		return "yes"
	case !d.Enabled:
		return "no — domain disabled"
	case hold(d.SendHold) == "disabled":
		return "no — DISABLED by operator (submissions refused permanently; queued mail bounced at the relay)"
	case hold(d.SendHold) == "paused":
		return "no — PAUSED by operator (submissions deferred; queued mail held, not bounced)"
	case d.ReceiveOnly:
		// The mirror of receiveMode's send-only line, and the reason the flag
		// exists: without it this domain reads "publish DNS" forever for records
		// it is never offered.
		return "no — receive-only (outbound goes through another provider; no send records are asked for)"
	case !d.SendVerified:
		return "no — send records not yet verified (publish the bounce + DKIM records, then re-run `domains create`)"
	}
	return "no"
}

// boolYNUnknown renders a tri-state liveness: nil is UNKNOWN (resolver
// unreachable), never conflated with a plain "no".
func boolYNUnknown(v *bool) string {
	if v == nil {
		return "?"
	}
	return boolYN(*v)
}
