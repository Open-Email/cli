package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newAdminMailSlotsCmd is PLATFORM MAIL (core docs/templated-mail-design.md
// §VII): the copy of every message core writes about somebody's account, a
// recovery code, a forwarding confirmation, a limit notice. Each slot is a
// contract in code (its variables, its channel, rules such as "no links in a
// code mail") plus a compiled-in default; an operator may override the COPY
// per language, and core holds every override to the contract before storing
// it. A bare system key only; every write is audited under the operator's key.
func newAdminMailSlotsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mail-slots",
		Aliases: []string{"slots", "platform-mail"},
		Short:   "Platform mail: re-word what core writes about accounts, per language",
		Long: "Every message core writes about an account is a slot with a contract and a\n" +
			"compiled-in default. An override replaces the copy for one language; removing it\n" +
			"puts the default back. Preview a change first with `render --subject ... --text ...`,\n" +
			"which checks it against the slot's rules without storing it.",
	}
	cmd.AddCommand(
		newMailSlotsListCmd(a),
		newMailSlotsGetCmd(a),
		newMailSlotsPutCmd(a),
		newMailSlotsDeleteCmd(a),
		newMailSlotsRenderCmd(a),
		newMailSlotsTestCmd(a),
	)
	return cmd
}

func newMailSlotsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the slots, their channel and rules, and which languages are overridden",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			slots, err := client.ListMailSlots(cmd.Context())
			if err != nil {
				return err
			}
			a.out.Emit(slots, func(w io.Writer) {
				rows := make([][]string, 0, len(slots))
				for _, s := range slots {
					overridden := "none"
					if len(s.OverriddenLanguages) > 0 {
						overridden = strings.Join(s.OverriddenLanguages, ", ")
					}
					rows = append(rows, []string{s.Slug, s.Title, s.Channel, strings.Join(s.Constraints, ", "), overridden})
				}
				printTable(w, a.out, []string{"SLUG", "TITLE", "CHANNEL", "RULES", "OVERRIDDEN"}, rows)
			})
			return nil
		},
	}
}

func newMailSlotsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <slug>",
		Short: "Show a slot: its variables, its default copy and any override beside it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			slot, err := client.GetMailSlot(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.out.Emit(slot, func(w io.Writer) {
				a.out.Msgf("%s: %s (%s; rules: %s)\n", a.out.Bold(slot.Slug), slot.Title, slot.Channel, strings.Join(slot.Constraints, ", "))
				if len(slot.Variables) > 0 {
					rows := make([][]string, 0, len(slot.Variables))
					for _, v := range slot.Variables {
						need := "optional: guard it with a fallback or {{#if}}"
						if v.Required {
							need = "always supplied"
						}
						rows = append(rows, []string{"{{" + v.Name + "}}", strOr(v.Description, "not described"), sampleText(v.Sample), need})
					}
					printTable(w, a.out, []string{"VARIABLE", "DESCRIPTION", "SAMPLE", "SUPPLIED"}, rows)
				}
				for _, d := range slot.Defaults {
					fmt.Fprintf(w, "\n=== default, %s ===\n", d.Lang)
					printCopy(w, d.Subject, d.Text, d.HTML)
				}
				for _, o := range slot.Overrides {
					fmt.Fprintf(w, "\n=== OVERRIDE, %s (changed %s) ===\n", o.Lang, fmtEpoch(o.UpdatedAt))
					printCopy(w, o.Subject, o.Text, o.HTML)
				}
			})
			return nil
		},
	}
}

func newMailSlotsPutCmd(a *app) *cobra.Command {
	var copyF copyFlags
	cmd := &cobra.Command{
		Use:   "put <slug> <lang>",
		Short: "Override one language's copy (checked against the slot's rules first)",
		Long: "Replaces the copy every reader in that language gets, from the next message on.\n" +
			"Core refuses copy that breaks the slot's contract (a link in a code mail, an HTML\n" +
			"body on a text-only slot, an optional variable left unguarded) and stores nothing.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			copy, err := copyF.read(cmd)
			if err != nil {
				return err
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.PutMailSlotVariant(cmd.Context(), args[0], strings.ToLower(args[1]), *copy)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				a.out.Successf("Overrode the %s copy of %s: every %s reader gets it from the next message on", res.Lang, res.Slug, res.Lang)
			})
			return nil
		},
	}
	copyF.register(cmd)
	return cmd
}

func newMailSlotsDeleteCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <slug> <lang>",
		Aliases: []string{"rm", "revert"},
		Short:   "Remove an override: that language goes back to the default copy",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			lang := strings.ToLower(args[1])
			if !yes && !confirm(fmt.Sprintf("Revert the %s copy of %s to the default?", lang, args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.DeleteMailSlotVariant(cmd.Context(), args[0], lang)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				if !res.Removed {
					a.out.Msgf("%s had no %s override", res.Slug, res.Lang)
					return
				}
				a.out.Successf("Reverted the %s copy of %s to the default", res.Lang, res.Slug)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newMailSlotsRenderCmd(a *app) *cobra.Command {
	var lang string
	var vars varFlags
	var copyF copyFlags
	cmd := &cobra.Command{
		Use:   "render <slug>",
		Short: "Preview a slot with the registry's samples, or a draft of it (stores nothing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := vars.read()
			if err != nil {
				return err
			}
			if lang != "" && !isLanguageTag(lang) {
				return usageError(fmt.Errorf("--lang %q is not a language tag", lang))
			}
			in := coreapi.SlotRenderInput{Lang: strings.ToLower(lang), Vars: values}
			if copyF.given(cmd) {
				draft, err := copyF.read(cmd)
				if err != nil {
					return err
				}
				in.Draft = draft
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			out, err := client.RenderMailSlot(cmd.Context(), args[0], in)
			if err != nil {
				return err
			}
			a.out.Emit(out, func(w io.Writer) {
				a.out.Msgf("%s in %s (%s copy)\n", a.out.Bold(out.Slug), out.LangUsed, out.Source)
				printCopy(w, out.Subject, out.Text, out.HTML)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "the language to render (default en)")
	vars.register(cmd)
	copyF.register(cmd)
	return cmd
}

func newMailSlotsTestCmd(a *app) *cobra.Command {
	var to, lang string
	var copyF copyFlags
	cmd := &cobra.Command{
		Use:   "test <slug>",
		Short: "Send one real copy to an address you name (Email Sending slots only)",
		Long: "Renders the slot with the registry's samples (never values of your own) and\n" +
			"sends it over the slot's own channel. Give --subject and --text to test a draft\n" +
			"without publishing it. Test sends spend an hourly allowance of their own.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" {
				return usageError(errors.New("--to is required: a test send needs somebody to receive it"))
			}
			if lang != "" && !isLanguageTag(lang) {
				return usageError(fmt.Errorf("--lang %q is not a language tag", lang))
			}
			in := coreapi.SlotTestInput{To: to, Lang: strings.ToLower(lang)}
			if copyF.given(cmd) {
				draft, err := copyF.read(cmd)
				if err != nil {
					return err
				}
				in.Draft = draft
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			res, err := client.TestMailSlot(cmd.Context(), args[0], in)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				a.out.Successf("Sent the %s copy of %s to %s", res.LangUsed, res.Slug, to)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the address to send the test to (required)")
	cmd.Flags().StringVar(&lang, "lang", "", "the language to send (default en)")
	copyF.register(cmd)
	return cmd
}
