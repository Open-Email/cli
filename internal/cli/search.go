package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/spf13/cobra"
)

// searchFlags carries the structured-search flag set. Any of them switches the
// verb from the text-only GET to POST /search/query.
type searchFlags struct {
	from, to, cc     string
	subject, body    string
	before, after    string
	minSize, maxSize string
	hasAttachment    bool
	unread, flagged  bool
	hasKeyword       []string
	notKeyword       []string
	sort             []string
	position         int64
	total            bool
	snippet          bool
}

// structured reports whether any flag requires the structured endpoint.
func (s *searchFlags) structured(cmd *cobra.Command) bool {
	for _, name := range []string{
		"from", "to", "cc", "subject", "body", "before", "after",
		"min-size", "max-size", "has-attachment", "unread", "flagged",
		"has-keyword", "not-keyword", "sort", "position", "total", "snippet",
	} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func newSearchCmd(a *app) *cobra.Command {
	var (
		label       string
		limit       int
		cursor      string
		groupThread bool
		all         bool
		sf          searchFlags
	)
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search a mailbox: full text, or structured filters over any field",
		Long: "Search a mailbox's messages.\n\n" +
			"With just a query it runs a full-text search. Add any structured flag\n" +
			"(--from/--before/--unread/--sort/…) and it switches to the filter search,\n" +
			"which pages by offset (--position) rather than by cursor.\n\n" +
			"Core allows at most ONE full-text condition, so a bare query and --subject\n" +
			"or --body are mutually exclusive.\n\n" +
			"Dates take RFC3339, YYYY-MM-DD, or a relative form like 7d/24h.\n" +
			"Sizes take bytes or a k/m/g suffix. --sort takes property[:asc|desc],\n" +
			"repeatable, e.g. --sort receivedAt:desc --sort size:asc.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			client, err := a.authedClient()
			if err != nil {
				return err
			}
			mbx, err := a.resolveMailbox(cmd.Context(), client, "")
			if err != nil {
				return err
			}

			if sf.structured(cmd) {
				return runStructuredSearch(cmd, a, client, mbx, query, label, limit, groupThread, all, cursor, &sf)
			}
			if query == "" {
				return usageError(errors.New("pass a query, or structured flags (see --help)"))
			}
			if groupThread && (cursor != "" || all) {
				return usageError(errors.New("--cursor/--all are not allowed with --group-thread (grouped search is single-page)"))
			}
			if all && cursor != "" {
				return usageError(errors.New("--all cannot be combined with --cursor"))
			}

			if all {
				results, derr := coreapi.Depaginate(cmd.Context(), func(ctx context.Context, cur string) (coreapi.Page[coreapi.MessageMeta], error) {
					r, e := client.Search(ctx, mbx, query, label, limit, cur, false)
					if e != nil {
						return coreapi.Page[coreapi.MessageMeta]{}, e
					}
					return coreapi.Page[coreapi.MessageMeta]{Items: r.Results, NextCursor: r.NextCursor}, nil
				})
				if derr != nil {
					return derr
				}
				a.out.Emit(map[string]any{"results": results, "nextCursor": ""}, func(w io.Writer) {
					printTable(w, a.out, messageListHeaders, messageListRows(results, ""))
				})
				return nil
			}

			res, err := client.Search(cmd.Context(), mbx, query, label, limit, cursor, groupThread)
			if err != nil {
				return err
			}
			a.out.Emit(map[string]any{"results": res.Results, "nextCursor": res.NextCursor}, func(w io.Writer) {
				printTable(w, a.out, messageListHeaders, messageListRows(res.Results, ""))
				a.moreHint(res.NextCursor)
			})
			return nil
		},
	}
	addMailboxFlag(cmd, a)
	cmd.Flags().StringVar(&label, "label", "", "restrict to one label")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results per page (text: 1–100 default 25; structured: 1–200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from a previous page (text search only)")
	cmd.Flags().BoolVar(&groupThread, "group-thread", false, "one result per conversation")
	cmd.Flags().BoolVar(&all, "all", false, "fetch every page (text search only)")

	cmd.Flags().StringVar(&sf.from, "from", "", "From contains")
	cmd.Flags().StringVar(&sf.to, "to", "", "To contains")
	cmd.Flags().StringVar(&sf.cc, "cc", "", "Cc contains")
	cmd.Flags().StringVar(&sf.subject, "subject", "", "subject full-text match (excludes a bare query)")
	cmd.Flags().StringVar(&sf.body, "body", "", "body full-text match (excludes a bare query)")
	cmd.Flags().StringVar(&sf.before, "before", "", "received before this date (RFC3339, YYYY-MM-DD, or 7d/24h ago)")
	cmd.Flags().StringVar(&sf.after, "after", "", "received after this date (RFC3339, YYYY-MM-DD, or 7d/24h ago)")
	cmd.Flags().StringVar(&sf.minSize, "min-size", "", "at least this size (bytes, or k/m/g)")
	cmd.Flags().StringVar(&sf.maxSize, "max-size", "", "at most this size (bytes, or k/m/g)")
	cmd.Flags().BoolVar(&sf.hasAttachment, "has-attachment", false, "only messages with attachments")
	cmd.Flags().BoolVar(&sf.unread, "unread", false, "only unread messages")
	cmd.Flags().BoolVar(&sf.flagged, "flagged", false, "only flagged messages")
	cmd.Flags().StringArrayVar(&sf.hasKeyword, "has-keyword", nil, "message has this keyword (seen, flagged, draft, answered, …), repeatable")
	cmd.Flags().StringArrayVar(&sf.notKeyword, "not-keyword", nil, "message lacks this keyword, repeatable")
	cmd.Flags().StringArrayVar(&sf.sort, "sort", nil, "sort as property[:asc|desc] (receivedAt, size, sentAt, from, subject), repeatable")
	cmd.Flags().Int64Var(&sf.position, "position", 0, "zero-based offset into the results (negative counts from the end)")
	cmd.Flags().BoolVar(&sf.total, "total", false, "also report the total number of matches")
	cmd.Flags().BoolVar(&sf.snippet, "snippet", false, "also show highlighted excerpts of the matched text")
	return cmd
}

func runStructuredSearch(cmd *cobra.Command, a *app, client *coreapi.Client, mbx, query, label string,
	limit int, groupThread, all bool, cursor string, sf *searchFlags) error {
	if all || cursor != "" {
		return usageError(errors.New("--all/--cursor are cursor-paging flags; structured search pages by --position"))
	}
	// Sort is validated first: it is pure syntax, so a malformed comparator
	// should name itself rather than lose to the "no criteria" complaint a
	// sort-only invocation also triggers.
	sort, err := buildSearchSort(sf.sort)
	if err != nil {
		return usageError(err)
	}
	filter, err := buildSearchFilter(cmd.Context(), client, mbx, query, label, sf)
	if err != nil {
		return usageError(err)
	}
	req := coreapi.EmailSearchRequest{
		Filter: filter, Sort: sort, Position: sf.position,
		CalculateTotal: sf.total, CollapseThreads: groupThread, Snippet: sf.snippet,
	}
	if limit > 0 {
		req.Limit = int64(limit)
	}
	res, err := client.SearchQuery(cmd.Context(), mbx, req)
	if err != nil {
		return err
	}
	a.out.Emit(res, func(w io.Writer) {
		printTable(w, a.out, messageListHeaders, messageListRows(res.Results, ""))
		if sf.snippet {
			printSnippets(a, res.Snippets)
		}
		shown := int64(len(res.Results))
		switch {
		case res.Total != nil:
			a.out.Msgf("results %d–%d of %d", res.Position+1, res.Position+shown, *res.Total)
		case shown > 0:
			a.out.Msgf("results %d–%d (pass --total for the match count)", res.Position+1, res.Position+shown)
		}
		if shown > 0 {
			a.out.Msgf("next page: --position %d", res.Position+shown)
		}
	})
	return nil
}

// printSnippets renders the highlighted excerpts under the result table,
// converting core's <mark> spans to terminal highlighting.
func printSnippets(a *app, snippets []coreapi.EmailSearchSnippet) {
	if len(snippets) == 0 {
		return
	}
	a.out.Msgf("")
	for _, s := range snippets {
		if s.Subject != nil && *s.Subject != "" {
			a.out.Msgf("%s  %s", a.out.Dim(truncate(s.ID, 26)), renderMarks(a, *s.Subject))
		}
		if s.Preview != nil && *s.Preview != "" {
			a.out.Msgf("%s  %s", strings.Repeat(" ", 26), renderMarks(a, *s.Preview))
		}
	}
}

var markRe = regexp.MustCompile(`<mark>(.*?)</mark>`)

// renderMarks turns <mark>…</mark> spans into colored text. Any other markup
// is left verbatim — core emits only <mark>, and inventing an HTML parser here
// would be the wrong kind of clever.
func renderMarks(a *app, s string) string {
	return markRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := markRe.FindStringSubmatch(m)[1]
		return a.out.Yellow(inner)
	})
}

// buildSearchFilter assembles the filter object, enforcing core's
// one-full-text-condition rule locally so the error names the flags the user
// actually typed.
func buildSearchFilter(ctx context.Context, client *coreapi.Client, mbx, query, label string, sf *searchFlags) (coreapi.EmailSearchFilter, error) {
	f := coreapi.EmailSearchFilter{}
	fullText := 0
	if query != "" {
		f["text"] = query
		fullText++
	}
	if sf.subject != "" {
		f["subject"] = sf.subject
		fullText++
	}
	if sf.body != "" {
		f["body"] = sf.body
		fullText++
	}
	if fullText > 1 {
		return nil, errors.New("only one full-text condition is allowed: use a bare query, --subject, or --body — not several")
	}
	if sf.from != "" {
		f["from"] = sf.from
	}
	if sf.to != "" {
		f["to"] = sf.to
	}
	if sf.cc != "" {
		f["cc"] = sf.cc
	}
	if sf.before != "" {
		t, err := parseSearchDate(sf.before)
		if err != nil {
			return nil, fmt.Errorf("--before: %w", err)
		}
		f["before"] = t
	}
	if sf.after != "" {
		t, err := parseSearchDate(sf.after)
		if err != nil {
			return nil, fmt.Errorf("--after: %w", err)
		}
		f["after"] = t
	}
	if sf.minSize != "" {
		n, err := parseByteSize(sf.minSize)
		if err != nil {
			return nil, fmt.Errorf("--min-size: %w", err)
		}
		f["minSize"] = n
	}
	if sf.maxSize != "" {
		n, err := parseByteSize(sf.maxSize)
		if err != nil {
			return nil, fmt.Errorf("--max-size: %w", err)
		}
		f["maxSize"] = n
	}
	if sf.hasAttachment {
		f["hasAttachment"] = true
	}

	has := append([]string{}, sf.hasKeyword...)
	not := append([]string{}, sf.notKeyword...)
	if sf.unread {
		not = append(not, "seen")
	}
	if sf.flagged {
		has = append(has, "flagged")
	}
	// The wire wants ONE keyword per condition; more than one of either side
	// needs the boolean tree, which is also how they combine with the rest.
	var extra []map[string]any
	for i, k := range has {
		kw := normalizeKeyword(k)
		if i == 0 {
			f["hasKeyword"] = kw
		} else {
			extra = append(extra, map[string]any{"hasKeyword": kw})
		}
	}
	for i, k := range not {
		kw := normalizeKeyword(k)
		if i == 0 {
			f["notKeyword"] = kw
		} else {
			extra = append(extra, map[string]any{"notKeyword": kw})
		}
	}

	if label != "" {
		id, err := resolveLabelID(ctx, client, mbx, label)
		if err != nil {
			return nil, err
		}
		f["inMailbox"] = id
	}

	if len(f) == 0 {
		return nil, errors.New("no search criteria — pass a query or at least one filter flag")
	}
	if len(extra) > 0 {
		conds := append([]map[string]any{map[string]any(f)}, extra...)
		return coreapi.EmailSearchFilter{"operator": "AND", "conditions": conds}, nil
	}
	return f, nil
}

// normalizeKeyword maps the CLI's bare flag vocabulary to JMAP keywords: the
// system ones are $-prefixed, user keywords pass through untouched.
func normalizeKeyword(k string) string {
	switch strings.ToLower(strings.TrimPrefix(k, "$")) {
	case "seen", "flagged", "draft", "answered", "forwarded", "phishing", "junk", "notjunk":
		return "$" + strings.ToLower(strings.TrimPrefix(k, "$"))
	}
	return k
}

// resolveLabelID turns a label NAME into the L<id> mailbox id the filter
// wants. A value already shaped like an id passes through.
func resolveLabelID(ctx context.Context, client *coreapi.Client, mbx, label string) (string, error) {
	if strings.HasPrefix(label, "L") {
		if _, err := strconv.ParseInt(label[1:], 10, 64); err == nil {
			return label, nil
		}
	}
	labels, err := client.ListLabels(ctx, mbx)
	if err != nil {
		return "", err
	}
	for _, l := range labels {
		if strings.EqualFold(l.Name, label) {
			return "L" + strconv.FormatInt(l.ID, 10), nil
		}
	}
	return "", fmt.Errorf("no label named %q in this mailbox", label)
}

// buildSearchSort parses `property[:asc|desc]` comparators.
func buildSearchSort(specs []string) ([]coreapi.EmailSearchComparator, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if len(specs) > 4 {
		return nil, errors.New("at most 4 --sort comparators")
	}
	valid := map[string]bool{
		"receivedAt": true, "size": true, "sentAt": true,
		"from": true, "subject": true, "hasKeyword": true,
	}
	out := make([]coreapi.EmailSearchComparator, 0, len(specs))
	for _, spec := range specs {
		prop, dir, hasDir := strings.Cut(spec, ":")
		keyword := ""
		// hasKeyword sorts need the keyword: `hasKeyword:flagged[:asc|desc]`.
		// A bare direction in the keyword slot ("hasKeyword:desc") is the
		// forgotten-keyword mistake, not a keyword literally named "desc".
		if prop == "hasKeyword" && hasDir {
			kw, d, has := strings.Cut(dir, ":")
			if isSortDirection(kw) && !has {
				return nil, fmt.Errorf("invalid --sort %q: hasKeyword needs a keyword, e.g. hasKeyword:flagged:desc", spec)
			}
			keyword, dir, hasDir = normalizeKeyword(kw), d, has
		}
		if !valid[prop] {
			return nil, fmt.Errorf("invalid --sort %q: property must be receivedAt, size, sentAt, from, subject, or hasKeyword", spec)
		}
		if prop == "hasKeyword" && keyword == "" {
			return nil, fmt.Errorf("invalid --sort %q: hasKeyword needs a keyword, e.g. hasKeyword:flagged:desc", spec)
		}
		c := coreapi.EmailSearchComparator{Property: prop, Keyword: keyword}
		if hasDir {
			switch strings.ToLower(dir) {
			case "asc", "ascending":
				asc := true
				c.IsAscending = &asc
			case "desc", "descending":
				asc := false
				c.IsAscending = &asc
			default:
				return nil, fmt.Errorf("invalid --sort %q: direction must be asc or desc", spec)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// isSortDirection reports whether a token is a sort direction rather than a
// value.
func isSortDirection(s string) bool {
	switch strings.ToLower(s) {
	case "asc", "ascending", "desc", "descending":
		return true
	}
	return false
}

// searchRelative matches the relative date forms (7d, 24h, 30m).
var searchRelative = regexp.MustCompile(`^(\d+)([dhm])$`)

// parseSearchDate renders a date flag as the RFC 3339 UTCDate string core's
// filter wants, accepting a relative form (7d = seven days ago), a bare date,
// or a full timestamp.
func parseSearchDate(s string) (string, error) {
	if m := searchRelative.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		var d time.Duration
		switch m[2] {
		case "d":
			d = time.Duration(n) * 24 * time.Hour
		case "h":
			d = time.Duration(n) * time.Hour
		case "m":
			d = time.Duration(n) * time.Minute
		}
		return time.Now().Add(-d).UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("unparseable date %q (RFC3339, YYYY-MM-DD, unix seconds, or 7d/24h/30m)", s)
}
