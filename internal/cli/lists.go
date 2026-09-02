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

// newListsCmd is the ADDRESS LIST surface (core migration 0040) — the tenant's
// own allow/block policy in both directions, and the construct behind
// `openemail do-not-send` next door.
//
// One shape answers four wishes: "never mail X" (outbound block), "never
// accept mail from Y" (inbound block), "always accept mail from Z, even if the
// spam filter disagrees" (inbound allow), and "let this one recipient through a
// broader block" (outbound allow). A list carries a SCOPE (account, one domain,
// one mailbox), a DIRECTION and a VERDICT, and holds address patterns.
//
// Names carry no ORDER: precedence is scope specificity (mailbox beats domain
// beats account) and then allow-beats-block within one scope, so lists at one
// scope are a union and adding another can never change what the existing ones
// decide.
func newListsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "lists",
		Aliases: []string{"list-policy", "address-lists"},
		Short:   "Allow/block lists for senders and recipients",
		Long: "Named lists of address patterns, each binding one scope (this account, one\n" +
			"of its domains, or one mailbox) in one direction:\n\n" +
			"  inbound  block   refuse mail from these senders (a 550 in session — never\n" +
			"                   a silent drop, so the sender learns)\n" +
			"  inbound  allow   accept them past a broader block AND past the spam\n" +
			"                   filter (the score is still recorded)\n" +
			"  outbound block   never mail these recipients\n" +
			"  outbound allow   exempt one recipient from a broader block — an allow\n" +
			"                   NEVER restricts who you may mail\n\n" +
			"Precedence is by scope, not by list: mailbox beats domain beats account,\n" +
			"and within one scope an allow beats a block. Lists at the same scope are a\n" +
			"union, so a new list can never reorder the ones you already have.\n\n" +
			"Patterns: `jo@acme.example`, `@acme.example` (any local part — stored\n" +
			"`*@acme.example`), `sales-*@acme.example`, and `@.acme.example` for the\n" +
			"domain AND its subdomains.\n\n" +
			"`openemail do-not-send` is a shortcut for the account-wide outbound block\n" +
			"list; use it unless you need the other three combinations.",
	}
	cmd.AddCommand(
		newListsListCmd(a),
		newListsShowCmd(a),
		newListsCreateCmd(a),
		newListsRenameCmd(a),
		newListsDeleteCmd(a),
		newListsAddCmd(a),
		newListsRemoveCmd(a),
		newListsImportCmd(a),
		newListsCheckCmd(a),
	)
	return cmd
}

// listFamily resolves which path family to act on — an account's lists, or one
// mailbox's own. `--mailbox` picks the second, which is also the only family a
// mailbox credential can reach.
func listFamily(ctx context.Context, a *app, client *coreapi.Client, flagAccount, flagMailbox string) (coreapi.AddressLists, error) {
	if flagMailbox != "" {
		return coreapi.MailboxLists(flagMailbox), nil
	}
	acct, _, err := suppressionScope(ctx, a, client, flagAccount)
	if err != nil {
		return coreapi.AddressLists{}, err
	}
	return coreapi.AccountLists(acct), nil
}

func listScopeFlags(cmd *cobra.Command, account, mailbox *string) {
	accountFlag(cmd, account)
	cmd.Flags().StringVarP(mailbox, "mailbox", "m", "", "act on ONE mailbox's own lists instead of the account's (the only family a mailbox credential can use)")
}

func listRows(items []coreapi.AddressList) [][]string {
	rows := make([][]string, 0, len(items))
	for _, l := range items {
		rows = append(rows, []string{
			l.ID, l.Name, l.Direction, l.Verdict, l.ScopeKind + ":" + l.ScopeID, entryCountCell(l),
		})
	}
	return rows
}

// entryCountCell keeps "not counted" and "counted, empty" distinguishable — a
// listing does not count per row, and printing 0 for both would be a lie in one
// of the two cases.
func entryCountCell(l coreapi.AddressList) string {
	if l.EntryCount == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *l.EntryCount)
}

func newListsListCmd(a *app) *cobra.Command {
	var (
		account, mailbox          string
		direction, verdict, scope string
		all                       bool
		limit                     int
		cursor                    string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show your address lists",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			f := coreapi.ListAddressListsFilter{Direction: direction, Verdict: verdict}
			if scope != "" {
				kind, id, ok := strings.Cut(scope, ":")
				if !ok {
					return usageError(errors.New("--scope takes kind:id, e.g. domain:acme.example or mailbox:01ABC…"))
				}
				f.ScopeKind, f.ScopeID = kind, id
			}
			var items []coreapi.AddressList
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressList], error) {
					return client.ListAddressLists(ctx, fam, f, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.AddressList]
				page, err = client.ListAddressLists(ctx, fam, f, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"lists": items, "nextCursor": next}, func(w io.Writer) {
				printTable(w, a.out, []string{"ID", "NAME", "DIRECTION", "VERDICT", "SCOPE", "ENTRIES"}, listRows(items))
				a.moreHint(next)
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().StringVar(&direction, "direction", "", "inbound or outbound")
	cmd.Flags().StringVar(&verdict, "verdict", "", "allow or block")
	cmd.Flags().StringVar(&scope, "scope", "", "narrow to one scope, as kind:id (account:<id>, domain:<name>, mailbox:<id>)")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newListsShowCmd(a *app) *cobra.Command {
	var (
		account, mailbox string
		all              bool
		limit            int
		cursor           string
	)
	cmd := &cobra.Command{
		Use:     "show <list-id>",
		Aliases: []string{"get", "entries"},
		Short:   "Show one list and its patterns",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			l, err := client.GetAddressList(ctx, fam, args[0])
			if err != nil {
				return err
			}
			var items []coreapi.AddressListEntry
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.AddressListEntry], error) {
					return client.ListAddressListEntries(ctx, fam, args[0], limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.AddressListEntry]
				page, err = client.ListAddressListEntries(ctx, fam, args[0], limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"list": l, "entries": items, "nextCursor": next}, func(w io.Writer) {
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Name", l.Name},
					{"Scope", l.ScopeKind + ":" + l.ScopeID},
					{"Direction", l.Direction},
					{"Verdict", l.Verdict},
					{"Entries", entryCountCell(*l)},
					{"Created", fmtEpoch(l.CreatedAt)},
				})
				rows := make([][]string, 0, len(items))
				for _, e := range items {
					rows = append(rows, []string{e.Pattern, fmtEpoch(e.CreatedAt), truncate(strOr(e.Note, "—"), 40)})
				}
				printTable(w, a.out, []string{"PATTERN", "ADDED", "NOTE"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page of entries")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newListsCreateCmd(a *app) *cobra.Command {
	var (
		account, mailbox          string
		direction, verdict, scope string
	)
	cmd := &cobra.Command{
		Use:     "create <name>",
		Aliases: []string{"new"},
		Short:   "Create an address list",
		Long: "Direction, verdict and scope are fixed at creation: they decide what every\n" +
			"pattern on the list MEANS, so changing them later would silently rewrite\n" +
			"entries you already added. Create another list instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if direction != "inbound" && direction != "outbound" {
				return usageError(fmt.Errorf("--direction must be inbound or outbound, got %q", direction))
			}
			if verdict != "allow" && verdict != "block" {
				return usageError(fmt.Errorf("--verdict must be allow or block, got %q", verdict))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			in := coreapi.AddressListCreate{Name: args[0], Direction: direction, Verdict: verdict}
			if mailbox == "" {
				// The account family needs an explicit scope; default to the whole
				// account, which is what "my company blocks them" means.
				kind, id := "account", ""
				if scope != "" {
					var ok bool
					kind, id, ok = strings.Cut(scope, ":")
					if !ok {
						return usageError(errors.New("--scope takes kind:id, e.g. domain:acme.example or mailbox:01ABC…"))
					}
				} else {
					acct, _, serr := suppressionScope(ctx, a, client, account)
					if serr != nil {
						return serr
					}
					id = acct
				}
				in.ScopeKind, in.ScopeID = kind, id
			}
			l, err := client.CreateAddressList(ctx, fam, in)
			if err != nil {
				return err
			}
			a.out.Emit(l, func(w io.Writer) {
				a.out.Successf("Created %s (%s %s at %s:%s) — id %s", l.Name, l.Direction, l.Verdict, l.ScopeKind, l.ScopeID, l.ID)
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().StringVar(&direction, "direction", "", "inbound (judges senders) or outbound (judges recipients)")
	cmd.Flags().StringVar(&verdict, "verdict", "", "block or allow")
	cmd.Flags().StringVar(&scope, "scope", "", "what it binds, as kind:id — defaults to the whole account")
	_ = cmd.MarkFlagRequired("direction")
	_ = cmd.MarkFlagRequired("verdict")
	return cmd
}

func newListsRenameCmd(a *app) *cobra.Command {
	var account, mailbox string
	cmd := &cobra.Command{
		Use:   "rename <list-id> <new-name>",
		Short: "Rename a list",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			l, err := client.RenameAddressList(ctx, fam, args[0], args[1])
			if err != nil {
				return err
			}
			a.out.Emit(l, func(w io.Writer) { a.out.Successf("Renamed to %s", l.Name) })
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	return cmd
}

func newListsDeleteCmd(a *app) *cobra.Command {
	var (
		account, mailbox string
		yes              bool
	)
	cmd := &cobra.Command{
		Use:     "delete <list-id>",
		Aliases: []string{"rm-list"},
		Short:   "Delete a list and every pattern on it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete list %s and all of its patterns?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			if _, err := client.DeleteAddressList(ctx, fam, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true}, func(w io.Writer) {
				a.out.Successf("List %s deleted", args[0])
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newListsAddCmd(a *app) *cobra.Command {
	var (
		account, mailbox string
		note             string
	)
	cmd := &cobra.Command{
		Use:   "add <list-id> <pattern>",
		Short: "Add one pattern to a list",
		Long: "Patterns: an address (`jo@acme.example`), a whole domain\n" +
			"(`@acme.example`), a domain and its subdomains (`@.acme.example`), or a\n" +
			"local-part glob (`sales-*@acme.example`). Repeat adds converge on one\n" +
			"entry — the note refreshes, the original added-at stays.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			e, err := client.AddAddressListEntry(ctx, fam, args[0], coreapi.AddressListEntryInput{
				Pattern: args[1], Note: note,
			})
			if err != nil {
				return err
			}
			a.out.Emit(e, func(w io.Writer) {
				// The stored spelling, not the typed one: core normalizes, and a
				// user who typed `@acme.example` needs to see `*@acme.example` to
				// recognize it in a listing.
				a.out.Successf("Added %s", e.Pattern)
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().StringVar(&note, "note", "", "why — stored on the entry, display only")
	return cmd
}

func newListsRemoveCmd(a *app) *cobra.Command {
	var account, mailbox string
	cmd := &cobra.Command{
		Use:     "remove <list-id> <pattern>",
		Aliases: []string{"rm"},
		Short:   "Remove one pattern from a list",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			if _, err := client.RemoveAddressListEntry(ctx, fam, args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true}, func(w io.Writer) {
				a.out.Successf("Removed %s", args[1])
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	return cmd
}

func newListsImportCmd(a *app) *cobra.Command {
	var account, mailbox string
	cmd := &cobra.Command{
		Use:   "import <list-id> <pattern>...",
		Short: "Add many patterns at once",
		Long: "Up to 500 per call. Anything that is not a pattern is REPORTED rather than\n" +
			"failing the import — a suppression export from another provider routinely\n" +
			"carries a few rows that are not addresses.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			entries := make([]coreapi.AddressListEntryInput, 0, len(args)-1)
			for _, p := range args[1:] {
				entries = append(entries, coreapi.AddressListEntryInput{Pattern: p})
			}
			res, err := client.AddAddressListEntries(ctx, fam, args[0], entries)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(w io.Writer) {
				a.out.Successf("Added %d pattern(s)", res.Added)
				if len(res.Invalid) > 0 {
					a.out.Warnf("Not patterns, skipped: %s", strings.Join(res.Invalid, ", "))
				}
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	return cmd
}

func newListsCheckCmd(a *app) *cobra.Command {
	var (
		account, mailbox         string
		direction, domain, scope string
	)
	cmd := &cobra.Command{
		Use:     "check <address>",
		Aliases: []string{"evaluate", "explain"},
		Short:   "Ask what your lists decide about an address",
		Long: "Runs the SAME evaluator the delivery path and the relay run, and names the\n" +
			"list and pattern that decided — the answer to \"why was this refused?\".\n\n" +
			"Narrow the scope chain with --domain and --scope-mailbox to see what one\n" +
			"domain or one mailbox gets, which is where an allow at a narrower scope\n" +
			"lifts a wider block.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if direction != "inbound" && direction != "outbound" {
				return usageError(fmt.Errorf("--direction must be inbound or outbound, got %q", direction))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fam, err := listFamily(ctx, a, client, account, mailbox)
			if err != nil {
				return err
			}
			targetMbx := scope
			if targetMbx == "" && mailbox != "" {
				targetMbx = mailbox
			}
			v, err := client.EvaluateAddressLists(ctx, fam, coreapi.AddressListEvaluateInput{
				Direction: direction, Address: args[0], Domain: domain, MailboxID: targetMbx,
			})
			if err != nil {
				return err
			}
			a.out.Emit(v, func(w io.Writer) {
				switch v.Verdict {
				case "block":
					a.out.Warnf("%s is BLOCKED (%s scope, pattern %s, list %s)", args[0], v.ScopeKind, v.Pattern, v.ListID)
				case "allow":
					a.out.Successf("%s is ALLOWED past broader blocks (%s scope, pattern %s, list %s)", args[0], v.ScopeKind, v.Pattern, v.ListID)
				default:
					a.out.Successf("No list has an opinion about %s — it is handled normally", args[0])
				}
			})
			return nil
		},
	}
	listScopeFlags(cmd, &account, &mailbox)
	cmd.Flags().StringVar(&direction, "direction", "", "inbound (they write to you) or outbound (you write to them)")
	cmd.Flags().StringVar(&domain, "domain", "", "include this domain's scope in the chain")
	cmd.Flags().StringVar(&scope, "scope-mailbox", "", "include this mailbox's scope in the chain")
	_ = cmd.MarkFlagRequired("direction")
	return cmd
}
