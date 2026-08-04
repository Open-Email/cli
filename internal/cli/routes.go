package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newRoutesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "routes",
		Aliases: []string{"route"},
		Short:   "Manage address routes",
	}
	cmd.AddCommand(
		newRouteListCmd(a),
		newRouteCreateCmd(a),
		newRouteGetCmd(a),
		newRouteUpdateCmd(a),
		newRouteDeleteCmd(a),
		newRouteMembersCmd(a),
	)
	return cmd
}

func newRouteListCmd(a *app) *cobra.Command {
	var (
		domain, mailbox string
		all             bool
		limit           int
		cursor          string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List routes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Route
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Route], error) {
					return client.ListRoutes(ctx, domain, mailbox, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Route]
				page, err = client.ListRoutes(ctx, domain, mailbox, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"routes": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, r := range items {
					rows = append(rows, []string{r.Address, r.DestinationType, routeTarget(&r), boolYN(r.Paced)})
				}
				printTable(w, a.out, []string{"ADDRESS", "TYPE", "TARGET", "PACED"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain")
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "filter by mailbox id")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	return cmd
}

func newRouteCreateCmd(a *app) *cobra.Command {
	var (
		typ, mailbox, webhookURL, webhookSecret, remote string
		posting                                         string
		paced                                           bool
	)
	cmd := &cobra.Command{
		Use:   "create <address>",
		Short: "Create a route (address → mailbox | webhook | remote | group); idempotent for an identical binding, 409 only on a conflicting one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if typ == "" {
				return usageError(errors.New("--type is required (mailbox|webhook|remote|group)"))
			}
			if req := requiredDestFlag(typ); req != "" && !cmd.Flags().Changed(req) {
				return usageError(fmt.Errorf("--type %s requires --%s", typ, req))
			}
			if err := validPosting(posting); err != nil {
				return usageError(err)
			}
			in := coreapi.RouteCreateInput{
				Address:         args[0],
				DestinationType: typ,
				MailboxID:       mailbox,
				WebhookURL:      webhookURL,
				WebhookSecret:   webhookSecret,
				RemoteAddress:   remote,
				Paced:           paced,
				Posting:         posting,
			}
			r, err := client.CreateRoute(cmd.Context(), in)
			if err != nil {
				return err
			}
			a.out.Emit(r, func(w io.Writer) {
				a.out.Successf("Created route %s", r.Address)
				printRoute(w, a.out, r)
			})
			return nil
		},
	}
	routeDestFlags(cmd, &typ, &mailbox, &webhookURL, &webhookSecret, &remote)
	cmd.Flags().StringVar(&posting, "posting", "", "group posting policy: open|members (type=group)")
	cmd.Flags().BoolVar(&paced, "paced", false, "pace deliveries through the paced queue")
	return cmd
}

func newRouteGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <address>",
		Short: "Show a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			r, err := client.GetRoute(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(r, func(w io.Writer) { printRoute(w, a.out, r) })
			return nil
		},
	}
}

func newRouteUpdateCmd(a *app) *cobra.Command {
	var (
		typ, mailbox, webhookURL, webhookSecret, remote string
		posting                                         string
		paced                                           bool
		clearWebhookSecret                              bool
	)
	cmd := &cobra.Command{
		Use:   "update <address>",
		Short: "Update a route (--paced/--posting alone, or --type with its destination fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("webhook-secret") && clearWebhookSecret {
				return usageError(errors.New("--webhook-secret and --clear-webhook-secret are mutually exclusive"))
			}
			patch := map[string]any{}
			destChanged := cmd.Flags().Changed("mailbox") || cmd.Flags().Changed("webhook-url") ||
				cmd.Flags().Changed("webhook-secret") || cmd.Flags().Changed("remote") || clearWebhookSecret
			if cmd.Flags().Changed("type") {
				// A destination update replaces the destination; require the type's
				// mandatory field. The webhook secret is the exception — core
				// PRESERVES it when omitted (it's write-only), so omit to keep it,
				// pass --webhook-secret to change it, or --clear-webhook-secret to
				// remove it.
				if req := requiredDestFlag(typ); req != "" && !cmd.Flags().Changed(req) {
					return usageError(fmt.Errorf("--type %s requires --%s", typ, req))
				}
				patch["destinationType"] = typ
				if cmd.Flags().Changed("mailbox") {
					patch["mailboxId"] = mailbox
				}
				if cmd.Flags().Changed("webhook-url") {
					patch["webhookUrl"] = webhookURL
				}
				if cmd.Flags().Changed("webhook-secret") {
					patch["webhookSecret"] = webhookSecret
				} else if clearWebhookSecret {
					patch["webhookSecret"] = nil
				}
				if cmd.Flags().Changed("remote") {
					patch["remoteAddress"] = remote
				}
			} else if destChanged {
				return usageError(errors.New("destination fields require --type"))
			}
			if cmd.Flags().Changed("paced") {
				patch["paced"] = paced
			}
			// posting is patchable alone (like paced); core answers 400
			// posting_requires_group on non-group routes.
			if cmd.Flags().Changed("posting") {
				if err := validPosting(posting); err != nil {
					return usageError(err)
				}
				patch["posting"] = posting
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update — pass --paced, --posting, and/or --type with fields"))
			}
			r, err := client.UpdateRoute(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(r, func(w io.Writer) {
				a.out.Successf("Updated route %s", r.Address)
				printRoute(w, a.out, r)
			})
			return nil
		},
	}
	routeDestFlags(cmd, &typ, &mailbox, &webhookURL, &webhookSecret, &remote)
	cmd.Flags().BoolVar(&clearWebhookSecret, "clear-webhook-secret", false, "remove the webhook signing secret (type=webhook)")
	cmd.Flags().StringVar(&posting, "posting", "", "group posting policy: open|members (type=group)")
	cmd.Flags().BoolVar(&paced, "paced", false, "pace deliveries through the paced queue")
	return cmd
}

func newRouteDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <address>",
		Aliases: []string{"rm"},
		Short:   "Delete a route",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete route %s?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			if err := client.DeleteRoute(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "address": args[0]}, func(w io.Writer) {
				a.out.Successf("Deleted route %s", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// --- group members ---

func newRouteMembersCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "Manage a group route's members",
	}
	cmd.AddCommand(
		newMemberListCmd(a),
		newMemberReplaceCmd(a),
		newMemberAddCmd(a),
		newMemberRemoveCmd(a),
	)
	return cmd
}

func newMemberListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list <address>",
		Aliases: []string{"ls"},
		Short:   "List a group route's members",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.RouteMember
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.RouteMember], error) {
					return client.ListMembers(ctx, args[0], limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.RouteMember]
				page, err = client.ListMembers(ctx, args[0], limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"members": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, m := range items {
					rows = append(rows, []string{m.MemberAddress, fmtEpoch(m.CreatedAt)})
				}
				printTable(w, a.out, []string{"MEMBER", "ADDED"}, rows)
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

func newMemberReplaceCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "replace <address> [member...]",
		Short: "Replace a group's member list wholesale (no members = clear)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			members := args[1:]
			if len(members) == 0 && !yes && !confirm(fmt.Sprintf("Clear ALL members of %s?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			full, err := client.ReplaceMembers(cmd.Context(), args[0], members)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"members": full}, func(w io.Writer) {
				a.out.Successf("Replaced members of %s (%d now)", args[0], len(full))
				rows := make([][]string, 0, len(full))
				for _, m := range full {
					rows = append(rows, []string{m.MemberAddress})
				}
				printTable(w, a.out, []string{"MEMBER"}, rows)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt when clearing")
	return cmd
}

// memberBatchMax mirrors core's per-call cap on POST …/members/batch.
const memberBatchMax = 1000

func newMemberAddCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "add <address> [member...]",
		Short: "Add members to a group route (one = strict single add; several or --file = idempotent batch)",
		Long: "Add one or more members to a group route.\n\n" +
			"A single positional member uses the strict one-member call (409\n" +
			"member_exists if already present). Several members — or any --file\n" +
			"import — go through the batch endpoint instead: already-present members\n" +
			"count as duplicates, never errors, so re-running the same import\n" +
			"converges on the full list.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			address := args[0]
			members := append([]string{}, args[1:]...)
			if file != "" {
				data, rerr := os.ReadFile(file)
				if rerr != nil {
					return rerr
				}
				members = append(members, parseMemberList(string(data))...)
			}
			members = dedupeMembers(members)
			if len(members) == 0 {
				return usageError(errors.New("no members given — pass member addresses and/or --file"))
			}
			ctx := cmd.Context()
			// One positional member keeps the pre-batch behavior exactly.
			if len(members) == 1 && !cmd.Flags().Changed("file") {
				if err := client.AddMember(ctx, address, members[0]); err != nil {
					return err
				}
				a.out.Emit(map[string]any{"address": address, "memberAddress": members[0]}, func(w io.Writer) {
					a.out.Successf("Added %s to %s", members[0], address)
				})
				return nil
			}
			var added, duplicates int64
			for done := 0; done < len(members); {
				end := min(done+memberBatchMax, len(members))
				res, err := client.BatchAddMembers(ctx, address, members[done:end])
				if err != nil {
					if done > 0 {
						a.out.Warnf("stopped after %d of %d members (added %d, duplicates %d) — the import is idempotent, re-run the same command to converge on the rest",
							done, len(members), added, duplicates)
					}
					return err
				}
				added += res.Added
				duplicates += res.Duplicates
				done = end
			}
			a.out.Emit(map[string]any{"address": address, "added": added, "duplicates": duplicates}, func(w io.Writer) {
				a.out.Successf("added %d, duplicates %d", added, duplicates)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "newline-separated member addresses (blank lines and # comments skipped), merged with positional members")
	return cmd
}

// parseMemberList extracts addresses from newline-separated text: blank lines
// and #-comment lines are skipped, surrounding whitespace trimmed.
func parseMemberList(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// dedupeMembers drops repeats case-insensitively, keeping first-seen order
// (core lowercases addresses, so A@x and a@x land as one member anyway).
func dedupeMembers(members []string) []string {
	seen := make(map[string]struct{}, len(members))
	out := make([]string, 0, len(members))
	for _, m := range members {
		m = strings.TrimSpace(m)
		key := strings.ToLower(m)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out
}

func newMemberRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <address> <memberAddress>",
		Aliases: []string{"rm"},
		Short:   "Remove a member from a group route",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if err := client.RemoveMember(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true}, func(w io.Writer) {
				a.out.Successf("Removed %s from %s", args[1], args[0])
			})
			return nil
		},
	}
}

func routeDestFlags(cmd *cobra.Command, typ, mailbox, webhookURL, webhookSecret, remote *string) {
	cmd.Flags().StringVar(typ, "type", "", "destination type: mailbox|webhook|remote|group")
	cmd.Flags().StringVar(mailbox, "mailbox", "", "mailbox id (type=mailbox)")
	cmd.Flags().StringVar(webhookURL, "webhook-url", "", "https webhook URL (type=webhook)")
	cmd.Flags().StringVar(webhookSecret, "webhook-secret", "", "webhook signing secret (type=webhook)")
	cmd.Flags().StringVar(remote, "remote", "", "remote address (type=remote)")
}

func routeTarget(r *coreapi.Route) string {
	switch r.DestinationType {
	case "mailbox":
		return strOr(r.MailboxID, "—")
	case "webhook":
		return strOr(r.WebhookURL, "—")
	case "remote":
		return strOr(r.RemoteAddress, "—")
	case "group":
		return "(group)"
	}
	return "—"
}

func printRoute(w io.Writer, p *Printer, r *coreapi.Route) {
	rows := [][]string{
		{"Address", r.Address},
		{"Domain", r.Domain},
		{"Type", r.DestinationType},
		{"Target", routeTarget(r)},
	}
	// Group-only fields, carried by single-route answers only.
	if r.Posting != "" {
		rows = append(rows, []string{"Posting", r.Posting})
	}
	if r.MemberCount != nil {
		rows = append(rows, []string{"Members", fmt.Sprintf("%d", *r.MemberCount)})
	}
	rows = append(rows,
		[]string{"Paced", boolYN(r.Paced)},
		[]string{"Created", fmtEpoch(r.CreatedAt)},
	)
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)
}

// validPosting accepts an unset value (core's default) or the two policies.
func validPosting(s string) error {
	if s != "" && s != "open" && s != "members" {
		return fmt.Errorf("--posting must be open or members, got %q", s)
	}
	return nil
}
