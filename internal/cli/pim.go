package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newPimCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pim",
		Short: "Cross-collection calendar/contact surfaces: shares received, public directory, feeds",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newPimSharedCmd(a),
		newPimPublicCmd(a),
		newPimSubscriptionsCmd(a),
		newPimSubscribeCmd(a),
		newPimUnsubscribeCmd(a),
		newPimFeedCmd(a),
	)
	return cmd
}

// ownScope resolves the acting mailbox as its own store's scope (the pim
// group's endpoints are all owner-side).
func (a *app) ownScope(cmd *cobra.Command, client *coreapi.Client) (coreapi.PimScope, error) {
	mbx, err := a.resolveMailbox(cmd.Context(), client, "")
	if err != nil {
		return coreapi.PimScope{}, err
	}
	return coreapi.PimScope{MailboxID: mbx}, nil
}

func newPimSharedCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "shared",
		Short: "List collections other mailboxes shared with this one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := a.ownScope(cmd, client)
			if err != nil {
				return err
			}
			shared, err := client.ListPimSharedWithMe(cmd.Context(), s)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"shared": shared}, func(w io.Writer) {
				rows := make([][]string, 0, len(shared))
				for _, sh := range shared {
					// OWNER carries the address when core knows one. The
					// collection id stays a column of its own because it is what
					// the follow-up command takes; the owner column is for reading.
					rows = append(rows, []string{
						sh.Kind, sh.Name, strOr(sh.DisplayName, "—"), sh.CollectionID,
						granteeLabel(sh.OwnerAddress, sh.OwnerIdentityID), sh.Permission,
					})
				}
				printTable(w, a.out, []string{"KIND", "NAME", "DISPLAY", "COLLECTION", "OWNER", "PERMISSION"}, rows)
				if len(shared) > 0 {
					a.out.Msgf("read one with: openemail calendars objects list <COLLECTION> --owner <OWNER> (addressbooks likewise)")
				}
			})
			return nil
		},
	}
}

func newPimPublicCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "public",
		Short: "Browse the account's public collection directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := a.ownScope(cmd, client)
			if err != nil {
				return err
			}
			public, err := client.ListPimPublicDirectory(cmd.Context(), s)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"public": public}, func(w io.Writer) {
				printPimPublicRows(a, w, public)
				if len(public) > 0 {
					a.out.Msgf("subscribe with: openemail pim subscribe <ID>")
				}
			})
			return nil
		},
	}
}

func newPimSubscriptionsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "subscriptions",
		Aliases: []string{"subs"},
		Short:   "List this mailbox's public-directory subscriptions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := a.ownScope(cmd, client)
			if err != nil {
				return err
			}
			subs, err := client.ListPimSubscriptions(cmd.Context(), s)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"subscriptions": subs}, func(w io.Writer) {
				printPimPublicRows(a, w, subs)
			})
			return nil
		},
	}
}

func newPimSubscribeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "subscribe <publicId>",
		Short: "Subscribe to a public collection (grants this mailbox read access)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := a.ownScope(cmd, client)
			if err != nil {
				return err
			}
			pub, err := client.SubscribePimPublic(cmd.Context(), s, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(pub, func(w io.Writer) {
				a.out.Successf("Subscribed to %s (%s %s of %s)",
					derefOr(pub.DisplayName, pub.CollectionID), pub.Kind, pub.CollectionID, pub.OwnerIdentityID)
			})
			return nil
		},
	}
}

func newPimUnsubscribeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <publicId>",
		Short: "Remove a public-collection subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			s, err := a.ownScope(cmd, client)
			if err != nil {
				return err
			}
			if err := client.UnsubscribePimPublic(cmd.Context(), s, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "publicId": args[0]}, func(w io.Writer) {
				a.out.Successf("Unsubscribed from %s", args[0])
			})
			return nil
		},
	}
}

func newPimFeedCmd(a *app) *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "feed <token-or-url>",
		Short: "Fetch a published feed by its token (no login required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := pimFeedToken(args[0])
			if err != nil {
				return usageError(err)
			}
			// The token IS the credential — an unauthenticated client works.
			client, err := a.client()
			if err != nil {
				return err
			}
			body, _, err := client.GetPimFeed(cmd.Context(), token)
			if err != nil {
				return err
			}
			defer body.Close()
			return writeStream(a, body, outPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write to a file instead of stdout")
	return cmd
}

func printPimPublicRows(a *app, w io.Writer, entries []coreapi.PimPublicCollection) {
	rows := make([][]string, 0, len(entries))
	for _, p := range entries {
		rows = append(rows, []string{
			p.ID, p.Kind, strOr(p.DisplayName, "—"), strOr(p.Category, "—"),
			p.OwnerIdentityID, fmtEpoch(p.CreatedAt),
		})
	}
	printTable(w, a.out, []string{"ID", "KIND", "DISPLAY", "CATEGORY", "OWNER", "LISTED"}, rows)
}

// pimFeedToken extracts the token from a bare value or a full feed URL.
func pimFeedToken(arg string) (string, error) {
	if !strings.Contains(arg, "://") {
		return arg, nil
	}
	u, err := url.Parse(arg)
	if err != nil {
		return "", fmt.Errorf("unparseable feed URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	token := parts[len(parts)-1]
	if token == "" {
		return "", errors.New("the URL carries no token segment")
	}
	return token, nil
}
