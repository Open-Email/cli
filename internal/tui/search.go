package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Open-Email/cli/internal/coreapi"
	"github.com/charmbracelet/huh"
)

// searchSpec is one query, built by the form and replayed by every page fetch.
// It is deliberately a small fixed set rather than the whole JMAP condition
// grammar: a console cannot usefully offer an arbitrary boolean tree, and the
// CLI's `openemail search` already does when someone needs it.
type searchSpec struct {
	text       string
	from       string
	subject    string
	unreadOnly bool
	hasAttach  bool
}

// filter renders the spec as a JMAP FilterCondition. A single condition is sent
// bare; several are ANDed. Core allows at most ONE full-text condition and
// never under OR/NOT, which this shape satisfies by construction — `text` and
// `subject` are both full-text, so the form treats them as alternatives.
func (s searchSpec) filter() coreapi.EmailSearchFilter {
	var conds []any
	if t := strings.TrimSpace(s.text); t != "" {
		conds = append(conds, map[string]any{"text": t})
	} else if sub := strings.TrimSpace(s.subject); sub != "" {
		conds = append(conds, map[string]any{"subject": sub})
	}
	if f := strings.TrimSpace(s.from); f != "" {
		conds = append(conds, map[string]any{"from": f})
	}
	if s.unreadOnly {
		conds = append(conds, map[string]any{"notKeyword": "$seen"})
	}
	if s.hasAttach {
		conds = append(conds, map[string]any{"hasAttachment": true})
	}
	switch len(conds) {
	case 0:
		return nil
	case 1:
		return coreapi.EmailSearchFilter(conds[0].(map[string]any))
	default:
		return coreapi.EmailSearchFilter{"operator": "AND", "conditions": conds}
	}
}

func (s searchSpec) empty() bool { return len(s.filter()) == 0 }

// describe is the header line: what was actually asked, so a surprising result
// set can be read against the query that produced it.
func (s searchSpec) describe() string {
	var parts []string
	if t := strings.TrimSpace(s.text); t != "" {
		parts = append(parts, "text "+strconv.Quote(t))
	} else if sub := strings.TrimSpace(s.subject); sub != "" {
		parts = append(parts, "subject "+strconv.Quote(sub))
	}
	if f := strings.TrimSpace(s.from); f != "" {
		parts = append(parts, "from "+strconv.Quote(f))
	}
	if s.unreadOnly {
		parts = append(parts, "unread")
	}
	if s.hasAttach {
		parts = append(parts, "has attachment")
	}
	return strings.Join(parts, " · ")
}

// sortNewestFirst is the address taken by every result page's comparator. It is
// never mutated; a package-level false exists only because the wire field is a
// pointer (so that "descending" can be SENT rather than omitted).
var sortNewestFirst = false

// searchHit pairs a result with its highlighted excerpt.
type searchHit struct {
	meta    coreapi.MessageMeta
	snippet string
}

// searchDesc is one query's result listing. Search is OFFSET-paged rather than
// cursor-paged, so the screenPane's opaque cursor carries the next position —
// the pane never inspects it, which is what makes the substitution safe.
func searchDesc(mbx coreapi.Mailbox, spec searchSpec) resourceDesc {
	st := &searchScreenState{spec: spec}
	return resourceDesc{
		key:  "search:" + mbx.ID,
		name: "Search — " + mailboxLabel(&mbx),
		summary: func(w int) []string {
			return st.summaryLines(w)
		},
		columns: []column{
			{title: "DATE", width: 16},
			{title: "FLAGS", width: 5},
			{title: "FROM", width: 24},
			{title: "SUBJECT", flex: true},
			{title: "MATCH", flex: true},
		},
		fetch: func(ctx context.Context, c *coreapi.Client, cursor string) ([]rowData, string, error) {
			pos, _ := strconv.ParseInt(cursor, 10, 64) // "" → 0, the first page
			res, err := c.SearchQuery(ctx, mbx.ID, coreapi.EmailSearchRequest{
				Filter: spec.filter(),
				// isAscending must be sent EXPLICITLY. It is a *bool with
				// omitempty, and core reads an absent value as ASCENDING
				// (`isAscending !== false`) — so omitting it puts the OLDEST
				// matches on page one, while every other listing in this console
				// is newest-first and core's own no-sort default is descending.
				Sort:     []coreapi.EmailSearchComparator{{Property: "receivedAt", IsAscending: &sortNewestFirst}},
				Position: pos,
				Limit:    pageLimit,
				// The total is what turns "50 results" into "50 of 812" — the
				// difference between a complete answer and a first page.
				CalculateTotal: pos == 0,
				Snippet:        true,
			})
			if err != nil {
				return nil, "", err
			}
			if pos == 0 {
				st.set(res)
			}
			snips := make(map[string]string, len(res.Snippets))
			for _, sn := range res.Snippets {
				snips[sn.ID] = snippetText(sn)
			}
			rows := make([]rowData, len(res.Results))
			for i, m := range res.Results {
				rows[i] = rowData{
					cells: []string{
						fmtEpoch(m.ReceivedAt), fmtMsgFlags(m.Flags),
						truncate(m.EnvelopeFrom, 24), strOr(m.Subject, "(no subject)"),
						snips[m.ID],
					},
					item: searchHit{meta: m, snippet: snips[m.ID]},
				}
			}
			// A short page is the end of the results. Without a total there is
			// nothing else to test, and asking for one more page than exists
			// costs a round trip that answers empty.
			next := ""
			if len(res.Results) == pageLimit {
				next = strconv.FormatInt(pos+int64(len(res.Results)), 10)
			}
			return rows, next, nil
		},
		open: func(ctx context.Context, ui *Options, item any) pane {
			return newPreviewPane(ctx, ui, mbx.ID, item.(searchHit).meta)
		},
		actions: []action{
			{key: "n", label: "n new search", run: func(ctx context.Context, ui *Options, _ any) pane {
				return searchFormPane(ctx, ui, mbx)
			}},
		},
	}
}

// searchScreenState is written by the fetch goroutine and read by the render
// loop — see sieveScreenState for why that needs a mutex. `spec` is immutable
// for the pane's lifetime and needs no guard.
type searchScreenState struct {
	mu     sync.Mutex
	spec   searchSpec
	loaded bool
	res    *coreapi.EmailSearchResult
}

func (s *searchScreenState) set(res *coreapi.EmailSearchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.res = true, res
}

func (s *searchScreenState) summaryLines(w int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := s.spec.describe()
	if !s.loaded || s.res == nil {
		return []string{stDim.Render(truncate(line, w))}
	}
	if s.res.Total != nil {
		line += fmt.Sprintf("  —  %d match(es)", *s.res.Total)
		if *s.res.Total == 0 {
			return []string{stDim.Render(truncate(line, w))}
		}
	}
	return []string{stLive.Render(truncate(line, w))}
}

// snippetText flattens a highlighted excerpt to one line. The <mark> tags are
// stripped rather than styled: the excerpt shares a table cell with real
// content, and half a color escape at a truncation boundary corrupts the row.
func snippetText(sn coreapi.EmailSearchSnippet) string {
	s := strOr(sn.Preview, "")
	if s == "" {
		s = strOr(sn.Subject, "")
	}
	s = strings.ReplaceAll(s, "<mark>", "")
	s = strings.ReplaceAll(s, "</mark>", "")
	return strings.Join(strings.Fields(s), " ")
}

// searchFormPane collects a query and opens its results.
func searchFormPane(ctx context.Context, ui *Options, mbx coreapi.Mailbox) pane {
	spec := searchSpec{}
	return newFormPane(ctx, ui, formSpec{
		title: "Search — " + mailboxLabel(&mbx),
		build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Text").Value(&spec.text).
					Description("full-text over subject and body"),
				huh.NewInput().Title("Subject").Value(&spec.subject).
					Description("used only when Text is empty — core allows one full-text term"),
				huh.NewInput().Title("From").Value(&spec.from).
					Description("substring of the sender address or display name"),
				huh.NewConfirm().Title("Unread only").Value(&spec.unreadOnly).Affirmative("yes").Negative("no"),
				huh.NewConfirm().Title("Has attachment").Value(&spec.hasAttach).Affirmative("yes").Negative("no"),
			))
		},
		submit: func(_ context.Context, c *coreapi.Client) (string, pane, error) {
			if spec.empty() {
				return "", nil, fmt.Errorf("enter at least one term to search for")
			}
			// The results replace this form on the stack rather than flashing
			// back to the message list. The pane is built on the PARENT ctx, not
			// the submit's: that one carries the submit timeout and is cancelled
			// the moment this returns, so every fetch the pane made would die.
			return "", newScreenPane(ctx, ui, searchDesc(mbx, spec)), nil
		},
	})
}
