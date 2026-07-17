package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/openemail/openemail-cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newPatternsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "patterns",
		Aliases: []string{"pattern"},
		Short:   "Manage pattern routes (local-part globs)",
	}
	cmd.AddCommand(
		newPatternListCmd(a),
		newPatternCreateCmd(a),
		newPatternUpdateCmd(a),
		newPatternDeleteCmd(a),
	)
	return cmd
}

func newPatternListCmd(a *app) *cobra.Command {
	var (
		domain string
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List pattern routes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var items []coreapi.Pattern
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.Pattern], error) {
					return client.ListPatterns(ctx, domain, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.Pattern]
				page, err = client.ListPatterns(ctx, domain, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"patterns": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, p := range items {
					rows = append(rows, []string{
						strconv.FormatInt(p.ID, 10), p.Domain, p.Pattern,
						fmt.Sprintf("%d", p.Priority), p.DestinationType, patternTarget(&p), boolYN(p.Paced),
					})
				}
				printTable(w, a.out, []string{"ID", "DOMAIN", "PATTERN", "PRIO", "TYPE", "TARGET", "PACED"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	return cmd
}

func newPatternCreateCmd(a *app) *cobra.Command {
	var (
		domain, pattern, typ               string
		mailbox, webhookURL, webhookSecret string
		remote, alias                      string
		priority                           int
		paced                              bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a pattern route (local-part glob → mailbox | webhook | remote | alias)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if domain == "" || pattern == "" || typ == "" {
				return usageError(errors.New("--domain, --pattern and --type are required"))
			}
			if req := requiredDestFlag(typ); req != "" && !cmd.Flags().Changed(req) {
				return usageError(fmt.Errorf("--type %s requires --%s", typ, req))
			}
			in := coreapi.PatternCreateInput{
				Domain:          domain,
				Pattern:         pattern,
				DestinationType: typ,
				MailboxID:       mailbox,
				WebhookURL:      webhookURL,
				WebhookSecret:   webhookSecret,
				RemoteAddress:   remote,
				AliasAddress:    alias,
				Paced:           paced,
			}
			if cmd.Flags().Changed("priority") {
				in.Priority = &priority
			}
			p, err := client.CreatePattern(cmd.Context(), in)
			if err != nil {
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Created pattern %d", p.ID)
				printPattern(w, a.out, p)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain the pattern applies to (required)")
	cmd.Flags().StringVar(&pattern, "pattern", "", "local-part glob, e.g. 'team-*' (required)")
	patternDestFlags(cmd, &typ, &mailbox, &webhookURL, &webhookSecret, &remote, &alias)
	cmd.Flags().IntVar(&priority, "priority", 0, "match priority (higher wins)")
	cmd.Flags().BoolVar(&paced, "paced", false, "pace deliveries through the paced queue")
	return cmd
}

func newPatternUpdateCmd(a *app) *cobra.Command {
	var (
		typ, mailbox, webhookURL, webhookSecret, remote, alias string
		priority                                               int
		paced                                                  bool
		clearWebhookSecret                                     bool
	)
	cmd := &cobra.Command{
		Use:   "update <patternId>",
		Short: "Update a pattern (--priority/--paced alone, or --type with fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, perr := strconv.ParseInt(args[0], 10, 64)
			if perr != nil {
				return usageError(fmt.Errorf("patternId must be an integer: %q", args[0]))
			}
			if cmd.Flags().Changed("webhook-secret") && clearWebhookSecret {
				return usageError(errors.New("--webhook-secret and --clear-webhook-secret are mutually exclusive"))
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("priority") {
				patch["priority"] = priority
			}
			if cmd.Flags().Changed("paced") {
				patch["paced"] = paced
			}
			destChanged := cmd.Flags().Changed("mailbox") || cmd.Flags().Changed("webhook-url") ||
				cmd.Flags().Changed("webhook-secret") || cmd.Flags().Changed("remote") ||
				cmd.Flags().Changed("alias") || clearWebhookSecret
			if cmd.Flags().Changed("type") {
				// The webhook secret is preserved when omitted (core keeps the
				// write-only key across a URL-only rotation); pass --webhook-secret
				// to change it or --clear-webhook-secret to remove it.
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
				if cmd.Flags().Changed("alias") {
					patch["aliasAddress"] = alias
				}
			} else if destChanged {
				return usageError(errors.New("destination fields require --type"))
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update"))
			}
			p, err := client.UpdatePattern(cmd.Context(), id, patch)
			if err != nil {
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Updated pattern %d", p.ID)
				printPattern(w, a.out, p)
			})
			return nil
		},
	}
	patternDestFlags(cmd, &typ, &mailbox, &webhookURL, &webhookSecret, &remote, &alias)
	cmd.Flags().IntVar(&priority, "priority", 0, "match priority")
	cmd.Flags().BoolVar(&paced, "paced", false, "pace deliveries through the paced queue")
	cmd.Flags().BoolVar(&clearWebhookSecret, "clear-webhook-secret", false, "remove the webhook signing secret (type=webhook)")
	return cmd
}

func newPatternDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <patternId>",
		Aliases: []string{"rm"},
		Short:   "Delete a pattern route",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, perr := strconv.ParseInt(args[0], 10, 64)
			if perr != nil {
				return usageError(fmt.Errorf("patternId must be an integer: %q", args[0]))
			}
			if !yes && !confirm(fmt.Sprintf("Delete pattern %d?", id)) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			if err := client.DeletePattern(cmd.Context(), id); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "id": id}, func(w io.Writer) {
				a.out.Successf("Deleted pattern %d", id)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func patternDestFlags(cmd *cobra.Command, typ, mailbox, webhookURL, webhookSecret, remote, alias *string) {
	cmd.Flags().StringVar(typ, "type", "", "destination type: mailbox|webhook|remote|alias")
	cmd.Flags().StringVar(mailbox, "mailbox", "", "mailbox id (type=mailbox)")
	cmd.Flags().StringVar(webhookURL, "webhook-url", "", "https webhook URL (type=webhook)")
	cmd.Flags().StringVar(webhookSecret, "webhook-secret", "", "webhook signing secret (type=webhook)")
	cmd.Flags().StringVar(remote, "remote", "", "remote address (type=remote)")
	cmd.Flags().StringVar(alias, "alias", "", "alias target address (type=alias)")
}

func patternTarget(p *coreapi.Pattern) string {
	switch p.DestinationType {
	case "mailbox":
		return strOr(p.MailboxID, "—")
	case "webhook":
		return strOr(p.WebhookURL, "—")
	case "remote":
		return strOr(p.RemoteAddress, "—")
	case "alias":
		return strOr(p.AliasAddress, "—")
	}
	return "—"
}

func printPattern(w io.Writer, pr *Printer, p *coreapi.Pattern) {
	printTable(w, pr, []string{"FIELD", "VALUE"}, [][]string{
		{"ID", strconv.FormatInt(p.ID, 10)},
		{"Domain", p.Domain},
		{"Pattern", p.Pattern},
		{"Priority", fmt.Sprintf("%d", p.Priority)},
		{"Type", p.DestinationType},
		{"Target", patternTarget(p)},
		{"Paced", boolYN(p.Paced)},
		{"Created", fmtEpoch(p.CreatedAt)},
	})
}
