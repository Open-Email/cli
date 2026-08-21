package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// Mailbox sharing, both directions.
//
// `mailboxes shares …` is the OWNER's side — whom have I given access to — and
// `mailboxes shared` is the GRANTEE's — what do I have access to. They are
// separate commands rather than one listing with a flag because core serves
// them as separate resources keyed on different principals, and because the
// question a person is asking is genuinely one or the other.
//
// Two backend rules shape the flags and are worth reading before changing them:
//
//   - A grant NEVER allows sending as the mailbox owner, and NEVER allows
//     re-sharing. Despite the letter, `a` is label management here, not RFC
//     4314's ACL-administration right, so no rights value produces a grantee
//     who can widen their own grant.
//   - A FOLDER-SCOPED grant may carry only `lrs`. Core answers
//     `rights_not_allowed_on_folder_share` and names the offending letters,
//     which is a better message than one this client could compose, so
//     --folder is not pre-validated against --rights.
func newMailboxSharesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "Manage who else can open this mailbox (owner or account only)",
	}
	cmd.AddCommand(
		newMailShareListCmd(a),
		newMailShareSetCmd(a),
		newMailShareRemoveCmd(a),
	)
	return cmd
}

// fmtLabelScope renders a grant's reach for a table cell. The distinction the
// column has to carry is scoped vs not, so the unscoped case is spelled out
// rather than left blank — an empty cell reads as missing data.
func fmtLabelScope(scope []string) string {
	if scope == nil {
		return "whole mailbox"
	}
	return strings.Join(scope, ", ")
}

// granteeLabel prefers the address and falls back to the ULID. An address-less
// identity is a real state (a calendar-only user), not an error.
func granteeLabel(address *string, id string) string {
	if address != nil && *address != "" {
		return *address
	}
	return id
}

func newMailShareListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list [mailboxId]",
		Aliases: []string{"ls"},
		Short:   "List the grants issued on a mailbox",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			mailboxID, err := a.resolveMailbox(cmd.Context(), client, ref)
			if err != nil {
				return err
			}
			shares, err := client.ListMailShares(cmd.Context(), mailboxID)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"shares": shares}, func(w io.Writer) {
				rows := make([][]string, 0, len(shares))
				for _, sh := range shares {
					rows = append(rows, []string{
						granteeLabel(sh.GranteeAddress, sh.GranteeIdentityID),
						sh.Rights,
						fmtLabelScope(sh.LabelScope),
						fmtEpoch(sh.CreatedAt),
					})
				}
				printTable(w, a.out, []string{"GRANTEE", "RIGHTS", "SCOPE", "GRANTED"}, rows)
			})
			return nil
		},
	}
}

func newMailShareSetCmd(a *app) *cobra.Command {
	var (
		rights      string
		folders     []string
		keepFolders bool
		mailbox     string
	)
	cmd := &cobra.Command{
		Use:     "set <granteeMailbox>",
		Aliases: []string{"grant"},
		Short:   "Give another identity in your account access to this mailbox",
		Long: "Give another identity in your account access to this mailbox.\n\n" +
			"Sending a grant REPLACES any previous grant for that identity, so this is how " +
			"access is widened or narrowed as well as how it is created.\n\n" +
			"--rights takes a preset (read_only = lrs, read_write = lrswit, full = lrswitea) " +
			"or a literal letter set. Pass --folder to confine the grant to named folders; " +
			"repeat it for several. A folder-scoped grant is read-only — core refuses any " +
			"writing letter on one — and it follows a folder through a RENAME, because the " +
			"names are resolved to ids at the time of the grant.\n\n" +
			"With neither --folder nor --keep-folders the grant covers the WHOLE mailbox, " +
			"widening a folder-scoped grant if one exists. Pass --keep-folders to change " +
			"only the rights and leave the folder scope alone.\n\n" +
			"No grant ever allows sending as the mailbox owner, and no grant allows the " +
			"recipient to re-share the mailbox.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Trimmed, and blanks dropped. `--folder=` does NOT produce an empty
			// slice — cobra's StringArray appends the empty string — so a length
			// check on the raw flag would pass and send `[""]`, which core answers
			// as unknown_label. What is left after trimming is the real ask, and
			// if that is nothing the grant would be one that can see nothing:
			// core refuses it (`empty_label_scope`), so it is refused here where
			// the message can name the two ways out.
			scope := make([]string, 0, len(folders))
			for _, f := range folders {
				if f = strings.TrimSpace(f); f != "" {
					scope = append(scope, f)
				}
			}
			if cmd.Flags().Changed("folder") && len(scope) == 0 {
				return usageError(errors.New(
					"--folder was given with no folder name — name a folder, or omit --folder to share the whole mailbox"))
			}
			if keepFolders && len(scope) > 0 {
				return usageError(errors.New(
					"--keep-folders and --folder ask for opposite things — pass one or neither"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mailboxID, err := a.resolveMailbox(cmd.Context(), client, mailbox)
			if err != nil {
				return err
			}
			// The grantee is an IDENTITY, and `resolveMailbox` is the right
			// resolver for one: every identity's store id equals its identity id,
			// so an address resolves to the ULID core wants here.
			grantee, err := a.resolveMailbox(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			in := coreapi.MailShareInput{Rights: rights}
			switch {
			case len(scope) > 0:
				in.Scope, in.Folders = coreapi.ScopeFolders, scope
			case keepFolders:
				in.Scope = coreapi.ScopePreserve
			default:
				// No --folder and no --keep-folders means what the help says:
				// the whole mailbox. That has to be an EXPLICIT null — omitting
				// the field tells core to keep the folders the grant already
				// has, so a re-grant would silently stay scoped.
				in.Scope = coreapi.ScopeWholeMailbox
			}
			sh, err := client.PutMailShare(cmd.Context(), mailboxID, grantee, in)
			if err != nil {
				return err
			}
			a.out.Emit(sh, func(w io.Writer) {
				who := granteeLabel(sh.GranteeAddress, sh.GranteeIdentityID)
				if sh.LabelScope == nil {
					a.out.Successf("Granted %s on the whole mailbox to %s", sh.Rights, who)
				} else {
					a.out.Successf("Granted %s on %s to %s", sh.Rights, strings.Join(sh.LabelScope, ", "), who)
				}
				// Core NORMALIZES a preset into letters, so what was asked for and
				// what was stored differ in spelling. Saying so once stops the
				// next `list` reading as though the grant were changed.
				a.out.Msgf("rights are stored as letters; sending as the owner and re-sharing are never granted")
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&rights, "rights", "read_only",
		"read_only (lrs) | read_write (lrswit) | full (lrswitea), or a literal letter set")
	cmd.Flags().StringArrayVar(&folders, "folder", nil,
		"confine the grant to this folder (repeatable); a folder-scoped grant is read-only")
	cmd.Flags().BoolVar(&keepFolders, "keep-folders", false,
		"change only the rights, leaving the grant's existing folder scope untouched")
	cmd.Flags().StringVarP(&mailbox, "mailbox", "m", "",
		"mailbox to share (defaults to the profile's default mailbox)")
	return cmd
}

func newMailShareRemoveCmd(a *app) *cobra.Command {
	var (
		yes     bool
		mailbox string
	)
	cmd := &cobra.Command{
		Use:     "remove <granteeMailbox>",
		Aliases: []string{"rm", "revoke"},
		Short:   "Revoke an identity's access to this mailbox",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Revoke %s's access?", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			mailboxID, err := a.resolveMailbox(cmd.Context(), client, mailbox)
			if err != nil {
				return err
			}
			grantee, err := a.resolveMailbox(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if err := client.DeleteMailShare(cmd.Context(), mailboxID, grantee); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"deleted": true, "granteeIdentityId": grantee}, func(w io.Writer) {
				a.out.Successf("Revoked %s's access", grantee)
				// Worth saying because the alternative assumption is a cache: core
				// re-reads rights on every request, so an open IMAP session loses
				// the mailbox on its next command.
				a.out.Msgf("effective immediately — rights are re-read on every request")
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().StringVarP(&mailbox, "mailbox", "m", "",
		"mailbox whose grant is being revoked (defaults to the profile's default mailbox)")
	return cmd
}

func newSharedMailboxesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "shared",
		Short:   "List mailboxes other people shared with you",
		Args:    cobra.NoArgs,
		Aliases: []string{"shared-with-me"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			// No mailbox argument: core keys this on the PRINCIPAL, so it answers
			// for whoever is authenticated and cannot be asked about anyone else.
			shared, err := client.ListSharedMailboxes(cmd.Context())
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"sharedMailboxes": shared}, func(w io.Writer) {
				rows := make([][]string, 0, len(shared))
				for _, sh := range shared {
					rows = append(rows, []string{
						sh.MailboxID,
						granteeLabel(sh.OwnerAddress, sh.MailboxID),
						sh.Rights,
						fmtLabelScope(sh.LabelScope),
					})
				}
				printTable(w, a.out, []string{"MAILBOX", "OWNER", "RIGHTS", "SCOPE"}, rows)
				if len(shared) > 0 {
					// The id is the actionable column: every mailbox-scoped command
					// takes -m, and a shared mailbox is addressed by ULID because
					// the caller does not own its address.
					a.out.Msgf("read one with -m <mailbox>, e.g. `openemail messages list -m %s`", shared[0].MailboxID)
				}
			})
			return nil
		},
	}
}
