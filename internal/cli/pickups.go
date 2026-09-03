package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newPickupsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pickups",
		Aliases: []string{"pickup"},
		Short:   "Manage a mailbox's POP3 pickup sources",
	}
	addMailboxFlag(cmd, a)
	cmd.AddCommand(
		newPickupListCmd(a),
		newPickupGetCmd(a),
		newPickupCreateCmd(a),
		newPickupUpdateCmd(a),
		newPickupDeleteCmd(a),
		newPickupRunCmd(a),
	)
	return cmd
}

func newPickupListCmd(a *app) *cobra.Command {
	var (
		all    bool
		limit  int
		cursor string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List pickup sources",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			mbx, err := a.resolveMailbox(ctx, client, "")
			if err != nil {
				return err
			}
			var items []coreapi.PickupSource
			next := ""
			if all {
				items, err = coreapi.Depaginate(ctx, func(ctx context.Context, cur string) (coreapi.Page[coreapi.PickupSource], error) {
					return client.ListPickups(ctx, mbx, limit, cur)
				})
			} else {
				var page coreapi.Page[coreapi.PickupSource]
				page, err = client.ListPickups(ctx, mbx, limit, cursor)
				items, next = page.Items, page.NextCursor
			}
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"pickups": items, "nextCursor": next}, func(w io.Writer) {
				rows := make([][]string, 0, len(items))
				for _, p := range items {
					rows = append(rows, []string{
						p.ID, strOr(p.Name, "—"),
						fmt.Sprintf("%s:%d", p.Host, p.Port), p.Username, p.TLS,
						fmt.Sprintf("%dm", p.IntervalMinutes), boolYN(p.Enabled),
						pickupStatus(p),
					})
				}
				printTable(w, a.out, []string{"ID", "NAME", "HOST", "USERNAME", "TLS", "EVERY", "ENABLED", "LAST"}, rows)
				a.moreHint(next)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page")
	return cmd
}

func newPickupGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <pickupId>",
		Short: "Show one pickup source (never shows the password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			p, err := client.GetPickup(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(p, func(w io.Writer) { printPickup(w, a.out, p) })
			return nil
		},
	}
}

func newPickupCreateCmd(a *app) *cobra.Command {
	var (
		host, username, password, name, tls, label string
		markRead                                   bool
		port, interval                             int64
		deleteAfterFetch                           bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a POP3 pickup source (password prompted if omitted on a TTY)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if host == "" || username == "" || port == 0 {
				return usageError(errors.New("--host, --port and --username are required"))
			}
			if tls != "" && tls != "tls" && tls != "starttls" {
				return usageError(errors.New("--tls must be 'tls' or 'starttls'"))
			}
			if !cmd.Flags().Changed("password") {
				if !promptTTY() {
					return usageError(errors.New("--password is required (no TTY to prompt)"))
				}
				pw, rerr := readSecretRaw("Pickup password: ")
				if rerr != nil {
					return rerr
				}
				password = pw
			}
			if password == "" {
				return usageError(errors.New("password must not be empty"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			in := coreapi.PickupCreateInput{
				Host: host, Port: port, Username: username, Password: password, TLS: tls,
			}
			if cmd.Flags().Changed("name") {
				in.Name = &name
			}
			if cmd.Flags().Changed("interval") {
				in.IntervalMinutes = &interval
			}
			if cmd.Flags().Changed("delete-after-fetch") {
				in.DeleteAfterFetch = &deleteAfterFetch
			}
			if cmd.Flags().Changed("label") {
				in.LabelName = &label
			}
			if cmd.Flags().Changed("mark-read") {
				in.MarkRead = &markRead
			}
			p, err := client.CreatePickup(cmd.Context(), mbx, in)
			if err != nil {
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Created pickup source %s", p.ID)
				printPickup(w, a.out, p)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "POP3 server hostname (required)")
	cmd.Flags().Int64Var(&port, "port", 0, "POP3 server port (required)")
	cmd.Flags().StringVar(&username, "username", "", "POP3 username (required)")
	cmd.Flags().StringVar(&password, "password", "", "POP3 password (prompted if omitted on a TTY)")
	cmd.Flags().StringVar(&name, "name", "", "human label")
	cmd.Flags().StringVar(&tls, "tls", "", "transport security: tls|starttls (default tls)")
	cmd.Flags().Int64Var(&interval, "interval", 0, "poll interval in minutes (5–1440, default 15)")
	cmd.Flags().BoolVar(&deleteAfterFetch, "delete-after-fetch", false, "delete messages from the server after fetching (default true)")
	cmd.Flags().StringVar(&label, "label", "", "folder to file fetched mail into (created on first use; default INBOX)")
	cmd.Flags().BoolVar(&markRead, "mark-read", false, "mark every imported message read (default: only mail older than the source)")
	return cmd
}

func newPickupUpdateCmd(a *app) *cobra.Command {
	var (
		host, username, password, name, tls, label string
		markRead                                   bool
		port, interval                             int64
		deleteAfterFetch, enabled, disabled        bool
		clearName, clearLabel                      bool
	)
	cmd := &cobra.Command{
		Use:   "update <pickupId>",
		Short: "Update a pickup source (partial; password write-only, omit to keep)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if enabled && disabled {
				return usageError(errors.New("--enabled and --disabled are mutually exclusive"))
			}
			if cmd.Flags().Changed("tls") && tls != "tls" && tls != "starttls" {
				return usageError(errors.New("--tls must be 'tls' or 'starttls'"))
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("host") {
				patch["host"] = host
			}
			if cmd.Flags().Changed("port") {
				patch["port"] = port
			}
			if cmd.Flags().Changed("username") {
				patch["username"] = username
			}
			if cmd.Flags().Changed("password") {
				patch["password"] = password
			}
			if cmd.Flags().Changed("tls") {
				patch["tls"] = tls
			}
			if cmd.Flags().Changed("interval") {
				patch["intervalMinutes"] = interval
			}
			if cmd.Flags().Changed("delete-after-fetch") {
				patch["deleteAfterFetch"] = deleteAfterFetch
			}
			if clearName {
				patch["name"] = nil
			} else if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			if clearLabel {
				patch["labelName"] = nil
			} else if cmd.Flags().Changed("label") {
				patch["labelName"] = label
			}
			if cmd.Flags().Changed("mark-read") {
				patch["markRead"] = markRead
			}
			if enabled {
				patch["enabled"] = true
			}
			if disabled {
				patch["enabled"] = false
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			p, err := client.UpdatePickup(cmd.Context(), mbx, args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Updated pickup source %s", p.ID)
				printPickup(w, a.out, p)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "POP3 server hostname")
	cmd.Flags().Int64Var(&port, "port", 0, "POP3 server port")
	cmd.Flags().StringVar(&username, "username", "", "POP3 username")
	cmd.Flags().StringVar(&password, "password", "", "new POP3 password (omit to keep the current one)")
	cmd.Flags().StringVar(&name, "name", "", "human label")
	cmd.Flags().BoolVar(&clearName, "clear-name", false, "clear the display name")
	cmd.Flags().StringVar(&label, "label", "", "folder to file fetched mail into (created on first use)")
	cmd.Flags().BoolVar(&clearLabel, "clear-label", false, "file fetched mail into INBOX again")
	cmd.Flags().StringVar(&tls, "tls", "", "transport security: tls|starttls")
	cmd.Flags().Int64Var(&interval, "interval", 0, "poll interval in minutes (5–1440)")
	cmd.Flags().BoolVar(&deleteAfterFetch, "delete-after-fetch", false, "delete messages from the server after fetching")
	cmd.Flags().BoolVar(&markRead, "mark-read", false, "mark every imported message read (--mark-read=false: only mail older than the source)")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "enable the source")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "disable the source")
	return cmd
}

func newPickupDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <pickupId>",
		Aliases: []string{"rm"},
		Short:   "Delete a pickup source",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete pickup source %s?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			if err := client.DeletePickup(cmd.Context(), mbx, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "id": args[0]}, func(w io.Writer) {
				a.out.Successf("Deleted pickup source %s", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newPickupRunCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "run <pickupId>",
		Short: "Schedule an out-of-band pickup run now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			status, err := client.RunPickup(cmd.Context(), mbx, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"status": status}, func(w io.Writer) {
				a.out.Successf("Pickup %s %s", args[0], strOrEmpty(status, "scheduled"))
			})
			return nil
		},
	}
}

func pickupStatus(p coreapi.PickupSource) string {
	if p.LastStatus == nil {
		return "—"
	}
	if p.LastRunAt != nil {
		return *p.LastStatus + " @ " + fmtEpoch(*p.LastRunAt)
	}
	return *p.LastStatus
}

// int64PtrOr renders a nullable count, dash when nothing has been reported.
func int64PtrOr(v *int64, dash string) string {
	if v == nil {
		return dash
	}
	return fmt.Sprintf("%d", *v)
}

func printPickup(w io.Writer, p *Printer, s *coreapi.PickupSource) {
	rows := [][]string{
		{"ID", s.ID},
		{"Name", strOr(s.Name, "—")},
		{"Host", fmt.Sprintf("%s:%d", s.Host, s.Port)},
		{"Username", s.Username},
		{"TLS", s.TLS},
		{"Interval", fmt.Sprintf("%d min", s.IntervalMinutes)},
		{"Delete after fetch", boolYN(s.DeleteAfterFetch)},
		{"Files into", strOr(s.LabelName, "INBOX")},
		{"Mark read", boolYN(s.MarkRead)},
		{"Enabled", boolYN(s.Enabled)},
		{"Last run", fmtEpochPtr(s.LastRunAt)},
		{"Last status", strOr(s.LastStatus, "—")},
		{"Last imported", int64PtrOr(s.LastImportedCount, "—")},
		{"Consecutive failures", fmt.Sprintf("%d", s.ConsecutiveFailures)},
		{"Created", fmtEpoch(s.CreatedAt)},
	}
	if s.LastError != nil && *s.LastError != "" {
		rows = append(rows, []string{"Last error", *s.LastError})
	}
	// The scheduler's own last attempt — the only line that can explain a
	// source that never reports a run at all (nothing is being dispatched).
	if d := s.Dispatch; d != nil {
		rows = append(rows, []string{"Last dispatch", fmtEpochPtr(d.LastAttemptAt)})
		rows = append(rows, []string{"Dispatch outcome", strOr(d.Outcome, "—")})
		if d.Detail != nil && *d.Detail != "" {
			rows = append(rows, []string{"Dispatch detail", *d.Detail})
		}
		rows = append(rows, []string{"Next run", fmtEpochPtr(d.NextRunAt)})
	}
	printTable(w, p, []string{"FIELD", "VALUE"}, rows)
}
