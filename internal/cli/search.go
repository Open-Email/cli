package cli

import (
	"context"
	"errors"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newSearchCmd(a *app) *cobra.Command {
	var (
		label       string
		limit       int
		cursor      string
		groupThread bool
		all         bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search a mailbox's messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if groupThread && (cursor != "" || all) {
				return usageError(errors.New("--cursor/--all are not allowed with --group-thread (grouped search is single-page)"))
			}
			if all && cursor != "" {
				return usageError(errors.New("--all cannot be combined with --cursor"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}

			if all {
				results, derr := coreapi.Depaginate(cmd.Context(), func(ctx context.Context, cur string) (coreapi.Page[coreapi.MessageMeta], error) {
					r, e := client.Search(ctx, mbx, args[0], label, limit, cur, false)
					if e != nil {
						return coreapi.Page[coreapi.MessageMeta]{}, e
					}
					return coreapi.Page[coreapi.MessageMeta]{Items: r.Results, NextCursor: r.NextCursor}, nil
				})
				if derr != nil {
					return derr
				}
				a.out.Emit(map[string]any{"results": results, "nextCursor": ""}, func(w io.Writer) {
					printTable(w, a.out, messageListHeaders, messageListRows(results))
				})
				return nil
			}

			res, err := client.Search(cmd.Context(), mbx, args[0], label, limit, cursor, groupThread)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"results": res.Results, "nextCursor": res.NextCursor}, func(w io.Writer) {
				printTable(w, a.out, messageListHeaders, messageListRows(res.Results))
				a.moreHint(res.NextCursor)
			})
			return nil
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().StringVar(&label, "label", "", "restrict to one label")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results (1–100, default 25)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	cmd.Flags().BoolVar(&groupThread, "group-thread", false, "collapse matches to one result per thread (single page)")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page (drains the cursor; not allowed with --group-thread)")
	return cmd
}
