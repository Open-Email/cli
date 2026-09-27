package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// newTemplatesCmd is the tenant's PREPARED EMAILS (core
// docs/templated-mail-design.md §VI): stored, per-language messages a backend
// sends by name with variables filled in per recipient. Core owns the template
// language, the ceilings, the From check and the rendering; these commands
// write the copy, preview it and send it.
//
// Copy is a subgroup, `variants`, named after core's own path
// (/templates/:slug/variants/:lang), so what a person types and what the API
// reference says are one word.
func newTemplatesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "templates",
		Aliases: []string{"template", "tpl"},
		Short:   "Prepared emails: stored copy your backend sends by name",
		Long: "A template is a message written once, in as many languages as you need, that\n" +
			"your code sends by name with the details filled in per recipient. Each send is an\n" +
			"ordinary message from the template's From address, one per recipient.\n\n" +
			"  openemail templates create order-shipped --name \"Order shipped\" --from orders@acme.com\n" +
			"  openemail templates variants put order-shipped en --subject \"Shipped: {{orderRef}}\" --text-file en.txt\n" +
			"  openemail templates render order-shipped --var orderRef=A-1\n" +
			"  openemail templates send order-shipped --to customer@example.com --var orderRef=A-1\n\n" +
			"Writing a language's copy PUBLISHES it: the next send in that language uses it.\n" +
			"Preview a change first with `templates render <slug> --subject ... --text ...`,\n" +
			"which renders it through core without storing anything.",
	}
	cmd.AddCommand(
		newTemplatesListCmd(a),
		newTemplatesGetCmd(a),
		newTemplatesCreateCmd(a),
		newTemplatesUpdateCmd(a),
		newTemplatesDeleteCmd(a),
		newTemplateVariantsCmd(a),
		newTemplatesRenderCmd(a),
		newTemplatesSendCmd(a),
	)
	return cmd
}

/* ── shared flags ─────────────────────────────────────────────────────────── */

// copyFlags are one language's subject, text and optional HTML. Shared by the
// template PUT, a template draft render, and the three mail-slot writes, so
// the same flags mean the same thing everywhere copy is typed.
type copyFlags struct {
	subject, text, textFile, html, htmlFile string
}

func (f *copyFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.subject, "subject", "", "subject line (may use {{variables}})")
	fl.StringVar(&f.text, "text", "", "plain-text body")
	fl.StringVar(&f.textFile, "text-file", "", "read the plain-text body from a file (- for stdin)")
	fl.StringVar(&f.html, "html", "", "HTML body (optional)")
	fl.StringVar(&f.htmlFile, "html-file", "", "read the HTML body from a file (- for stdin)")
}

// given reports whether any copy flag was set: on a render, that is what makes
// the call a draft.
func (f *copyFlags) given(cmd *cobra.Command) bool {
	for _, name := range []string{"subject", "text", "text-file", "html", "html-file"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// read assembles the copy, requiring a subject and a text body: core refuses a
// variant without either, and an HTML-only body in particular, so the refusal
// happens here, with exit 2, before a request leaves.
func (f *copyFlags) read(cmd *cobra.Command) (*coreapi.VariantCopy, error) {
	if f.text != "" && f.textFile != "" {
		return nil, usageError(errors.New("--text and --text-file are mutually exclusive"))
	}
	if f.html != "" && f.htmlFile != "" {
		return nil, usageError(errors.New("--html and --html-file are mutually exclusive"))
	}
	text, err := inlineOrFile(f.text, f.textFile)
	if err != nil {
		return nil, err
	}
	html, err := inlineOrFile(f.html, f.htmlFile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.subject) == "" {
		return nil, usageError(errors.New("--subject is required"))
	}
	if strings.TrimSpace(text) == "" {
		return nil, usageError(errors.New("a text body is required (--text or --text-file): every mail client can show text, and some show nothing else"))
	}
	copy := &coreapi.VariantCopy{Subject: f.subject, Text: text}
	if html != "" {
		copy.HTML = &html
	}
	return copy, nil
}

// inlineOrFile returns the file's content when one is named ("-" is stdin),
// else the inline value.
func inlineOrFile(inline, file string) (string, error) {
	if file == "" {
		return inline, nil
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// varFlags are the values a render or a send fills the copy with: --var
// name=value (text), and --vars-file for typed values (a flag stays a flag:
// core reads the STRING "false" as true). A --var wins over the file for its
// name.
type varFlags struct {
	pairs []string
	file  string
}

func (v *varFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&v.pairs, "var", nil, "a value as name=value (repeatable; the value is text)")
	cmd.Flags().StringVar(&v.file, "vars-file", "", "values as a JSON object, keeping their types (true, 12, \"text\")")
}

func (v *varFlags) read() (map[string]any, error) {
	out := map[string]any{}
	if v.file != "" {
		raw, err := inlineOrFile("", v.file)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, usageError(fmt.Errorf("--vars-file must hold a JSON object: %w", err))
		}
	}
	for _, pair := range v.pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, usageError(fmt.Errorf("--var %q: expected name=value", pair))
		}
		out[strings.TrimSpace(name)] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// templateAccount is the account a template command acts on: the key's own,
// or --account for a system key (one resolution, shared with the account's
// address lists).
func (a *app) templateAccount(cmd *cobra.Command, client *coreapi.Client, flag string) (string, error) {
	id, _, err := suppressionScope(cmd.Context(), a, client, flag)
	return id, err
}

/* ── the directory ────────────────────────────────────────────────────────── */

func newTemplatesListCmd(a *app) *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the account's templates and the languages each has copy in",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			list, err := client.ListTemplates(cmd.Context(), accountID)
			if err != nil {
				return err
			}
			a.out.Emit(list, func(w io.Writer) {
				if len(list) == 0 {
					a.out.Msgf("no templates yet: create one with `openemail templates create <slug> --name ... --from ...`")
					return
				}
				rows := make([][]string, 0, len(list))
				for _, t := range list {
					rows = append(rows, []string{t.Slug, t.Name, strOr(t.From, "not set"), languagesOf(t.Languages), fmtEpoch(t.UpdatedAt)})
				}
				printTable(w, a.out, []string{"SLUG", "NAME", "FROM", "LANGUAGES", "CHANGED"}, rows)
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	return cmd
}

func languagesOf(langs []string) string {
	if len(langs) == 0 {
		return "no copy yet"
	}
	return strings.Join(langs, ", ")
}

func newTemplatesGetCmd(a *app) *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:   "get <slug>",
		Short: "Show a template: its details, its copy per language and its variables",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			t, err := client.GetTemplate(cmd.Context(), accountID, args[0])
			if err != nil {
				return err
			}
			a.out.Emit(t, func(w io.Writer) {
				printTable(w, a.out, []string{"FIELD", "VALUE"}, [][]string{
					{"Slug", t.Slug},
					{"Name", t.Name},
					{"From", strOr(t.From, "not set")},
					{"Reply-To", strOr(t.ReplyTo, "not set (replies go to the From address)")},
					{"Default language", t.DefaultLang},
					{"Version", fmt.Sprintf("%d", t.Version)},
					{"Changed", fmtEpoch(t.UpdatedAt)},
				})
				if len(t.Variants) == 0 {
					a.out.Msgf("\nno copy yet: write it with `openemail templates variants put %s %s --subject ... --text ...`", t.Slug, t.DefaultLang)
				} else {
					rows := make([][]string, 0, len(t.Variants))
					for _, v := range t.Variants {
						lang := v.Lang
						if lang == t.DefaultLang {
							lang += " (default)"
						}
						rows = append(rows, []string{lang, v.Subject, boolYN(v.HTML != nil), fmtEpoch(v.UpdatedAt)})
					}
					fmt.Fprintln(w)
					printTable(w, a.out, []string{"LANGUAGE", "SUBJECT (AS WRITTEN)", "HTML", "CHANGED"}, rows)
				}
				if len(t.Variables) > 0 {
					fmt.Fprintln(w)
					printTable(w, a.out, []string{"VARIABLE", "DESCRIPTION", "SAMPLE"}, declarationRows(t.Variables))
				}
			})
			return nil
		},
	}
	accountFlag(cmd, &account)
	return cmd
}

// declarationRows renders the declared variables, sorted by name.
func declarationRows(vars map[string]coreapi.VariableDeclaration) [][]string {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		d := vars[name]
		rows = append(rows, []string{"{{" + name + "}}", strOr(d.Description, "not described"), sampleText(d.Sample)})
	}
	return rows
}

// sampleText prints a sample as its JSON value would read, "none" for absent.
func sampleText(v any) string {
	switch s := v.(type) {
	case nil:
		return "none"
	case string:
		return s
	default:
		raw, _ := json.Marshal(s)
		return string(raw)
	}
}

func newTemplatesCreateCmd(a *app) *cobra.Command {
	var account, name, from, replyTo, defaultLang, variablesFile string
	cmd := &cobra.Command{
		Use:   "create <slug>",
		Short: "Register a template (its copy is written next, per language)",
		Long: "Registers a template under a slug your code will send it by: lowercase letters,\n" +
			"digits, - and _. --from must be an address this account may already send as.\n" +
			"--variables-file documents the variables as JSON, name -> {description, sample};\n" +
			"a sample is what the preview fills a variable with.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" || strings.TrimSpace(from) == "" {
				return usageError(errors.New("--name and --from are required"))
			}
			if defaultLang != "" && !isLanguageTag(defaultLang) {
				return usageError(fmt.Errorf("--default-lang %q is not a language tag (like en, de or pt-br)", defaultLang))
			}
			in := coreapi.TemplateCreate{Slug: args[0], Name: name, From: from, ReplyTo: replyTo, DefaultLang: strings.ToLower(defaultLang)}
			if variablesFile != "" {
				raw, err := inlineOrFile("", variablesFile)
				if err != nil {
					return err
				}
				if !json.Valid([]byte(raw)) {
					return usageError(errors.New("--variables-file is not valid JSON"))
				}
				in.Variables = json.RawMessage(raw)
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			t, err := client.CreateTemplate(cmd.Context(), accountID, in)
			if err != nil {
				return err
			}
			a.out.Emit(t, func(io.Writer) {
				a.out.Successf("Created template %s", t.Slug)
				a.out.Msgf("next: openemail templates variants put %s %s --subject ... --text-file ...", t.Slug, t.DefaultLang)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "what your team calls it (required)")
	cmd.Flags().StringVar(&from, "from", "", "the address it is sent from (required)")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "where replies go, if not to --from")
	cmd.Flags().StringVar(&defaultLang, "default-lang", "", "the language a send gets when it asks for one with no copy (default en)")
	cmd.Flags().StringVar(&variablesFile, "variables-file", "", "variable declarations as JSON: {\"orderRef\":{\"description\":\"...\",\"sample\":\"A-1\"}}")
	accountFlag(cmd, &account)
	return cmd
}

func newTemplatesUpdateCmd(a *app) *cobra.Command {
	var account, name, from, replyTo, defaultLang, variablesFile string
	cmd := &cobra.Command{
		Use:   "update <slug>",
		Short: "Change a template's name, From, Reply-To, default language or variable declarations",
		Long: "Only the flags given are sent; an empty --reply-to clears it. The slug cannot\n" +
			"change: it is what your code sends the template by.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			if cmd.Flags().Changed("from") {
				patch["from"] = from
			}
			if cmd.Flags().Changed("reply-to") {
				if replyTo == "" {
					patch["replyTo"] = nil
				} else {
					patch["replyTo"] = replyTo
				}
			}
			if cmd.Flags().Changed("default-lang") {
				if !isLanguageTag(defaultLang) {
					return usageError(fmt.Errorf("--default-lang %q is not a language tag (like en, de or pt-br)", defaultLang))
				}
				patch["defaultLang"] = strings.ToLower(defaultLang)
			}
			if variablesFile != "" {
				raw, err := inlineOrFile("", variablesFile)
				if err != nil {
					return err
				}
				if !json.Valid([]byte(raw)) {
					return usageError(errors.New("--variables-file is not valid JSON"))
				}
				patch["variables"] = json.RawMessage(raw)
			}
			if len(patch) == 0 {
				return usageError(errors.New("nothing to update: pass --name, --from, --reply-to, --default-lang or --variables-file"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			t, err := client.UpdateTemplate(cmd.Context(), accountID, args[0], patch)
			if err != nil {
				return err
			}
			a.out.Emit(t, func(io.Writer) { a.out.Successf("Updated template %s (version %d)", t.Slug, t.Version) })
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "rename it (the slug stays)")
	cmd.Flags().StringVar(&from, "from", "", "send it from this address")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "where replies go; empty clears it")
	cmd.Flags().StringVar(&defaultLang, "default-lang", "", "the language a send gets when it asks for one with no copy")
	cmd.Flags().StringVar(&variablesFile, "variables-file", "", "replace the variable declarations with this JSON")
	accountFlag(cmd, &account)
	return cmd
}

func newTemplatesDeleteCmd(a *app) *cobra.Command {
	var account string
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <slug>",
		Aliases: []string{"rm"},
		Short:   "Delete a template and its copy in every language",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(fmt.Sprintf("Delete template %s? A backend still sending it gets not_found from then on.", args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			if err := client.DeleteTemplate(cmd.Context(), accountID, args[0]); err != nil {
				return err
			}
			a.out.Emit(map[string]any{"slug": args[0], "deleted": true}, func(io.Writer) {
				a.out.Successf("Deleted template %s", args[0])
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	accountFlag(cmd, &account)
	return cmd
}

/* ── one language's copy ──────────────────────────────────────────────────── */

func newTemplateVariantsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "variants",
		Aliases: []string{"variant", "copy"},
		Short:   "One language's copy of a template: get, put, delete",
	}
	cmd.AddCommand(newTemplateVariantGetCmd(a), newTemplateVariantPutCmd(a), newTemplateVariantDeleteCmd(a))
	return cmd
}

func newTemplateVariantGetCmd(a *app) *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:   "get <slug> <lang>",
		Short: "Print one language's copy as written, {{tags}} and all",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			t, err := client.GetTemplate(cmd.Context(), accountID, args[0])
			if err != nil {
				return err
			}
			lang := strings.ToLower(args[1])
			for _, v := range t.Variants {
				if v.Lang != lang {
					continue
				}
				a.out.Emit(v, func(w io.Writer) { printCopy(w, v.Subject, v.Text, v.HTML) })
				return nil
			}
			return fmt.Errorf("%s has no %s copy (it has: %s)", t.Slug, lang, languagesOf(t.Languages))
		},
	}
	accountFlag(cmd, &account)
	return cmd
}

// printCopy writes a subject and bodies the way a person reads a message.
func printCopy(w io.Writer, subject, text string, html *string) {
	fmt.Fprintf(w, "Subject: %s\n\n%s\n", subject, strings.TrimRight(text, "\n"))
	if html != nil {
		fmt.Fprintf(w, "\n--- HTML ---\n%s\n", strings.TrimRight(*html, "\n"))
	}
}

func newTemplateVariantPutCmd(a *app) *cobra.Command {
	var account string
	var copyF copyFlags
	cmd := &cobra.Command{
		Use:   "put <slug> <lang>",
		Short: "Write one language's copy (it is published: the next send uses it)",
		Long: "Replaces the language's copy whole: subject, text and, if given, HTML (leaving\n" +
			"out --html removes an HTML body). Core checks it first; a copy it cannot parse\n" +
			"is refused and nothing is stored. Preview a change before publishing it with\n" +
			"`openemail templates render <slug> --lang <lang> --subject ... --text-file ...`.",
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
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			res, err := client.PutTemplateVariant(cmd.Context(), accountID, args[0], strings.ToLower(args[1]), *copy)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				if !res.Changed {
					a.out.Msgf("unchanged: the %s copy of %s already said exactly this", res.Lang, res.Slug)
					return
				}
				a.out.Successf("Published the %s copy of %s: the next send in %s uses it", res.Lang, res.Slug, res.Lang)
			})
			return nil
		},
	}
	copyF.register(cmd)
	accountFlag(cmd, &account)
	return cmd
}

func newTemplateVariantDeleteCmd(a *app) *cobra.Command {
	var account string
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <slug> <lang>",
		Aliases: []string{"rm"},
		Short:   "Remove one language's copy (sends asking for it get the default language)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			lang := strings.ToLower(args[1])
			if !yes && !confirm(fmt.Sprintf("Remove the %s copy of %s?", lang, args[0])) {
				return usageError(errors.New("aborted (pass --yes to skip confirmation)"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			res, err := client.DeleteTemplateVariant(cmd.Context(), accountID, args[0], lang)
			if err != nil {
				return err
			}
			a.out.Emit(res, func(io.Writer) {
				if !res.Removed {
					a.out.Msgf("%s had no %s copy", res.Slug, res.Lang)
					return
				}
				a.out.Successf("Removed the %s copy of %s", res.Lang, res.Slug)
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	accountFlag(cmd, &account)
	return cmd
}

/* ── render and send ──────────────────────────────────────────────────────── */

func newTemplatesRenderCmd(a *app) *cobra.Command {
	var account, lang, recipient string
	var preview bool
	var vars varFlags
	var copyF copyFlags
	cmd := &cobra.Command{
		Use:   "render <slug>",
		Short: "Show what a send would produce, or preview a draft (sends and stores nothing)",
		Long: "Renders the stored copy with the values given. --preview fills any variable you\n" +
			"leave out from its declared sample. Give --subject and --text (or --text-file) to\n" +
			"render THAT copy instead, as a draft: core holds it to the checks a save makes and\n" +
			"stores nothing, so a change can be seen before it is published.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := vars.read()
			if err != nil {
				return err
			}
			if lang != "" && !isLanguageTag(lang) {
				return usageError(fmt.Errorf("--lang %q is not a language tag", lang))
			}
			in := coreapi.TemplateRenderInput{Lang: strings.ToLower(lang), Vars: values, Preview: preview}
			if recipient != "" {
				in.Recipient = &coreapi.RenderRecipient{Email: recipient}
			}
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
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			out, err := client.RenderTemplate(cmd.Context(), accountID, args[0], in)
			if err != nil {
				return err
			}
			a.out.Emit(out, func(w io.Writer) {
				what := "stored copy, version " + fmt.Sprintf("%d", out.TemplateVersion)
				if out.Source == "draft" {
					what = "a DRAFT: nothing was stored"
				}
				a.out.Msgf("%s in %s (%s)\n", a.out.Bold(out.Slug), out.LangUsed, what)
				printCopy(w, out.Subject, out.Text, out.HTML)
				if len(out.Variables) > 0 {
					fmt.Fprintln(w)
					printTable(w, a.out, []string{"VARIABLE", "USED IN", "SAMPLE", "FILLED BY SAMPLE"}, renderVariableRows(out.Variables))
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "the language to render (default: the template's default language)")
	cmd.Flags().StringVar(&recipient, "recipient", "", "an address to fill {{recipient.email}} with")
	cmd.Flags().BoolVar(&preview, "preview", false, "fill any variable not given from its declared sample")
	vars.register(cmd)
	copyF.register(cmd)
	accountFlag(cmd, &account)
	return cmd
}

func renderVariableRows(vars []coreapi.MailTemplateVariable) [][]string {
	rows := make([][]string, 0, len(vars))
	for _, v := range vars {
		rows = append(rows, []string{"{{" + v.Name + "}}", strings.Join(v.Bodies, ", "), sampleText(v.Sample), boolYN(v.FilledFromSample)})
	}
	return rows
}

func newTemplatesSendCmd(a *app) *cobra.Command {
	var account, lang, toFile, replyTo, deliveryID string
	var to, headers []string
	var save, noBounce bool
	var vars varFlags
	cmd := &cobra.Command{
		Use:   "send <slug>",
		Short: "Send a template: one message per recipient, now",
		Long: "Renders the template once per recipient and sends each as its own message from\n" +
			"the template's From address. --to adds a recipient (repeatable); --to-file reads\n" +
			"a JSON array of {\"email\", \"name\", \"vars\"} for values that differ per recipient.\n\n" +
			"--delivery-id names the batch (default: a fresh id). If some recipients fail, run\n" +
			"the same command again with the same --delivery-id: core converges per recipient,\n" +
			"so those already sent are not sent twice.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := vars.read()
			if err != nil {
				return err
			}
			if lang != "" && !isLanguageTag(lang) {
				return usageError(fmt.Errorf("--lang %q is not a language tag", lang))
			}
			recipients := make([]coreapi.TemplateRecipient, 0, len(to))
			for _, addr := range to {
				recipients = append(recipients, coreapi.TemplateRecipient{Email: addr})
			}
			if toFile != "" {
				raw, err := inlineOrFile("", toFile)
				if err != nil {
					return err
				}
				var more []coreapi.TemplateRecipient
				if err := json.Unmarshal([]byte(raw), &more); err != nil {
					return usageError(fmt.Errorf("--to-file must hold a JSON array of {\"email\",\"name\",\"vars\"}: %w", err))
				}
				recipients = append(recipients, more...)
			}
			if len(recipients) == 0 {
				return usageError(errors.New("no recipient: pass --to <address> (repeatable) or --to-file <recipients.json>"))
			}
			in := coreapi.TemplateSendInput{Lang: strings.ToLower(lang), Vars: values, To: recipients, ReplyTo: replyTo}
			if len(headers) > 0 {
				in.Headers = map[string]string{}
				for _, h := range headers {
					name, value, ok := strings.Cut(h, ":")
					if !ok || strings.TrimSpace(name) == "" {
						return usageError(fmt.Errorf("--header %q: expected Name: value", h))
					}
					in.Headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
				}
			}
			if cmd.Flags().Changed("save") {
				in.Save = &save
			}
			if noBounce {
				bounce := false
				in.Bounce = &bounce
			}
			if deliveryID == "" {
				deliveryID = newDeliveryID()
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			accountID, err := a.templateAccount(cmd, client, account)
			if err != nil {
				return err
			}
			out, err := client.SendTemplate(cmd.Context(), accountID, args[0], in, deliveryID)
			if err != nil {
				return err
			}
			failed := 0
			for _, r := range out.Results {
				if r.Status == "failed" {
					failed++
				}
			}
			a.out.Emit(out, func(w io.Writer) {
				rows := make([][]string, 0, len(out.Results))
				for _, r := range out.Results {
					reason := r.Error
					if r.RetryAfterSeconds != nil {
						reason = fmt.Sprintf("%s (retry after %ds)", reason, *r.RetryAfterSeconds)
					}
					rows = append(rows, []string{r.Address, r.Status, reason})
				}
				printTable(w, a.out, []string{"RECIPIENT", "STATUS", "REASON"}, rows)
				a.out.Msgf("batch %s, %s copy, version %d", out.DeliveryID, out.LangUsed, out.TemplateVersion)
			})
			if failed > 0 {
				a.out.Warnf("%d of %d recipients failed; resend with --delivery-id %s to retry only those", failed, len(out.Results), out.DeliveryID)
				return silentExit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&to, "to", nil, "a recipient address (repeatable)")
	cmd.Flags().StringVar(&toFile, "to-file", "", "recipients as a JSON array of {\"email\",\"name\",\"vars\"}")
	cmd.Flags().StringVar(&lang, "lang", "", "the language to send (default: the template's default language)")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "override the template's Reply-To for this send")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "an extra header as \"Name: value\" (repeatable)")
	cmd.Flags().StringVar(&deliveryID, "delivery-id", "", "the batch's idempotency key (default: a fresh ULID; reuse it to retry)")
	cmd.Flags().BoolVar(&save, "save", false, "keep a Sent copy in the sending mailbox (off by default here)")
	cmd.Flags().BoolVar(&noBounce, "no-bounce", false, "do not deliver a bounce notice on terminal failure")
	vars.register(cmd)
	accountFlag(cmd, &account)
	return cmd
}
