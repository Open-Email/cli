package coreapi

import (
	"context"
	"net/http"
	"net/url"
)

// SearchResult is one page of full-text search results. NextCursor is empty on
// the last page (core omits it). Grouped (by-thread) searches are single-page and
// never carry a cursor.
type SearchResult struct {
	Results    []MessageMeta
	NextCursor string
}

// EmailSearchComparator is an RFC 8620 §5.5 sort comparator. Property is one
// of receivedAt|size|sentAt|from|subject|hasKeyword; a hasKeyword comparator
// also needs Keyword. IsAscending is a pointer so "descending" (false) is sent
// rather than omitted.
type EmailSearchComparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending,omitempty"`
	Keyword     string `json:"keyword,omitempty"`
}

// EmailSearchFilter is an RFC 8621 §4.4 FilterCondition, or a boolean tree
// ({operator: AND|OR|NOT, conditions: [...]}) over them. It stays a free-form
// map because the condition set is open and core is the validator: keys are
// inMailbox, inMailboxOtherThan, before, after (RFC 3339 UTCDate strings),
// minSize, maxSize, hasKeyword, notKeyword, allInThreadHaveKeyword,
// someInThreadHaveKeyword, noneInThreadHaveKeyword, hasAttachment, from, to,
// cc, text, subject, body, header. Mailbox ids are label ids spelled L<n>.
// At most one full-text condition (text/subject/body), never under OR/NOT.
type EmailSearchFilter map[string]any

// EmailSearchRequest is the POST /search/query body — the structured search
// that reaches every JMAP filter condition, unlike the text-only GET.
type EmailSearchRequest struct {
	Filter EmailSearchFilter       `json:"filter,omitempty"`
	Sort   []EmailSearchComparator `json:"sort,omitempty"`
	// Position is a zero-based offset; a negative value counts back from the
	// end (RFC 8620 §5.5).
	Position        int64 `json:"position,omitempty"`
	Limit           int64 `json:"limit,omitempty"`
	CalculateTotal  bool  `json:"calculateTotal,omitempty"`
	CollapseThreads bool  `json:"collapseThreads,omitempty"`
	Snippet         bool  `json:"snippet,omitempty"`
}

// EmailSearchSnippet is one highlighted excerpt. Matched terms are wrapped in
// <mark>…</mark>; Subject and Preview are null when that field had no match.
type EmailSearchSnippet struct {
	ID      string  `json:"id"`
	Subject *string `json:"subject"`
	Preview *string `json:"preview"`
}

// EmailSearchResult is one page of structured search results. Total is present
// only when CalculateTotal was set, Limit only when the server narrowed the
// requested page size, and Snippets only when Snippet was set (at most the
// first 50 results of the page).
type EmailSearchResult struct {
	Results  []MessageMeta        `json:"results"`
	Position int64                `json:"position"`
	Total    *int64               `json:"total,omitempty"`
	Limit    *int64               `json:"limit,omitempty"`
	Snippets []EmailSearchSnippet `json:"snippets,omitempty"`
	// Truncated is set (true) only on a collapseThreads query whose scan hit the
	// server's row cap: the collapsed set covers the NEWEST matches rather than
	// all of them, and positional paging ends where that set does even though
	// Total (when asked for) counts the whole match set. Core sends it on EVERY
	// page of such a query, not just the last, so a client can say so from page
	// one. ScanWindow is the cap that clipped — the N in "more than N matches" —
	// and is present only beside Truncated.
	Truncated  *bool  `json:"truncated,omitempty"`
	ScanWindow *int64 `json:"scanWindow,omitempty"`
}

// SearchQuery runs a structured search. Offset-paged (Position), not
// cursor-paged: the result reports the position it answered from.
func (c *Client) SearchQuery(ctx context.Context, mailboxID string, req EmailSearchRequest) (*EmailSearchResult, error) {
	var out EmailSearchResult
	err := c.doJSON(ctx, request{
		method: http.MethodPost,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/search/query",
		body:   mustJSON(req), contentType: "application/json", idempotent: true,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Search runs a full-text query over a mailbox. label restricts to one label;
// groupThread collapses matches to one per conversation (single page — a cursor
// with groupThread is rejected 400 grouped_search_unpaginated). limit is 1..100
// (default 25). 400 empty_query / invalid_cursor on bad input.
func (c *Client) Search(ctx context.Context, mailboxID, query, label string, limit int, cursor string, groupThread bool) (SearchResult, error) {
	q := url.Values{}
	q.Set("q", query)
	if label != "" {
		q.Set("label", label)
	}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if groupThread {
		q.Set("group", "thread")
	}
	var out struct {
		Results    []MessageMeta `json:"results"`
		NextCursor string        `json:"nextCursor"`
	}
	err := c.doJSON(ctx, request{
		method: http.MethodGet,
		path:   "/mailboxes/" + escapeSegment(mailboxID) + "/search",
		query:  q, idempotent: true,
	}, &out)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Results: out.Results, NextCursor: out.NextCursor}, nil
}
