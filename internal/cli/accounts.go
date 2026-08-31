package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newAccountsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "Manage tenant accounts (system callers only for create/list)",
	}
	cmd.AddCommand(
		newAccountCreateCmd(a),
		newAccountListCmd(a),
		newAccountGetCmd(a),
		newAccountUpdateCmd(a),
		newAccountTrafficCmd(a),
		newAccountUsageCmd(a),
		newAccountDeleteCmd(a),
		newAccountRestoreCmd(a),
	)
	return cmd
}

func newAccountCreateCmd(a *app) *cobra.Command {
	var maxMailboxes int
	var withKey bool
	var keyName string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant account (system callers only)",
		Long: "Create a tenant account. A fresh account has no credentials — pass --with-key\n" +
			"to also mint its first account API key (the token prints ONCE).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			var max *int64
			if cmd.Flags().Changed("max-mailboxes") {
				v := int64(maxMailboxes)
				max = &v
			}
			acc, err := client.CreateAccount(cmd.Context(), args[0], max)
			if err != nil {
				return err
			}
			if !withKey {
				a.out.Emit(acc, func(w io.Writer) {
					a.out.Successf("Created account %s", acc.ID)
					printAccount(w, a.out, acc)
				})
				return nil
			}
			key, kerr := client.CreateAPIKey(cmd.Context(), keyName, coreapi.PrincipalAccount, acc.ID)
			if kerr != nil {
				// The account exists; report the partial state loudly rather
				// than pretending the whole call failed.
				a.out.Emit(acc, func(w io.Writer) {
					a.out.Successf("Created account %s", acc.ID)
					printAccount(w, a.out, acc)
				})
				return fmt.Errorf("account created, but the key mint failed: %w — retry with `openemail keys create %s --account %s`", kerr, keyName, acc.ID)
			}
			out := struct {
				Account *coreapi.Account       `json:"account"`
				Key     *coreapi.CreatedAPIKey `json:"key"`
			}{acc, key}
			a.out.Emit(out, func(w io.Writer) {
				a.out.Successf("Created account %s", acc.ID)
				printAccount(w, a.out, acc)
				a.out.Successf("Created key %q (role %s)", key.Name, key.Role)
				a.out.Msgf("  token (shown once): %s", key.Token)
			})
			return nil
		},
	}
	cmd.Flags().IntVar(&maxMailboxes, "max-mailboxes", 0, "cap on mailboxes (omit = unlimited)")
	cmd.Flags().BoolVar(&withKey, "with-key", false, "also mint the account's first API key and print its token")
	cmd.Flags().StringVar(&keyName, "key-name", "bootstrap", "name for the minted key (with --with-key)")
	return cmd
}

func newAccountListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accounts (system callers only)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Account
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Account], error) {
					return client.ListAccounts(ctx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Account]
				page, err = client.ListAccounts(ctx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"accounts": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, ac := range items {
					// SENDING is its own column rather than a suffix on NAME: a
					// list is where an operator scans for the disabled tenant, and
					// a marker buried in a name column is one they miss.
					sending := "enabled"
					switch hold(ac.SendHold) {
					case "disabled":
						sending = "DISABLED"
					case "paused":
						sending = "PAUSED"
					}
					rows = append(rows, []string{ac.ID, ac.Name, sending, int64Or(ac.MaxMailboxes, "unlimited"), fmtEpoch(ac.CreatedAt)})
				}
				printTable(w, a.out, []string{"ID", "NAME", "SENDING", "MAX MAILBOXES", "CREATED"}, rows)
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

func newAccountGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <accountId>",
		Short: "Show an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			acc, err := client.GetAccount(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(acc, func(w io.Writer) { printAccount(w, a.out, acc) })
			return nil
		},
	}
}

func newAccountDeleteCmd(a *app) *cobra.Command {
	var purge bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <account-id>",
		Short: "Delete an account (soft; purged after the grace window)",
		Long: "Delete a tenant account. This DESTROYS NOTHING immediately: the account is\n" +
			"fenced (its keys stop working, its mail defers, its sending holds) and every\n" +
			"byte survives until the grace window elapses. `accounts restore` undoes it.\n\n" +
			"--purge waives the window and starts the teardown at once. That is\n" +
			"IRREVERSIBLE and system-only.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			// Confirmation is required for --purge and only for --purge. The
			// ordinary delete is reversible for a day and prints how to undo it;
			// making the safe verb noisy trains people to type -y reflexively,
			// which is exactly what must not happen for the unsafe one.
			if purge && !yes {
				return fmt.Errorf(
					"refusing to purge %s without --yes: this is irreversible and takes effect immediately",
					args[0])
			}
			res, err := client.DeleteAccount(cmd.Context(), args[0], purge)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				if !res.Restorable {
					a.out.Successf("Purging account %s now — this cannot be undone", res.ID)
					return
				}
				a.out.Successf("Account %s scheduled for deletion", res.ID)
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Deleted", fmtEpoch(res.DeletedAt)},
					{"Purges", fmtEpoch(res.PurgeAt)},
				})
				a.out.Msgf("  undo: openemail accounts restore %s", res.ID)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "waive the grace window and purge now (system only, irreversible)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm --purge")
	return cmd
}

func newAccountRestoreCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <account-id>",
		Short: "Cancel a pending account deletion",
		Long: "Undo a soft delete, while there is something to undo. Refused once the purge\n" +
			"has claimed the account (409 purge_in_progress) or finished (410).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			acc, err := client.RestoreAccount(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(acc, func(w io.Writer) {
				a.out.Successf("Restored account %s", acc.ID)
				printAccount(w, a.out, acc)
			})
			return nil
		},
	}
}

// fmtAccountLifecycle states the deletion state as something an operator can
// act on. "Live" rather than a blank, because an empty cell in a status table
// reads as "unknown" — and the difference between a live account and one that
// vanishes tomorrow is the single most important row here.
func fmtAccountLifecycle(acc *coreapi.Account) string {
	if acc.PurgedAt != nil {
		return fmt.Sprintf("PURGED %s (this row is a tombstone)", fmtEpoch(*acc.PurgedAt))
	}
	if acc.DeletedAt != nil {
		if acc.PurgeAt != nil {
			return fmt.Sprintf("DELETING — purges %s (restore to cancel)", fmtEpoch(*acc.PurgeAt))
		}
		return "DELETING (restore to cancel)"
	}
	return "live"
}

func printAccount(w io.Writer, p *Printer, acc *coreapi.Account) {
	printTable(w, p, []string{"FIELD", "VALUE"}, [][]string{
		{"ID", acc.ID},
		{"Name", acc.Name},
		{"State", fmtAccountLifecycle(acc)},
		{"Sending", fmtAccountSendState(acc.SendHold)},
		{"Messages/day", fmtSendCap(acc.SendMsgsPerDay)},
		{"Recipients/day", fmtSendCap(acc.SendRcptsPerDay)},
		{"Max mailboxes", int64Or(acc.MaxMailboxes, "unlimited")},
		{"Storage pool", fmtStoragePool(acc.StorageLimitBytes)},
		{"Vanity hostnames", boolYN(acc.VanityHosts)},
		{"Created", fmtEpoch(acc.CreatedAt)},
	})
}

// fmtAccountSendState mirrors fmtSendState's reasoning — a freeze is something
// an operator DID, not a capability the account never had — and states the
// SCOPE, because the whole reason to reach for this rather than the per-mailbox
// freeze is that it covers mailboxes the tenant has not created yet.
//
// The two modes are spelled out rather than collapsed into "not sending",
// because what happens to the QUEUED mail differs and that is the thing an
// operator most needs to know before choosing. DISABLED (core's word for the
// freeze) is checked first: core resolves a row carrying both toward the
// freeze, so reporting the hold would describe behavior the tenant is not
// getting.
// fmtStoragePool is fmtSendCap for the pool: the same three states, but the
// configured one is bytes and an operator should not have to read 53687091200.
func fmtStoragePool(p *int64) string {
	if p == nil {
		return "platform default"
	}
	if *p == 0 {
		return "unlimited (metered)"
	}
	return fmtBytes(*p)
}

func fmtAccountSendState(sendHold *string) string {
	disabled, paused := hold(sendHold) == "disabled", hold(sendHold) == "paused"
	switch {
	case disabled:
		return "DISABLED (every mailbox on every domain; queued mail bounced at the relay)"
	case paused:
		return "PAUSED (every mailbox on every domain; queued mail held, not bounced)"
	}
	return "enabled"
}

func newAccountUpdateCmd(a *app) *cobra.Command {
	var (
		name                string
		maxMailboxes        string
		sendMsgs, sendRcpts string
		vanityHosts         string
		storageLimit        string
	)
	cmd := &cobra.Command{
		Use:     "update <accountId>",
		Aliases: []string{"patch"},
		Short:   "Update an account's name, caps, storage pool or vanity-hostname gate (system callers only)",
		Long: "Update a tenant account. Holding or stopping the tenant's outbound mail is an operator\n" +
			"action of its own: openemail admin hold account <id> --pause|--stop (and admin release).\n" +
			"Both cover every mailbox on every domain the account owns, queued relay backlog included,\n" +
			"and neither touches inbound.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			if cmd.Flags().Changed("max-mailboxes") {
				v, perr := parseMaxMailboxesFlag(maxMailboxes)
				if perr != nil {
					return usageError(perr)
				}
				patch["maxMailboxes"] = v // nil marshals as JSON null = unlimited
			}
			// The account-tier VOLUME caps, which reuse the send-cap parser
			// because they share its three states exactly: "default" inherits the
			// platform number, "unlimited" is 0, anything else is the number.
			for _, f := range []struct {
				flag  string
				field string
				val   *string
			}{
				{"send-msgs-per-day", "sendMsgsPerDay", &sendMsgs},
				{"send-rcpts-per-day", "sendRcptsPerDay", &sendRcpts},
			} {
				if !cmd.Flags().Changed(f.flag) {
					continue
				}
				v, perr := parseSendCapFlag(*f.val)
				if perr != nil {
					return usageError(perr)
				}
				patch[f.field] = v // nil marshals as JSON null = drop the override
			}
			// The account storage POOL. Same three states as the send caps, but a
			// separate parser because it takes a human size — nobody should have
			// to type 53687091200 to set 50 GiB.
			if cmd.Flags().Changed("storage-limit") {
				v, perr := parseStorageLimitFlag(storageLimit)
				if perr != nil {
					return usageError(perr)
				}
				patch["storageLimitBytes"] = v
			}
			// The vanity-hostname gate. A plain on/off rather than the two-flag
			// treatment the freeze gets: turning it OFF destroys nothing (it stops
			// new claims and leaves hostnames already serving a customer's clients
			// exactly where they are), so a mistyped value cannot cost anything.
			if cmd.Flags().Changed("vanity-hosts") {
				v, perr := parseBoolFlag("--vanity-hosts", vanityHosts)
				if perr != nil {
					return usageError(perr)
				}
				patch["vanityHosts"] = v
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass --name, --max-mailboxes, --send-*-per-day, --storage-limit or --vanity-hosts (to hold or stop sending: openemail admin hold account)"))
			}
			acc, err := client.UpdateAccount(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(acc, func(w io.Writer) {
				a.out.Successf("Updated account %s", acc.ID)
				printAccount(w, a.out, acc)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename the account")
	cmd.Flags().StringVar(&maxMailboxes, "max-mailboxes", "", "cap on mailboxes: a positive number, or 'unlimited' to clear it")
	cmd.Flags().StringVar(&sendMsgs, "send-msgs-per-day", "", "distinct messages this ACCOUNT may send per rolling 24h, across every mailbox on every domain it owns: a number, 'unlimited', or 'default' to drop the override")
	cmd.Flags().StringVar(&sendRcpts, "send-rcpts-per-day", "", "envelope recipients per rolling 24h for the whole account: a number, 'unlimited', or 'default'")
	cmd.Flags().StringVar(&storageLimit, "storage-limit", "", "account-wide storage POOL across every mailbox it owns: a size like 50G, 'unlimited' (metered — overage billed, never refused), or 'default' for the platform pool")
	cmd.Flags().StringVar(&vanityHosts, "vanity-hosts", "", "may this account claim VANITY HOSTNAMES (its own mail./smtp./webmail./dav. names): true|false. Gates claiming only — turning it off never revokes hostnames already serving clients")
	return cmd
}

// parseBoolFlag reads an explicit true/false string flag. Explicit rather than a
// bare bool because these are tri-state on the wire (absent = leave alone), and
// a bare `--flag` could only ever mean "true".
// parseStorageLimitFlag reads the pool's three states, accepting a human size
// for the configured one. It mirrors parseSendCapFlag's vocabulary exactly —
// 'default' drops the override, 'unlimited' is an explicit 0 — because an
// operator setting a plan tier reads these flags as one family and a divergence
// in what "default" means is the kind of thing found later, on a live account.
func parseStorageLimitFlag(s string) (*int64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "default":
		return nil, nil
	case "unlimited", "none":
		var zero int64
		return &zero, nil
	}
	n, err := parseByteSize(s)
	if err != nil {
		return nil, fmt.Errorf("invalid --storage-limit %q: %w", s, err)
	}
	return &n, nil
}

func parseBoolFlag(flag, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false, got %q", flag, raw)
	}
}

func newAccountUsageCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "send-usage <accountId>",
		Aliases: []string{"usage"},
		Short:   "Show an account's send-allowance usage for the current window",
		Long: "What the account has SENT and how many mailboxes it has MINTED in the rolling\n" +
			"24h window, plus the caps in force and whether sending is disabled.\n\n" +
			"`accounts traffic` shows what went out; this shows what is LEFT. When a tenant\n" +
			"reports an unexplained 429, this is usually where the reason is — the\n" +
			"per-mailbox reading looks healthy while the account total is exhausted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			u, err := client.GetAccountSendUsage(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(u, func(w io.Writer) {
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Sending", fmtAccountSendState(u.SendHold)},
					{"Window", fmt.Sprintf("rolling %dh", u.WindowSeconds/3600)},
					{"Messages", fmt.Sprintf("%d of %s", u.Send.Messages, fmtSendLimit(u.Send.MsgsPerDay))},
					{"Recipients", fmt.Sprintf("%d of %s", u.Send.Recipients, fmtSendLimit(u.Send.RcptsPerDay))},
					{"Mailboxes created", fmt.Sprintf("%d of %s", u.Creates.Mailboxes, fmtSendLimit(u.Creates.PerDay))},
				})
				// Same surprise as the per-mailbox reading, one scope up — and
				// here it matters more, because this is the number someone
				// compares against a plan tier.
				a.out.Msgf("messages count distinct content — the same message sent to N people is 1 message, N recipients")
			})
			return nil
		},
	}
}

// parseMaxMailboxesFlag parses --max-mailboxes into the two states core
// distinguishes: a POSITIVE integer, or null for unlimited.
//
// Deliberately NOT parseSendCapFlag, whose null means something else entirely.
// On a send cap, null is "inherit the platform number" and 0 spells unlimited;
// here null IS unlimited and 0 is refused by core (the column is
// `.positive()`), so reusing that parser would turn `--max-mailboxes none` into
// a 400 while looking like it worked in every other command.
func parseMaxMailboxesFlag(s string) (*int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "", "unlimited", "none":
		return nil, nil
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("invalid mailbox cap %q: expected a positive number, or 'unlimited'", s)
	}
	return &n, nil
}

func newAccountTrafficCmd(a *app) *cobra.Command {
	var rng string
	cmd := &cobra.Command{
		Use:   "traffic <accountId>",
		Short: "Traffic aggregates across every domain this account owns (sampled estimates)",
		Long: "The cross-mailbox view. Per-domain and per-mailbox surfaces cannot see a tenant\n" +
			"spreading volume: fifty mailboxes each under their cap look healthy fifty times\n" +
			"over while the account total is fifty times the intended one. This is the one\n" +
			"place that is a single number.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			tr, err := client.GetAccountTraffic(cmd.Context(), args[0], rng)
			if err != nil {
				return err
			}
			a.out.Emit(tr, func(w io.Writer) {
				a.out.Msgf("%s — %s (estimated, ~%dd retention)", a.out.Bold(tr.AccountID), tr.Range, tr.RetentionDays)
				a.out.Msgf("  total: %d events, %s across %d domain(s)", tr.Totals.Events, fmtBytes(tr.Totals.Bytes), len(tr.Domains))
				if tr.DomainsTruncated {
					// Never let a partial total read as a complete one — this is
					// the number someone decides to freeze an account on.
					a.out.Warnf("  this account holds more domains than one query covers; the totals above are PARTIAL")
				}
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
