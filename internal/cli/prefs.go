package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

func newPrefsCmd(a *app) *cobra.Command {
	var identity bool
	cmd := &cobra.Command{
		Use:   "prefs",
		Short: "Read and write the opaque client preferences blob",
		Long: "Client preferences are an opaque JSON document core stores but never\n" +
			"interprets — the webmail and other clients keep their settings here.\n" +
			"This group exists to inspect, back up, and restore that document.\n\n" +
			"The blob belongs to the IDENTITY, so it follows a user across every facet\n" +
			"and device. --identity only changes which path spelling is used; both\n" +
			"reach the same document, since an identity and its mail store share one id.\n\n" +
			"Writes carry a version for compare-and-swap: `put` sends the version it\n" +
			"read unless you pass --force, so two devices cannot silently clobber\n" +
			"each other (412 version_conflict on a stale write).",
	}
	addMailboxFlag(cmd, a)
	cmd.PersistentFlags().BoolVar(&identity, "identity", false,
		"address the blob by its identity path (same document; the honest spelling for a mail-less identity)")
	scope := func() coreapi.PrefsScope {
		if identity {
			return coreapi.PrefsIdentity
		}
		return coreapi.PrefsMailbox
	}
	cmd.AddCommand(
		newPrefsGetCmd(a, scope),
		newPrefsPutCmd(a, scope),
		newPrefsSetCmd(a, scope),
	)
	return cmd
}

func newPrefsGetCmd(a *app, scope func() coreapi.PrefsScope) *cobra.Command {
	var outPath string
	var raw bool
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Print the preferences document (with its version)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			p, err := client.GetPrefs(cmd.Context(), scope(), id)
			if err != nil {
				return err
			}
			// --raw prints just the blob, so `prefs get --raw | prefs put` is a
			// clean round trip; the default keeps the version visible.
			payload := any(p)
			if raw {
				payload = p.Prefs
			}
			blob, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			blob = append(blob, '\n')
			if outPath != "" {
				if err := os.WriteFile(outPath, blob, 0o600); err != nil {
					return err
				}
				a.out.Successf("Wrote %s (version %d)", outPath, p.Version)
				return nil
			}
			os.Stdout.Write(blob)
			if !a.out.JSON() && !raw {
				a.out.Msgf("version %d — pass it to `prefs put --if-match %d` for a safe write", p.Version, p.Version)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "write to a file instead of stdout")
	cmd.Flags().BoolVar(&raw, "raw", false, "print only the prefs object (no version envelope)")
	return cmd
}

func newPrefsPutCmd(a *app, scope func() coreapi.PrefsScope) *cobra.Command {
	var file, ifMatch string
	var force, createOnly bool
	cmd := &cobra.Command{
		Use:   "put",
		Short: "Replace the whole preferences document (compare-and-swap by default)",
		Long: "Replace the document from --file or stdin. The body is either the bare prefs\n" +
			"object or the {\"prefs\": …, \"version\": n} envelope `prefs get` prints — in the\n" +
			"latter case the version is used as the CAS guard automatically.\n\n" +
			"--if-match <version> sets the guard explicitly, --create-only writes only if\n" +
			"no document exists yet, and --force overwrites whatever is there.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if force && (ifMatch != "" || createOnly) {
				return usageError(errors.New("--force cannot be combined with --if-match/--create-only"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			body, err := readRawInput(file)
			if err != nil {
				return err
			}
			prefs, embedded, err := parsePrefsInput(body)
			if err != nil {
				return usageError(err)
			}
			guard := ifMatch
			switch {
			case force:
				guard = ""
			case createOnly:
				guard = "0"
			case guard == "" && embedded != nil:
				guard = strconv.FormatInt(*embedded, 10)
			}
			id, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			p, err := client.PutPrefs(cmd.Context(), scope(), id, prefs, guard)
			if err != nil {
				if ae, ok := coreapi.AsAPIError(err); ok && ae.Status == 412 {
					a.out.Warnf("someone else wrote these prefs since you read them — re-read and re-apply, or pass --force")
				}
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Preferences saved (version %d)", p.Version)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the document from a file (default: stdin)")
	cmd.Flags().StringVar(&ifMatch, "if-match", "", "write only if the stored version matches")
	cmd.Flags().BoolVar(&createOnly, "create-only", false, "write only if no document exists yet")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite unconditionally (no compare-and-swap)")
	return cmd
}

// newPrefsSetCmd edits single keys, so the common case needs no JSON wrangling.
func newPrefsSetCmd(a *app, scope func() coreapi.PrefsScope) *cobra.Command {
	var unset []string
	cmd := &cobra.Command{
		Use:   "set [key=value ...]",
		Short: "Set or remove individual top-level keys (read-modify-write, CAS-guarded)",
		Long: "Set individual keys without hand-editing the document. Values are parsed as\n" +
			"JSON when they parse (numbers, booleans, objects, arrays) and kept as strings\n" +
			"otherwise, so `theme=dark`, `density=3` and `panes={\"left\":220}` all do what\n" +
			"they look like. --unset removes a key.\n\n" +
			"The write is guarded by the version just read, so a concurrent change is\n" +
			"refused rather than silently overwritten.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(unset) == 0 {
				return usageError(errors.New("nothing to do — pass key=value pairs and/or --unset key"))
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			id, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}
			cur, err := client.GetPrefs(cmd.Context(), scope(), id)
			if err != nil {
				return err
			}
			prefs := cur.Prefs
			if prefs == nil {
				prefs = map[string]any{}
			}
			for _, kv := range args {
				key, value, ok := strings.Cut(kv, "=")
				if !ok || key == "" {
					return usageError(fmt.Errorf("invalid pair %q: expected key=value", kv))
				}
				prefs[key] = parsePrefValue(value)
			}
			for _, key := range unset {
				delete(prefs, key)
			}
			p, err := client.PutPrefs(cmd.Context(), scope(), id, prefs, strconv.FormatInt(cur.Version, 10))
			if err != nil {
				if ae, ok := coreapi.AsAPIError(err); ok && ae.Status == 412 {
					a.out.Warnf("these prefs changed while you were editing — re-run to apply on top of the new version")
				}
				return err
			}
			a.out.Emit(p, func(w io.Writer) {
				a.out.Successf("Preferences updated (version %d)", p.Version)
			})
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&unset, "unset", nil, "remove this key, repeatable")
	return cmd
}

// parsePrefsInput accepts the bare prefs object or the versioned envelope,
// returning the blob and — for the envelope — the version to use as the CAS
// guard.
func parsePrefsInput(raw []byte) (map[string]any, *int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil, errors.New("empty input: expected a JSON object")
	}
	var envelope struct {
		Prefs   map[string]any `json:"prefs"`
		Version *int64         `json:"version"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err == nil && envelope.Prefs != nil {
		return envelope.Prefs, envelope.Version, nil
	}
	var bare map[string]any
	if err := json.Unmarshal([]byte(trimmed), &bare); err != nil {
		return nil, nil, fmt.Errorf("invalid prefs JSON: %w", err)
	}
	return bare, nil, nil
}

// parsePrefValue keeps a value's JSON type when it has one, so a number stays
// a number; anything that is not JSON is a plain string.
//
// A QUOTED value decodes to its contents, which is the escape hatch for a
// string that looks like something else: `zip='"01234"'` stores the string
// "01234" where a bare 01234 would become the number 1234.
func parsePrefValue(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		return out
	}
	return v
}
