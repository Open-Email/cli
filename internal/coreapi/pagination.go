package coreapi

import (
	"context"
	"fmt"
	"net/url"
)

// Page is one page of a cursor-paginated list. NextCursor is empty on the last
// page (core omits the field, not sends null).
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// pageValues builds the shared limit/cursor query fragment.
func pageValues(limit int, cursor string) url.Values {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q
}

// ListOpts are the knobs the message, trash and thread listings share.
//
// Order and SortBy are independent axes and both default to the SERVER's
// choice when empty (desc, arrival) — they are omitted from the query rather
// than spelled out, so this client never has to track a default core owns.
//
//   - Order:  "asc" | "desc"     — direction.
//   - SortBy: "arrival" | "date" — which date. "date" is the Date header
//     (falling back to arrival where it is absent); "arrival" is the order
//     mail reached the mailbox.
//
// Both must be held CONSTANT across a cursor crawl. SortBy more strictly than
// Order: the two keys mint different cursor shapes, so core answers 400
// invalid_cursor rather than silently re-windowing the page. Depaginate holds
// one ListOpts for the whole crawl, so it cannot get this wrong.
//
// Label does not apply to the trash listing — expunged messages carry no label
// memberships, and core refuses the combination with `label_not_applicable`.
type ListOpts struct {
	Label  string
	Limit  int
	Cursor string
	Order  string
	SortBy string
}

// WithCursor is the crawl step: the same knobs at a new position. Depaginate
// takes a cursor per call, and copying the struct rather than mutating it is
// what keeps every page of a crawl on the key its cursor was minted under.
func (o ListOpts) WithCursor(cursor string) ListOpts {
	o.Cursor = cursor
	return o
}

// values renders the listing knobs, with `cursor` overridden for a crawl step.
func (o ListOpts) values(cursor string) url.Values {
	q := pageValues(o.Limit, cursor)
	if o.Label != "" {
		q.Set("label", o.Label)
	}
	if o.Order != "" {
		q.Set("order", o.Order)
	}
	if o.SortBy != "" {
		q.Set("sortBy", o.SortBy)
	}
	return q
}

// DefaultMaxPages is the safety cap on pages drained in a single --all call to prevent runaway OOM.
const DefaultMaxPages = 10000

// Depaginate drains every page by following NextCursor up to DefaultMaxPages.
func Depaginate[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (Page[T], error)) ([]T, error) {
	return DepaginateWithLimit(ctx, DefaultMaxPages, fetch)
}

// DepaginateWithLimit drains pages by following NextCursor up to maxPages.
func DepaginateWithLimit[T any](ctx context.Context, maxPages int, fetch func(ctx context.Context, cursor string) (Page[T], error)) ([]T, error) {
	var all []T
	cursor := ""
	pageCount := 0
	for {
		pageCount++
		if maxPages > 0 && pageCount > maxPages {
			return all, fmt.Errorf("coreapi: pagination exceeded maximum page limit (%d) — aborting", maxPages)
		}
		pg, err := fetch(ctx, cursor)
		if err != nil {
			return all, err
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == "" {
			return all, nil
		}
		// Stall guard: a cursor that fails to advance would loop forever and
		// accumulate unboundedly. Bail rather than hang.
		if pg.NextCursor == cursor {
			return all, fmt.Errorf("coreapi: pagination cursor did not advance (%q) — aborting", cursor)
		}
		cursor = pg.NextCursor
	}
}
