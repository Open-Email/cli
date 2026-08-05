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
					// list is where an operator scans for the frozen tenant, and
					// a marker buried in a name column is one they miss.
					sending := "enabled"
					switch {
					case ac.SendDisabled:
						sending = "FROZEN"
					case ac.SendPaused:
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

func printAccount(w io.Writer, p *Printer, acc *coreapi.Account) {
	printTable(w, p, []string{"FIELD", "VALUE"}, [][]string{
		{"ID", acc.ID},
		{"Name", acc.Name},
		{"Sending", fmtAccountSendState(acc.SendDisabled, acc.SendPaused)},
		{"Messages/day", fmtSendCap(acc.SendMsgsPerDay)},
		{"Recipients/day", fmtSendCap(acc.SendRcptsPerDay)},
		{"Max mailboxes", int64Or(acc.MaxMailboxes, "unlimited")},
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
// operator most needs to know before choosing. FROZEN is checked first: core
// resolves a row carrying both toward the freeze, so reporting the hold would
// describe behavior the tenant is not getting.
func fmtAccountSendState(disabled, paused bool) string {
	switch {
	case disabled:
		return "FROZEN (every mailbox on every domain; queued mail bounced at the relay)"
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
		freeze, unfreeze    bool
		pause, resume       bool
	)
	cmd := &cobra.Command{
		Use:     "update <accountId>",
		Aliases: []string{"patch"},
		Short:   "Update an account — including the tenant-scale send freeze or hold (system callers only)",
		Long: "Update a tenant account. Two tenant-scale stop buttons, and the difference is\n" +
			"what happens to the mail:\n\n" +
			"  --freeze  ABUSE STOP. Submissions are refused permanently (403), so an SMTP\n" +
			"            client gets a 550 and gives up, and mail already in the relay queue\n" +
			"            is bounced. Use it when you want the sending to END.\n" +
			"  --pause   REVERSIBLE HOLD. Submissions are refused temporarily (429), so an\n" +
			"            SMTP client gets a 451 and ITS OWN queue holds the mail, and the\n" +
			"            relay backlog is deferred rather than bounced. Use it for\n" +
			"            non-payment, or while investigating something you expect to clear.\n\n" +
			"Pausing destroys no mail; freezing does. If you are not sure which you want,\n" +
			"pause — a hold can be upgraded to a freeze, but bounced mail cannot be recalled.\n" +
			"A hold outliving the relay's ~3.2-day retry window dead-letters that backlog\n" +
			"(preserved and redrivable, but no longer automatic).\n\n" +
			"Neither touches inbound: a stopped or held account still receives its mail, and\n" +
			"every mailbox stays readable. System callers only, in full — a tenant that could\n" +
			"lift its own freeze does not have one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag validation BEFORE authentication: contradictory flags are a
			// usage error whatever credentials the caller holds, and answering
			// "not authenticated" to `--freeze --unfreeze` sends someone to fix
			// the wrong thing.
			//
			// Two flags rather than one --send-disabled=true|false, matching
			// `mailboxes update`: this is the destructive direction of an abuse
			// control, and a mistyped value must not be able to mean its
			// opposite.
			if freeze && unfreeze {
				return usageError(errors.New("--freeze and --unfreeze are mutually exclusive"))
			}
			if pause && resume {
				return usageError(errors.New("--pause and --resume are mutually exclusive"))
			}
			// --freeze --pause together is NOT an error: core resolves it toward
			// the freeze (the permanent answer wins), and an operator escalating a
			// hold to a stop in one call is a reasonable thing to want. Saying so
			// here rather than refusing keeps the CLI from inventing a rule core
			// does not have.
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
			if freeze || unfreeze {
				patch["sendDisabled"] = freeze
			}
			if pause || resume {
				patch["sendPaused"] = pause
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
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass --name, --max-mailboxes, --send-*-per-day, --freeze/--unfreeze, or --pause/--resume"))
			}
			acc, err := client.UpdateAccount(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(acc, func(w io.Writer) {
				switch {
				case freeze:
					a.out.Successf("Froze sending for account %s — queued mail will bounce", acc.ID)
				case unfreeze:
					a.out.Successf("Released the send freeze on account %s", acc.ID)
				case pause:
					a.out.Successf("Paused sending for account %s — queued mail is held, not bounced", acc.ID)
				case resume:
					a.out.Successf("Resumed sending for account %s", acc.ID)
				default:
					a.out.Successf("Updated account %s", acc.ID)
				}
				printAccount(w, a.out, acc)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename the account")
	cmd.Flags().StringVar(&maxMailboxes, "max-mailboxes", "", "cap on mailboxes: a positive number, or 'unlimited' to clear it")
	cmd.Flags().BoolVar(&freeze, "freeze", false, "stop ALL outbound mail for this account")
	cmd.Flags().BoolVar(&unfreeze, "unfreeze", false, "release the account send freeze")
	cmd.Flags().BoolVar(&pause, "pause", false, "HOLD all outbound mail for this account — reversible, queued mail is deferred not bounced")
	cmd.Flags().BoolVar(&resume, "resume", false, "lift the account send hold")
	cmd.Flags().StringVar(&sendMsgs, "send-msgs-per-day", "", "distinct messages this ACCOUNT may send per rolling 24h, across every mailbox on every domain it owns: a number, 'unlimited', or 'default' to drop the override")
	cmd.Flags().StringVar(&sendRcpts, "send-rcpts-per-day", "", "envelope recipients per rolling 24h for the whole account: a number, 'unlimited', or 'default'")
	return cmd
}

func newAccountUsageCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "send-usage <accountId>",
		Aliases: []string{"usage"},
		Short:   "Show an account's send-allowance usage for the current window",
		Long: "What the account has SENT and how many mailboxes it has MINTED in the rolling\n" +
			"24h window, plus the caps in force and whether sending is frozen.\n\n" +
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
					{"Sending", fmtAccountSendState(u.Frozen, u.Paused)},
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
