package cli

import (
	"errors"
	"io"

	"github.com/spf13/cobra"
)

func newSearchCmd(a *app) *cobra.Command {
	var (
		label       string
		limit       int
		cursor      string
		groupThread bool
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
			if groupThread && cursor != "" {
				return usageError(errors.New("--cursor is not allowed with --group-thread (grouped search is single-page)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
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
	return cmd
}
