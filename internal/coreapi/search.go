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
